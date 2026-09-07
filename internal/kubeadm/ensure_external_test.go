package kubeadm_test

import (
	"os"
	"path/filepath"
	"testing"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"github.com/MatchaScript/picokube/internal/certs"
	"github.com/MatchaScript/picokube/internal/kubeadm"
	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/layouttest"
)

func testInitConfig(t *testing.T) *kubeadmapi.InitConfiguration {
	t.Helper()
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	cfg.NodeRegistration.Name = "test-node"
	cfg.NodeRegistration.CRISocket = "unix:///var/run/crio/crio.sock"
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.168.10.10"
	cfg.LocalAPIEndpoint.BindPort = 6443
	return cfg
}

func testLayout(t *testing.T) layout.Layout {
	t.Helper()
	return layouttest.New(t)
}

func TestEnsureProducesNonCertArtifacts(t *testing.T) {
	cfg := testInitConfig(t)
	l := testLayout(t)

	if err := certs.NewSigner(cfg, l).EnsureAll(); err != nil {
		t.Fatalf("certs.EnsureAll: %v", err)
	}
	if err := kubeadm.Ensure(cfg, l); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	checks := []string{
		filepath.Join(l.ManifestsDir, "etcd.yaml"),
		filepath.Join(l.ManifestsDir, "kube-apiserver.yaml"),
		filepath.Join(l.ManifestsDir, "kube-controller-manager.yaml"),
		filepath.Join(l.ManifestsDir, "kube-scheduler.yaml"),
		filepath.Join(l.KubeletDir, "config.yaml"),
	}
	for _, p := range checks {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing artifact: %s (%v)", p, err)
		}
	}
}

// Ensure must NOT produce super-admin.conf — the system:masters-bound
// break-glass cred is created on demand by WriteSuperAdminKubeconfig
// only (during `picokube init`) and removed immediately after the
// cluster-admins CRB is seeded. If Ensure were to recreate it, every
// reconcile boot would silently undo init's deletion.
func TestEnsureDoesNotProduceSuperAdminKubeconfig(t *testing.T) {
	cfg := testInitConfig(t)
	l := testLayout(t)
	if err := certs.NewSigner(cfg, l).EnsureAll(); err != nil {
		t.Fatalf("certs.EnsureAll: %v", err)
	}
	if err := kubeadm.Ensure(cfg, l); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	path := filepath.Join(l.KubernetesDir, "super-admin.conf")
	if _, err := os.Stat(path); err == nil {
		t.Errorf("Ensure unexpectedly produced %s — every reconcile boot would resurrect the break-glass cred", path)
	}
}

// WriteSuperAdminKubeconfig is the explicit, separate writer used by
// initialize.Run and by `picokube kubeconfig super-admin`. Together
// with the test above it pins the contract: super-admin.conf only
// exists when something explicitly asks for it.
func TestWriteSuperAdminKubeconfigProducesFile(t *testing.T) {
	cfg := testInitConfig(t)
	l := testLayout(t)
	if err := certs.NewSigner(cfg, l).EnsureAll(); err != nil {
		t.Fatalf("certs.EnsureAll (prerequisite for CA): %v", err)
	}
	if err := kubeadm.WriteSuperAdminKubeconfig(cfg, l); err != nil {
		t.Fatalf("WriteSuperAdminKubeconfig: %v", err)
	}
	path := filepath.Join(l.KubernetesDir, "super-admin.conf")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("WriteSuperAdminKubeconfig did not produce %s: %v", path, err)
	}
}
