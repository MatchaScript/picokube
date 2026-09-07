// Package initialize implements `nanokube init`: the one-time node
// initialisation that mirrors `kubeadm init`'s scope.
//
// Run renders kubeadm artefacts to /etc/kubernetes, starts kubelet,
// waits for the apiserver, seeds the kubeadm:cluster-admins
// ClusterRoleBinding using a just-in-time super-admin.conf
// (system:masters-bound), removes super-admin.conf so the break-glass
// cred does not linger on a long-lived node, marks the control-plane
// node, applies addons, and repoints kubelet.conf at the client
// certificate kubelet rotates for itself. On success the cluster is
// healthy and the operator's next step is `systemctl enable
// nanokube.service` to put future reboots under supervisor control.
// lifecycle.Boot handles every reboot from then on as a pure reconcile.
//
// Recovery: a partial Run (e.g. /readyz never came up) leaves the node
// in a state state.Exists() detects, so a retry surfaces a clear
// "already exists; run reset" error. The operator-recovery path is
// uniform: `nanokube reset --yes` then `nanokube init`.
package initialize

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"k8s.io/client-go/kubernetes"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	nodebootstraptoken "k8s.io/kubernetes/cmd/kubeadm/app/phases/bootstraptoken/node"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/markcontrolplane"

	"github.com/MatchaScript/nanokube/internal/backup"
	"github.com/MatchaScript/nanokube/internal/certs"
	"github.com/MatchaScript/nanokube/internal/healthcheck"
	"github.com/MatchaScript/nanokube/internal/kubeadm"
	"github.com/MatchaScript/nanokube/internal/kubeclient"
	"github.com/MatchaScript/nanokube/internal/layout"
	"github.com/MatchaScript/nanokube/internal/ostree"
	"github.com/MatchaScript/nanokube/internal/preflight"
	"github.com/MatchaScript/nanokube/internal/state"
)

// Run executes the full one-time init. out receives human-readable
// progress logs (operator's terminal during `nanokube init`). Returns
// nil only if the cluster is verified healthy at function exit.
//
// cfg is the kubeadm InitConfiguration parsed by config.Load; nanokube
// does not add a configuration layer on top. NodeRegistration.Name has
// already been filled in by kubeadm's SetNodeRegistrationDynamicDefaults
// from the system hostname, so a separate nodeName argument is no
// longer threaded through the call graph.
func Run(ctx context.Context, cfg *kubeadmapi.InitConfiguration, l layout.Layout, selfVersion string, out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, "[init] "+format+"\n", a...) }
	nodeName := cfg.NodeRegistration.Name

	isOSTree, err := ostree.IsOSTree()
	if err != nil {
		return fmt.Errorf("detect ostree: %w", err)
	}

	checks := []preflight.Preflighter{
		preflight.FSWritable{Dirs: []string{l.NanoKubeVarDir, l.KubernetesDir}},
	}
	if isOSTree {
		checks = append(checks, backup.SpacePreflighter{Layout: l})
	}
	if err := preflight.Run(ctx, checks...); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	if err := certs.Init(cfg, l); err != nil {
		return fmt.Errorf("certs init: %w", err)
	}
	logf("provisioned PKI under %s", l.PKIDir)

	if err := kubeadm.Ensure(cfg, l); err != nil {
		return fmt.Errorf("ensure: %w", err)
	}
	logf("rendered static pod manifests and kubelet config")

	// super-admin.conf is written just-in-time so initAdminRBAC can
	// authenticate as system:masters; removeSuperAdminKubeconfig deletes
	// it again before this function returns. Ensure deliberately does not
	// produce super-admin.conf so reconcile boots cannot regenerate it.
	if err := kubeadm.WriteSuperAdminKubeconfig(cfg, l); err != nil {
		return err
	}

	if err := startKubelet(ctx, logf); err != nil {
		return err
	}

	if err := waitReadyz(ctx, l, logf); err != nil {
		return err
	}

	client, err := initAdminRBAC(l)
	if err != nil {
		return err
	}
	logf("seeded kubeadm:cluster-admins ClusterRoleBinding")

	if err := removeSuperAdminKubeconfig(l); err != nil {
		return err
	}
	logf("removed super-admin.conf (regenerate via `nanokube kubeconfig super-admin` if needed)")

	if err := waitControlPlane(ctx, client, nodeName, logf); err != nil {
		return err
	}

	if err := markcontrolplane.MarkControlPlane(client, nodeName, cfg.NodeRegistration.Taints); err != nil {
		return fmt.Errorf("mark control-plane: %w", err)
	}
	logf("marked control-plane node")

	// kubelet renews its own client certificate through the CSR API. The
	// CSRs are only auto-approved once system:nodes is bound to the
	// selfnodeclient ClusterRole, which is what this creates.
	if err := nodebootstraptoken.AutoApproveNodeCertificateRotation(client); err != nil {
		return fmt.Errorf("auto-approve node certificate rotation: %w", err)
	}
	logf("allowed auto-approval of node client certificate rotation")

	if err := kubeadm.EnsureAddons(cfg, client, out); err != nil {
		return fmt.Errorf("addons: %w", err)
	}

	if err := finalizeKubeletKubeconfig(ctx, l, logf); err != nil {
		return err
	}

	if err := writeFirstBootState(l, selfVersion, isOSTree); err != nil {
		return err
	}

	logf("init complete (node=%s, version=%s)", nodeName, selfVersion)
	logf("next step: `systemctl enable nanokube.service`")
	return nil
}

