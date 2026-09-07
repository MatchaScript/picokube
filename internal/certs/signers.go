package certs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/certs"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/certs/renewal"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubeconfig"

	atomicpkg "github.com/MatchaScript/picokube/internal/atomic"
	"github.com/MatchaScript/picokube/internal/layout"
)

// Signer issues and renews the cert material under l.PKIDir +
// l.KubernetesDir. Construct one per operation; instances are
// cheap and stateless beyond the captured cfg/layout.
type Signer struct {
	cfg    *kubeadmapi.InitConfiguration
	layout layout.Layout
}

// NewSigner returns a Signer scoped to the supplied layout. The kubeadm
// InitConfiguration's CertificatesDir is normalised to l.PKIDir
// on a private copy so callers can reuse cfg with a different layout
// elsewhere without aliasing problems.
func NewSigner(cfg *kubeadmapi.InitConfiguration, l layout.Layout) *Signer {
	own := *cfg // shallow copy; ClusterConfiguration is value-embedded
	own.CertificatesDir = l.PKIDir
	return &Signer{cfg: &own, layout: l}
}

// EnsureAll generates whatever CAs and leaf certificates are missing
// under PKIDir, plus the four reconcilable kubeconfig files (admin,
// controller-manager, scheduler, kubelet) under KubeconfigDir.
//
// Existing files are validated and reused; an existing file that fails
// validation is a hard error (we deliberately do NOT silently overwrite
// a corrupt CA). super-admin.conf is intentionally not produced —
// initialize.WriteSuperAdminKubeconfig is the sole writer.
func (s *Signer) EnsureAll() error {
	for _, dir := range []string{s.layout.PKIDir, s.layout.KubernetesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := certs.CreatePKIAssets(s.cfg); err != nil {
		return fmt.Errorf("create PKI assets: %w", err)
	}

	for _, name := range []string{
		kubeadmconstants.AdminKubeConfigFileName,
		kubeadmconstants.ControllerManagerKubeConfigFileName,
		kubeadmconstants.SchedulerKubeConfigFileName,
		kubeadmconstants.KubeletKubeConfigFileName,
	} {
		if err := kubeconfig.CreateKubeConfigFile(name, s.layout.KubernetesDir, s.cfg); err != nil {
			return fmt.Errorf("create kubeconfig %s: %w", name, err)
		}
	}

	return nil
}

// renewalManager is a small helper used by RenewLeaves and CheckLeaves.
// Instantiated lazily because it loads the CA off disk.
func (s *Signer) renewalManager() (*renewal.Manager, error) {
	mgr, err := renewal.NewManager(&s.cfg.ClusterConfiguration, s.layout.KubernetesDir)
	if err != nil {
		return nil, fmt.Errorf("renewal manager: %w", err)
	}
	return mgr, nil
}

// RenewLeaves re-signs the listed leaf certificates using the existing
// CA on disk. Each name must be a LeafKind constant; super-admin.conf
// is accepted only when the file already exists, since the renewal
// manager loads the current cert before re-issuing.
//
// Backed by kubeadm's renewal.Manager.RenewUsingLocalCA, which handles
// both PKI cert files (apiserver.crt, etcd/server.crt, …) and
// kubeconfig-embedded client certs (admin.conf, scheduler.conf, …)
// behind a single name-keyed lookup.
func (s *Signer) RenewLeaves(leaves []LeafKind) error {
	mgr, err := s.renewalManager()
	if err != nil {
		return err
	}
	for _, leaf := range leaves {
		renewed, err := mgr.RenewUsingLocalCA(string(leaf))
		if err != nil {
			return fmt.Errorf("renew %s: %w", leaf, err)
		}
		if !renewed {
			// Should be unreachable: picokube always owns the CA key.
			// A false return here means the kubeadm manager could not
			// find a usable local CA, which is a programming error
			// (PKI was not initialised) rather than a renewal outcome.
			return fmt.Errorf("renew %s: renewal manager reported no-op despite local CA ownership", leaf)
		}
	}
	return nil
}

// RegenerateCA atomically replaces the named CA together with every
// PKI leaf signed by it and every kubeconfig whose embedded client
// cert chains back to it.
//
// CR2: the legacy implementation deleted the live PKI files in-place
// before calling kubeadm's CreatePKIAssets, so a mid-flight failure
// left the node bricked (no CA, no leaves, no recovery path short of
// `picokube reset`). Now:
//
//  1. Copy the current PKI tree to a sibling `<PKIDir>.regen` staging dir.
//  2. Inside staging, delete the CA + dependent leaves so
//     CreatePKIAssets (pointed at staging) regenerates them.
//  3. atomic.SwapDir the staged PKI into place. The displaced live PKI
//     ends up at the staging path and is removed.
//  4. For each dependent kubeconfig, generate it into a sibling temp
//     directory and atomically rename into place. Per-file renames are
//     atomic; a power loss between two kubeconfig renames leaves the
//     cluster with a mix of old and new kubeconfigs, which the next
//     boot's per-cert NeedsRotation check will heal on its own.
//
// super-admin.conf is intentionally NOT regenerated: it is a
// break-glass cred whose presence on a long-lived node is itself an
// invariant violation, and `picokube kubeconfig super-admin` is the
// documented path to re-issue it under the new CA.
func (s *Signer) RegenerateCA(ca CAKind) error {
	stagePKI := s.layout.PKIDir + ".regen"
	if err := os.RemoveAll(stagePKI); err != nil {
		return fmt.Errorf("clear stale regen staging %s: %w", stagePKI, err)
	}
	if err := copyPKITree(s.layout.PKIDir, stagePKI); err != nil {
		return fmt.Errorf("stage PKI tree: %w", err)
	}
	// On any failure before the swap, drop staging to leave the live PKI intact.
	swapDone := false
	defer func() {
		if !swapDone {
			_ = os.RemoveAll(stagePKI)
		}
	}()

	// Map the live-PKI paths the helpers compute to their staged
	// counterparts and delete in staging only.
	toDelete := []string{
		stagedPath(s.layout.PKIDir, stagePKI, caCertPath(s.layout, ca)),
		stagedPath(s.layout.PKIDir, stagePKI, caKeyPath(s.layout, ca)),
	}
	for _, leaf := range dependentLeaves(ca) {
		toDelete = append(toDelete, stagedPath(s.layout.PKIDir, stagePKI, leafPath(s.layout, leaf)))
		if k := leafKeyPath(s.layout, leaf); k != "" {
			toDelete = append(toDelete, stagedPath(s.layout.PKIDir, stagePKI, k))
		}
	}
	for _, p := range toDelete {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}

	// Tell kubeadm to regenerate into staging.
	stageCfg := *s.cfg
	stageCfg.CertificatesDir = stagePKI
	if err := certs.CreatePKIAssets(&stageCfg); err != nil {
		return fmt.Errorf("create PKI assets for CA %s: %w", ca, err)
	}

	// Atomic swap: stagePKI <-> live PKIDir. After this returns, the
	// new PKI is the live one and the old PKI is at stagePKI for cleanup.
	if err := atomicpkg.SwapDir(stagePKI, s.layout.PKIDir); err != nil {
		return fmt.Errorf("swap PKI: %w", err)
	}
	swapDone = true
	if err := os.RemoveAll(stagePKI); err != nil {
		return fmt.Errorf("remove displaced PKI %s: %w", stagePKI, err)
	}

	// Regenerate each dependent kubeconfig via a temp sibling + rename
	// so each file's update is atomic. A signer using the live PKIDir
	// (now the rotated one) signs each kubeconfig's embedded client cert.
	for _, name := range dependentKubeconfigs(ca) {
		if err := writeKubeconfigAtomic(name, s.layout.KubernetesDir, s.cfg); err != nil {
			return fmt.Errorf("write kubeconfig %s: %w", name, err)
		}
	}

	return nil
}

// stagedPath maps a path under live (e.g. /etc/kubernetes/pki/foo.crt)
// to its equivalent under stage (e.g. /etc/kubernetes/pki.regen/foo.crt).
// Returns "" if p is not under live, so callers can ignore those.
func stagedPath(live, stage, p string) string {
	rel, ok := strings.CutPrefix(p, live+"/")
	if !ok {
		return ""
	}
	return filepath.Join(stage, rel)
}

// copyPKITree clones the PKI directory recursively, preserving file
// modes (kubeadm asserts 0o600 on .key files). Uses a small in-process
// walk rather than shelling out — the PKI tree is < 1 MB and we don't
// need reflink semantics for the regen staging.
func copyPKITree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// writeKubeconfigAtomic generates a kubeconfig into a temp sibling
// directory and renames into place so the live kubeconfig is never a
// half-written file.
func writeKubeconfigAtomic(name, dir string, cfg *kubeadmapi.InitConfiguration) error {
	tmpDir, err := os.MkdirTemp(dir, ".kubeconfig-stage-*")
	if err != nil {
		return fmt.Errorf("mktemp under %s: %w", dir, err)
	}
	defer os.RemoveAll(tmpDir)
	if err := kubeconfig.CreateKubeConfigFile(name, tmpDir, cfg); err != nil {
		return err
	}
	src := filepath.Join(tmpDir, name)
	dst := filepath.Join(dir, name)
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", src, dst, err)
	}
	return atomicpkg.SyncParent(dst)
}
