// Package lifecycle implements `picokube boot`: the pure-reconcile flow
// picokube.service invokes on every reboot of an already-initialised
// node. One-time initialisation (PKI seeding, cluster-admins CRB,
// super-admin.conf lifecycle) lives in package initialize and is run
// by `picokube init`; Boot deliberately knows nothing about it and
// uses admin.conf alone — never falling back to super-admin.conf, which
// has been deleted by the time Boot ever runs. Aligned with microshift's
// prerun flow (reference/microshift/pkg/admin/prerun/prerun.go):
//
//  1. If the greenboot red.d hook left a restore marker, pick the
//     newest backup for the currently-booted deployment and restore
//     its data trees (etcd, /etc/kubernetes, selected kubelet files)
//     before running any reconcile logic.
//  2. Take a snapshot of the data on disk NOW (which is whatever the
//     last successful boot wrote) so that a future rollback into that
//     deployment has a backup to pick up. The backup is named after the
//     previous boot's deployment+boot ids recorded in last-boot.json.
//  3. Reconcile via kubeadm phases (Ensure), start kubelet, poll
//     /readyz, wait for node + control-plane static pods Ready, mark
//     the control-plane node (idempotent: picks up taint changes in
//     PicoKubeConfig), reconcile addons (best effort), then notify
//     systemd READY=1 so a blocking `systemctl start` only returns
//     once the cluster is actually usable.
//  4. Prune backups belonging to deployments that bootc has GCed.
//  5. Update last-boot.json and last-event. Caller idles until SIGTERM.
//
// picokube.service is Type=notify and stays Active(running) once Boot
// returns nil — the binary blocks in the caller after a healthy boot
// rather than exiting. The unit deliberately does NOT declare
// Before=kubelet.service: while we're still 'activating' systemd would
// queue our own inline `systemctl start kubelet.service` job behind
// that activation and deadlock. Instead the kubelet unit we ship
// carries no [Install] section, so multi-user.target cannot pull it
// in ahead of picokube — kubelet only ever runs because picokube asked.
//
// Greenboot's required.d/ judges boot success against the live
// control plane via `picokube healthcheck`, not against this service's
// exit code. That decoupling lets Boot treat tail bookkeeping
// (last-boot.json, last-event) as best-effort: a transient write
// failure after the cluster is verified healthy logs a warning but
// does not flip a working cluster into rollback. Failures earlier in
// the pipeline (Ensure / kubelet / readyz / control-plane wait) still
// propagate as non-zero exit because the service is then genuinely
// broken — `picokube healthcheck` would also fail, and the systemctl
// is-active gate in required.d catches the service itself being dead.
// The rollback intent is conveyed by greenboot red.d touching the
// restore marker just before bootc rolls back — there is no self-set
// "rollback-needed" flag.
//
// Non-ostree systems (no /run/ostree-booted) still run Ensure + kubelet
// but skip backup/restore entirely; atomic rollback is physically
// unavailable without an ostree/bootc deployment model.
package boot

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/coreos/go-systemd/v22/daemon"
	"k8s.io/client-go/kubernetes"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	nodebootstraptoken "k8s.io/kubernetes/cmd/kubeadm/app/phases/bootstraptoken/node"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/markcontrolplane"

	"github.com/MatchaScript/picokube/internal/backup"
	"github.com/MatchaScript/picokube/internal/certs"
	"github.com/MatchaScript/picokube/internal/healthcheck"
	"github.com/MatchaScript/picokube/internal/kubeadm"
	"github.com/MatchaScript/picokube/internal/kubeclient"
	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/ostree"
	"github.com/MatchaScript/picokube/internal/preflight"
	"github.com/MatchaScript/picokube/internal/state"
)