// writeFirstBootState records the just-completed init so the next
// `lifecycle.Boot` invocation sees it as the previous-boot baseline (for
// upgrade detection and backup naming).
func writeFirstBootState(l layout.Layout, selfVersion string, isOSTree bool) error {
	bootID, err := backup.BootID()
	if err != nil {
		return err
	}
	deploymentID := ""
	if isOSTree {
		deploymentID, err = ostree.BootedDeploymentID()
		if err != nil {
			return fmt.Errorf("booted deployment id: %w", err)
		}
	}
	if err := state.WriteLastBoot(l, state.LastBoot{
		Version:      selfVersion,
		DeploymentID: deploymentID,
		BootID:       bootID,
	}); err != nil {
		return err
	}
	_ = state.WriteLastEvent(l, fmt.Sprintf("initialised at %s", selfVersion))
	return nil
}

// startKubelet asks systemd to start kubelet.service without blocking
// on its readiness. Readiness is verified separately via /readyz.
// Duplicates the equivalent helper in lifecycle/boot.go: init and
// reconcile share these waits but the packages are deliberately
// independent so neither can import the other.
func startKubelet(ctx context.Context, logf func(string, ...any)) error {
	cmd := exec.CommandContext(ctx, "systemctl", "start", "--no-block", "kubelet.service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start kubelet: %v: %s", err, out)
	}
	logf("kubelet.service queued for start")
	return nil
}

// restartKubelet asks systemd to restart kubelet.service so it re-reads
// the kubelet.conf finalizeKubeletKubeconfig just rewrote. Unlike the
// initial start this blocks, because nothing downstream re-checks that
// kubelet came back.
func restartKubelet(ctx context.Context, logf func(string, ...any)) error {
	cmd := exec.CommandContext(ctx, "systemctl", "restart", "kubelet.service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart kubelet: %v: %s", err, out)
	}
	logf("kubelet.service restarted")
	return nil
}

// kubeletCertTimeout bounds how long init waits for kubelet to complete
// its first client-certificate CSR, kubeletCertInterval how often the
// rotated pem is polled for.
const (
	kubeletCertTimeout  = 90 * time.Second
	kubeletCertInterval = 2 * time.Second
)

// finalizeKubeletKubeconfig waits for the certificate kubelet requests
// on its first start, then repoints kubelet.conf at it and restarts
// kubelet so the new reference takes effect.
//
// A timeout here is not fatal: the cluster is already healthy, and the
// only consequence is that kubelet.conf keeps the embedded bootstrap
// certificate until the next `nanokube boot` finalizes it. Failing init
// over it would send the operator down the reset+init path for
// something that fixes itself on reboot.
func finalizeKubeletKubeconfig(ctx context.Context, l layout.Layout, logf func(string, ...any)) error {
	logf("waiting for kubelet client certificate rotation (timeout=%s)", kubeletCertTimeout)
	cctx, cancel := context.WithTimeout(ctx, kubeletCertTimeout)
	defer cancel()
	for {
		changed, err := kubeadm.FinalizeKubeletKubeconfig(l)
		if err != nil {
			return fmt.Errorf("finalize kubelet.conf: %w", err)
		}
		if changed {
			logf("kubelet.conf now references the rotated client certificate")
			return restartKubelet(ctx, logf)
		}
		select {
		case <-cctx.Done():
			logf("kubelet client certificate not rotated within %s; next boot will finalize kubelet.conf", kubeletCertTimeout)
			return nil
		case <-time.After(kubeletCertInterval):
		}
	}
}

// readyzTimeout bounds how long init waits for apiserver /readyz after
// asking systemd to start kubelet for the first time.
const readyzTimeout = 3 * time.Minute

func waitReadyz(ctx context.Context, l layout.Layout, logf func(string, ...any)) error {
	logf("waiting for apiserver /readyz (timeout=%s)", readyzTimeout)
	client, err := kubeclient.LoadAdmin(l.AdminKubeconfig)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, readyzTimeout)
	defer cancel()
	if err := healthcheck.WaitForAPIServer(cctx, client); err != nil {
		return err
	}
	logf("apiserver ready")
	return nil
}

// controlPlaneTimeout bounds how long init waits for node Ready + the
// three control-plane static pods Ready once /readyz responded.
const controlPlaneTimeout = 3 * time.Minute

func waitControlPlane(ctx context.Context, client kubernetes.Interface, nodeName string, logf func(string, ...any)) error {
	logf("waiting for node + control-plane static pods Ready (timeout=%s)", controlPlaneTimeout)
	cctx, cancel := context.WithTimeout(ctx, controlPlaneTimeout)
	defer cancel()
	if err := healthcheck.WaitForControlPlane(cctx, client, nodeName); err != nil {
		return err
	}
	logf("control plane ready")
	return nil
}
