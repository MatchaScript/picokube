// Package teardown implements `picokube reset`: tearing the node back
// down to the state a fresh `picokube init` would expect. The flow
// mirrors the scope of `kubeadm reset --force`:
//
//  1. Stop kubelet (so static pods are not restarted mid-cleanup).
//  2. Lazy-unmount everything kubelet bind/tmpfs-mounted under
//     /var/lib/kubelet (projected service-account tokens, csi mounts,
//     …). Without this step os.RemoveAll trips EBUSY on the projected
//     volumes.
//  3. Remove every CRI pod sandbox (containers come along) via the
//     kubeadm CRI runtime helper.
//  4. Remove the managed filesystem paths (/etc/kubernetes,
//     /var/lib/etcd, /var/lib/kubelet, /var/lib/picokube).
//
// Network state (CNI virtual interfaces, iptables / IPVS / nftables
// rules) is deliberately left untouched. kubeadm reset does not touch
// it either, and a generic cleanup is unsafe in the presence of host
// firewalls or non-managed CNIs sharing the node. Operators who want a
// clean network slate can run `nft flush ruleset`, `ip link delete …`,
// `iptables -F` themselves.
package teardown

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/kubernetes/cmd/kubeadm/app/util/runtime"

	"github.com/MatchaScript/picokube/internal/layout"
)

// defaultCRISocket is the on-disk CRI endpoint picokube installs talk to
// (CRI-O ships in picokube's bootc image). Hardcoded here rather than
// read from PicoKubeConfig because reset must work even when the
// configuration file is corrupt or absent — the socket path is a
// property of the image, not the configuration.
const defaultCRISocket = "unix:///var/run/crio/crio.sock"

// logFn is the internal printf-shaped sink. nil out is folded to
// io.Discard at the entry point so helpers can always call logf safely.
type logFn = func(format string, a ...any)

// Run executes the full teardown. unmountKubeletMounts and the final
// RemoveAll loop are fatal; stopKubelet and removeKubeContainers are
// best-effort so a partially broken host can still be cleaned up
// (matches `kubeadm reset --force`).
func Run(ctx context.Context, l layout.Layout, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(out, "[reset] "+format+"\n", a...)
	}

	stopKubelet(ctx, logf)

	if err := unmountKubeletMounts(l, logf); err != nil {
		return fmt.Errorf("unmount kubelet mounts under %s: %w", l.KubeletDir, err)
	}

	removeKubeContainers(logf)

	for _, t := range []string{
		l.KubernetesDir,
		l.EtcdDataDir,
		l.KubeletDir,
		l.PicoKubeVarDir,
	} {
		// RemoveAll is uninterruptible; ctx cancellation cannot stop a single call.
		if err := os.RemoveAll(t); err != nil {
			return fmt.Errorf("remove %s: %w", t, err)
		}
		logf("removed %s", t)
	}

	return nil
}

// stopKubelet stops kubelet.service so static pods are not brought back
// up mid-cleanup. Non-fatal: on a fresh node kubelet may not be
// installed or enabled.
func stopKubelet(ctx context.Context, logf logFn) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "systemctl", "stop", "kubelet.service")
	if out, err := cmd.CombinedOutput(); err != nil {
		logf("systemctl stop kubelet.service (continuing): %v: %s", err, strings.TrimSpace(string(out)))
		return
	}
	logf("stopped kubelet.service")
}

// removeKubeContainers delegates to kubeadm's CRI runtime helper, which
// connects to the CRI socket via gRPC and removes every PodSandbox
// (StopPodSandbox + RemovePodSandbox, with internal retries).
// Best-effort: an unreachable CRI socket or list/remove failure is
// logged and skipped, matching `kubeadm reset`'s handling.
func removeKubeContainers(logf logFn) {
	rt := utilruntime.NewContainerRuntime(defaultCRISocket)
	if err := rt.Connect(); err != nil {
		logf("connect CRI runtime (continuing): %v", err)
		return
	}
	defer rt.Close(context.Background())

	sandboxes, err := rt.ListKubeContainers()
	if err != nil {
		logf("list CRI pod sandboxes (continuing): %v", err)
		return
	}
	if len(sandboxes) == 0 {
		return
	}
	if err := rt.RemoveContainers(sandboxes); err != nil {
		logf("remove CRI pod sandboxes (continuing): %v", err)
		return
	}
	logf("removed %d CRI pod sandboxes", len(sandboxes))
}

// unmountKubeletMounts lazy-detaches every mountpoint kubelet placed
// under l.KubeletDir. Mirrors kubeadm's
// cmd/kubeadm/app/cmd/phases/reset/unmount_linux.go, which is
// unexported and therefore impossible to call directly:
//
//   - children are unmounted before parents (reverse-sorted by path)
//     so nested binds detach cleanly.
//   - EINVAL is ignored — expected when a shared-peer mount has
//     already been unmounted via one of its other peers.
//   - other failures are aggregated and returned; RemoveAll downstream
//     would trip EBUSY on a half-mounted tree anyway, so failing here
//     surfaces a cleaner error.
func unmountKubeletMounts(l layout.Layout, logf logFn) error {
	raw, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return fmt.Errorf("read /proc/mounts: %w", err)
	}
	prefix := l.KubeletDir
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	var targets []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Split(line, " ")
		if len(fields) < 2 || !strings.HasPrefix(fields[1], prefix) {
			continue
		}
		targets = append(targets, fields[1])
	}
	sort.Sort(sort.Reverse(sort.StringSlice(targets)))

	var errs []error
	unmounted := 0
	for _, t := range targets {
		if err := syscall.Unmount(t, syscall.MNT_DETACH); err != nil {
			if err == syscall.EINVAL {
				continue
			}
			errs = append(errs, fmt.Errorf("unmount %s: %w", t, err))
			continue
		}
		unmounted++
	}
	if unmounted > 0 {
		logf("unmounted %d kubelet mounts under %s", unmounted, l.KubeletDir)
	}
	return utilerrors.NewAggregate(errs)
}