// Boot runs the per-reboot reconcile flow. out receives human-readable
// progress logs (journald when invoked from systemd). Returns nil on a
// healthy boot; any non-nil error means picokube.service will exit
// non-zero, which greenboot's required.d/ script turns into a boot
// failure. Boot assumes the node has already been initialised — first
// initialisation lives in package initialize. Boot uses admin.conf
// only; super-admin.conf has been deleted by then and any operator who
// regenerated it via `picokube kubeconfig super-admin` is responsible
// for cleaning it up themselves.
func Run(ctx context.Context, cfg *kubeadmapi.InitConfiguration, l layout.Layout, selfVersion string, out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, "[picokube] "+format+"\n", a...) }
	nodeName := cfg.NodeRegistration.Name

	// Preflight gates writability + free-space; AllocateWorkspace then
	// stages a clean scratch dir BEFORE Ensure / kubelet start. The
	// defer cleanup() is the single point of truth for wiping any
	// partial backup staging — backup.Create itself just returns errors
	// and stops, never rolls back its own scratch. The cleanup is a
	// no-op once a successful Create has renamed the staging dir to its
	// final name.
	isOSTree, err := ostree.IsOSTree()
	if err != nil {
		return fmt.Errorf("detect ostree: %w", err)
	}
	checks := []preflight.Preflighter{
		preflight.FSWritable{Dirs: []string{l.PicoKubeVarDir, l.KubernetesDir}},
		certs.CAExistPreflighter{Layout: l}, // CR9: gate Ensure on CA presence
	}
	if isOSTree {
		checks = append(checks, backup.SpacePreflighter{Layout: l})
	}
	if err := preflight.Run(ctx, checks...); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	workspace, cleanup, err := backup.AllocateWorkspace(l, isOSTree)
	if err != nil {
		return fmt.Errorf("allocate workspace: %w", err)
	}
	defer cleanup()
	useBackups := isOSTree

	currentDeployment := ""
	if useBackups {
		currentDeployment, err = ostree.BootedDeploymentID()
		if err != nil {
			return fmt.Errorf("booted deployment id: %w", err)
		}
	} else {
		logf("non-ostree system: backup/restore disabled")
	}

	currentBoot, err := backup.BootID()
	if err != nil {
		return err
	}

	if useBackups {
		if err := maybeRestore(l, currentDeployment, logf); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
	}

	prev, hadPrev, err := state.ReadLastBoot(l)
	if err != nil {
		return err
	}

	// Snapshot the data the previous successful boot left on disk, named
	// after that boot's (deployment, boot) ids. On the first ever boot
	// there is nothing to snapshot. On a rollback boot we may have just
	// restored; in that case the backup by this name already exists and
	// Create skips.
	//
	// CR6: gate the snapshot on prev.DeploymentID == currentDeployment.
	// After an ostree rebase the new boot runs under a different
	// deployment id, and labelling the previous boot's data with the
	// new deployment would let a future rollback restore data the old
	// binary cannot run. Skipping is the correct behaviour — operators
	// who want a pre-rebase snapshot must take it before `rpm-ostree
	// rebase`.
	if useBackups && hadPrev && prev.BootID != "" && prev.BootID != currentBoot {
		switch {
		case prev.DeploymentID == "" || prev.DeploymentID != currentDeployment:
			logf("skipping snapshot: prev deployment %s != current %s (likely ostree rebase)",
				shortID(prev.DeploymentID), shortID(currentDeployment))
			_ = state.WriteLastEvent(l, "snapshot skipped: deployment id mismatch after rebase")
		default:
			finalDir := filepath.Join(l.BackupsDir, backup.Name(prev))
			if err := backup.Create(workspace.BackupTmp, finalDir, prev, l); err != nil {
				return fmt.Errorf("create backup: %w", err)
			}
			logf("snapshot of previous boot saved as %s", shortPair(prev.DeploymentID, prev.BootID))
		}
	}

	upgrading := hadPrev && prev.Version != selfVersion
	switch {
	case !hadPrev:
		logf("first healthy boot pending (version=%s)", selfVersion)
	case upgrading:
		logf("upgrade path: %s -> %s", prev.Version, selfVersion)
		_ = state.WriteLastEvent(l, fmt.Sprintf("upgrading %s -> %s", prev.Version, selfVersion))
	default:
		logf("reconcile path (version=%s)", selfVersion)
	}

	if err := kubeadm.Ensure(cfg, l); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, fmt.Errorf("ensure: %w", err))
	}

	if err := rotateCertsIfStale(cfg, l, out); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, err)
	}

	// Point kubelet.conf at the certificate kubelet rotates for itself
	// rather than the bootstrap one certs.Init embedded. `picokube init`
	// normally did this already; this call covers the boot after an init
	// whose wait timed out. No restart is needed — kubelet reads
	// kubelet.conf on the start issued just below.
	if changed, err := kubeadm.FinalizeKubeletKubeconfig(l); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, fmt.Errorf("finalize kubelet.conf: %w", err))
	} else if changed {
		logf("kubelet.conf now references the rotated client certificate")
	}

	if err := startKubelet(ctx, logf); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, err)
	}

	if err := waitReadyz(ctx, l, logf); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, err)
	}

	// admin.conf only: the cluster-admins CRB was seeded once during
	// `picokube init` (package initialize) and super-admin.conf was
	// removed at that time. If the CRB has since been deleted by hand,
	// admin.conf cannot reach the apiserver and waitControlPlane will
	// surface the failure — recovery is `picokube reset` + `init`, not
	// a silent fallback.
	client, err := kubeclient.LoadAdmin(l.AdminKubeconfig)
	if err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, err)
	}

	// Beyond /readyz on the apiserver, confirm the node object reports
	// Ready=True and each of the three control-plane static pods is
	// Ready. Matches kinder's waitNewControlPlaneNodeReady and catches
	// CM/scheduler crash-loops that /readyz alone would miss.
	if err := waitControlPlane(ctx, client, nodeName, logf); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, err)
	}

	if err := markcontrolplane.MarkControlPlane(client, nodeName, cfg.NodeRegistration.Taints); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, fmt.Errorf("mark control-plane: %w", err))
	}

	// Re-assert the RBAC that lets the csrapprover controller
	// auto-approve kubelet's certificate-rotation CSRs. Seeded during
	// init; reconciled here so a hand-deleted binding cannot silently
	// strand kubelet on an expiring client certificate.
	if err := nodebootstraptoken.AutoApproveNodeCertificateRotation(client); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, fmt.Errorf("auto-approve node certificate rotation: %w", err))
	}

	// CR8: addon failure is fatal, matching `picokube init` and upstream
	// kubeadm. The previous log-and-continue was asymmetric and let a
	// boot succeed when the cluster was missing CoreDNS / kube-proxy.
	// kubeclient.LoadAdmin now sets Timeout=10s (CR14) so a stalled
	// apiserver TLS handshake cannot hold this fatal path open for the
	// full controlPlaneTimeout window.
	if err := kubeadm.EnsureAddons(cfg, client, out); err != nil {
		return bootFailed(l, upgrading, prev.Version, selfVersion, fmt.Errorf("addons: %w", err))
	}

	if useBackups {
		if deployments, err := ostree.AllDeploymentIDs(); err != nil {
			logf("list deployments failed (skipping prune): %v", err)
		} else if err := backup.Prune(l, deployments); err != nil {
			logf("prune failed (continuing): %v", err)
		}
	}

	// Cluster is verified healthy by this point. Treat last-boot.json
	// as bookkeeping: a transient write failure must NOT propagate to a
	// non-zero exit, because greenboot's required.d now judges boot
	// health via `picokube healthcheck` against the live apiserver, not
	// via this service's exit code. The cost of a missed write is one
	// boot of stale `prev` next time around (snapshot may use an older
	// name; backup.Create is idempotent on duplicates) — strictly less
	// damaging than rolling back a healthy cluster.
	if err := state.WriteLastBoot(l, state.LastBoot{
		Version:      selfVersion,
		DeploymentID: currentDeployment,
		BootID:       currentBoot,
	}); err != nil {
		logf("write last-boot failed (continuing): %v", err)
	}
	switch {
	case upgrading:
		_ = state.WriteLastEvent(l, fmt.Sprintf("upgraded %s -> %s", prev.Version, selfVersion))
	case !hadPrev:
		_ = state.WriteLastEvent(l, fmt.Sprintf("initialised at %s", selfVersion))
	default:
		_ = state.WriteLastEvent(l, fmt.Sprintf("healthy at %s", selfVersion))
	}
	// Cluster is verified healthy. Notify systemd READY=1 so a blocking
	// `systemctl start picokube.service` returns only once the system is
	// actually usable. The unit deliberately does NOT carry
	// Before=kubelet.service: that would make systemd queue the kubelet
	// start job we issue from inside startKubelet behind our own
	// activation, deadlocking the readyz wait. Instead we keep kubelet
	// from racing ahead by ensuring kubelet.service ships without an
	// [Install] section, so multi-user.target cannot pull it in
	// independently of picokube.
	notifyReady(logf)
	logf("boot complete")
	return nil
}

// maybeRestore consumes the greenboot-placed restore marker. If a
// backup matching the currently-booted deployment exists it is restored
// and its meta.json is written to last-boot.json so the rest of this
// boot sees the restored state as the "previous boot". The marker is
// always cleared so a stray marker cannot cause repeated restores.
func maybeRestore(l layout.Layout, currentDeployment string, logf func(string, ...any)) error {
	requested, err := backup.RestoreRequested(l)
	if err != nil {
		return err
	}
	if !requested {
		return nil
	}
	defer func() {
		if err := backup.ClearRestoreMarker(l); err != nil {
			logf("clear restore marker failed: %v", err)
		}
	}()

	if currentDeployment == "" {
		logf("restore marker present but no booted deployment id; ignoring")
		_ = state.WriteLastEvent(l, "restore requested but no deployment id")
		return nil
	}
	name, err := backup.LatestForDeployment(l, currentDeployment)
	if err != nil {
		return err
	}
	if name == "" {
		logf("restore marker present but no backup for deployment %s", shortID(currentDeployment))
		_ = state.WriteLastEvent(l, "restore requested but no backup for current deployment")
		return nil
	}

	logf("restoring backup %s", name)
	if err := backup.Restore(l, name); err != nil {
		return err
	}
	meta, err := backup.ReadMeta(l, name)
	if err != nil {
		return err
	}
	if err := state.WriteLastBoot(l, meta); err != nil {
		return err
	}
	_ = state.WriteLastEvent(l, fmt.Sprintf("restored backup %s", name))
	return nil
}

func bootFailed(l layout.Layout, upgrading bool, prev, self string, cause error) error {
	reason := cause.Error()
	switch {
	case upgrading:
		_ = state.WriteLastEvent(l, fmt.Sprintf("boot failed upgrading %s -> %s: %s", prev, self, reason))
	default:
		_ = state.WriteLastEvent(l, fmt.Sprintf("boot failed at %s: %s", self, reason))
	}
	return cause
}

// notifyReady sends sd_notify READY=1 if running under a systemd unit
// with Type=notify. Outside systemd (e.g. unit tests, manual `picokube
// boot` invocation) it is a no-op. We pass unsetEnvironment=true so
// that the systemctl/kubeadm processes we exec afterwards do not
// inherit NOTIFY_SOCKET and accidentally re-send readiness on our
// behalf.
func notifyReady(logf func(string, ...any)) {
	sent, err := daemon.SdNotify(true, daemon.SdNotifyReady)
	switch {
	case err != nil:
		logf("sd_notify READY=1 failed (continuing): %v", err)
	case sent:
		logf("sd_notify READY=1 sent")
	}
}

// startKubelet asks systemd to start kubelet.service without blocking
// on its readiness. Readiness is verified separately via /readyz.
func startKubelet(ctx context.Context, logf func(string, ...any)) error {
	cmd := exec.CommandContext(ctx, "systemctl", "start", "--no-block", "kubelet.service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start kubelet: %v: %s", err, out)
	}
	logf("kubelet.service queued for start")
	return nil
}

// readyzTimeout bounds how long we wait for apiserver /readyz after
// asking systemd to start kubelet. A stall here triggers the
// boot-failed path.
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

// controlPlaneTimeout bounds how long we wait for node Ready + the three
// control-plane static pods Ready once the apiserver itself responded to
// /readyz. Generous because CM/scheduler may take a few iterations after
// leader election + ServiceAccount token availability.
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

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func shortPair(deploy, boot string) string {
	return shortID(deploy) + "_" + shortID(boot)
}

// rotateCertsIfStale is the per-boot rotation hook called between
// kubeadm.Ensure (which depends on a complete PKI) and startKubelet
// (which reads the rotated cert files on first start).
//
// Two passes:
//  1. Check every CA. Any CA whose remaining lifetime trips
//     certs.NeedsRotation is regenerated together with every leaf and
//     kubeconfig that chains back to it (cascade).
//  2. Check every leaf. Anything still stale (CA was healthy, leaf was
//     not) is renewed in place via the kubeadm renewal manager.
//
// Step 2 is naturally idempotent against step 1: leaves regenerated by
// the CA cascade have a fresh NotAfter and will not trip NeedsRotation
// on the re-read in step 2.
func rotateCertsIfStale(cfg *kubeadmapi.InitConfiguration, l layout.Layout, out io.Writer) error {
	logf := func(format string, a ...any) { fmt.Fprintf(out, "[picokube] "+format+"\n", a...) }

	caReport, err := certs.CheckCAs(l)
	if err != nil {
		return fmt.Errorf("check CAs: %w", err)
	}
	signer := certs.NewSigner(cfg, l)
	for _, ca := range certs.AllCAs() {
		exp, ok := caReport[ca]
		if !ok || exp.NotFound {
			return fmt.Errorf("CA %s missing at boot: %s", ca, exp.Path)
		}
		if !certs.NeedsRotation(exp.Cert) {
			continue
		}
		logf("rotating CA %s (remaining=%s)", ca, exp.Remaining)
		if err := signer.RegenerateCA(ca); err != nil {
			return fmt.Errorf("regenerate CA %s: %w", ca, err)
		}
	}

	leafReport, err := certs.CheckLeaves(cfg, l)
	if err != nil {
		return fmt.Errorf("check leaves: %w", err)
	}
	var stale []certs.LeafKind
	for _, leaf := range certs.AllLeaves() {
		exp, ok := leafReport[leaf]
		if !ok || exp.NotFound {
			continue
		}
		if certs.NeedsRotation(exp.Cert) {
			stale = append(stale, leaf)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	logf("renewing %d expiring leaves", len(stale))
	if err := signer.RenewLeaves(stale); err != nil {
		return fmt.Errorf("renew leaves: %w", err)
	}
	return nil
}
