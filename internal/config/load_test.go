package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1alpha1 "github.com/MatchaScript/picokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/picokube/internal/layouttest"
	"github.com/MatchaScript/picokube/internal/version"
)

// writeTempFile drops body into a fresh file under t.TempDir() and
// returns its path.
func writeTempFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

// A minimum PicoKubeConfig wrapper plus the kubeadm InitConfiguration /
// ClusterConfiguration documents picokube expects. Any field worth
// asserting on per-test is added by the caller via concatenation.
const minimalConfig = `apiVersion: bootstrap.picokube.io/v1alpha1
kind: PicoKubeConfig
metadata:
  name: local
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: 192.168.1.10
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: v1.36.4
networking:
  serviceSubnet: 10.96.0.0/12
  podSubnet: 10.244.0.0/16
`

func TestLoad_MinimalConfigParsesAndDefaults(t *testing.T) {
	l := layouttest.New(t)
	cfg, err := Load(writeTempFile(t, minimalConfig), l)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LocalAPIEndpoint.AdvertiseAddress != "192.168.1.10" {
		t.Errorf("AdvertiseAddress = %q", cfg.LocalAPIEndpoint.AdvertiseAddress)
	}
	if cfg.LocalAPIEndpoint.BindPort != 6443 {
		t.Errorf("BindPort = %d; expected kubeadm default 6443", cfg.LocalAPIEndpoint.BindPort)
	}
	if cfg.CertificatesDir != l.PKIDir {
		t.Errorf("CertificatesDir = %q; want pinned %q", cfg.CertificatesDir, l.PKIDir)
	}
}

func TestLoad_FileNotFoundSurfacesPath(t *testing.T) {
	l := layouttest.New(t)
	_, err := Load("/tmp/picokube-does-not-exist-hopefully", l)
	if err == nil {
		t.Fatal("Load of missing file = nil")
	}
	if !strings.Contains(err.Error(), "/tmp/picokube-does-not-exist-hopefully") {
		t.Errorf("error should mention path; got %v", err)
	}
}

func TestLoad_RejectsMissingWrapper(t *testing.T) {
	l := layouttest.New(t)
	// Strip the PicoKubeConfig wrapper out of the minimal config.
	body := strings.SplitN(minimalConfig, "---\n", 2)[1]
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "PicoKubeConfig") {
		t.Fatalf("Load = %v; want PicoKubeConfig-not-found error", err)
	}
}

// JoinConfiguration is unimplemented and should be rejected with a
// clear message rather than silently parsed.
func TestLoad_RejectsJoinConfiguration(t *testing.T) {
	l := layouttest.New(t)
	body := minimalConfig + `---
apiVersion: kubeadm.k8s.io/v1beta4
kind: JoinConfiguration
discovery:
  bootstrapToken:
    token: aaaaaa.bbbbbbbbbbbbbbbb
    apiServerEndpoint: 10.0.0.1:6443
    unsafeSkipCAVerification: true
`
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "JoinConfiguration") {
		t.Fatalf("Load = %v; want JoinConfiguration-not-supported error", err)
	}
}

func TestLoad_RejectsCertificatesDirOverride(t *testing.T) {
	l := layouttest.New(t)
	body := minimalConfig + "certificatesDir: /tmp/elsewhere\n"
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "certificatesDir") {
		t.Fatalf("Load = %v; want certificatesDir mismatch error", err)
	}
}

func TestLoad_RejectsMismatchedKubernetesVersion(t *testing.T) {
	l := layouttest.New(t)
	// Use a syntactically valid Kubernetes version that does not match
	// the one pinned in this image — kubeadm itself rejects unparseable
	// values (e.g. v0.0.1-evil) before our gate runs, so the gate-under
	// -test is only reachable with a version kubeadm accepts.
	body := strings.Replace(minimalConfig, "kubernetesVersion: v1.36.4", "kubernetesVersion: v1.34.0", 1)
	_, err := Load(writeTempFile(t, body), l)
	if err == nil || !strings.Contains(err.Error(), "kubernetesVersion") {
		t.Fatalf("Load = %v; want kubernetesVersion mismatch error", err)
	}
}

// An unset kubernetesVersion must resolve to the version pinned in this
// image. kubeadm's own defaulter would resolve its "stable-1" label over
// the internet instead, which is both a network dependency and whatever
// version upstream released last.
func TestLoad_UnsetKubernetesVersionInheritsPinnedVersion(t *testing.T) {
	l := layouttest.New(t)
	body := strings.Replace(minimalConfig, "kubernetesVersion: v1.36.4\n", "", 1)
	cfg, err := Load(writeTempFile(t, body), l)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KubernetesVersion != version.KubernetesVersion {
		t.Errorf("KubernetesVersion = %q; want pinned %q", cfg.KubernetesVersion, version.KubernetesVersion)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	l := layouttest.New(t)
	body := "this: is: not valid: yaml: at all"
	_, err := Load(writeTempFile(t, body), l)
	if err == nil {
		t.Fatal("Load of malformed yaml = nil")
	}
}

// kubeadm's loader (validateSupportedVersion) emits a klog.Warningf for
// deprecated API versions. We don't assert on klog output (capturing
// requires plumbing -log_dir / klog.SetOutput in a test-hostile way);
// instead pin the behaviour that v1beta3 still loads successfully,
// which catches the day kubeadm drops support and Load starts
// returning an error.
func TestLoad_DeprecatedAPIVersionStillLoads(t *testing.T) {
	l := layouttest.New(t)
	body := `apiVersion: bootstrap.picokube.io/v1alpha1
kind: PicoKubeConfig
metadata:
  name: local
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: 192.168.1.10
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
kubernetesVersion: v1.36.4
networking:
  serviceSubnet: 10.96.0.0/12
  podSubnet: 10.244.0.0/16
`
	_, err := Load(writeTempFile(t, body), l)
	if err != nil {
		t.Fatalf("Load(v1beta3) = %v; v1beta3 should still be accepted with a deprecation warning. "+
			"If kubeadm has dropped v1beta3 support, update this test together with the README to "+
			"document the new minimum supported version.", err)
	}
}

func TestMarshal_RoundTripsThroughLoad(t *testing.T) {
	l := layouttest.New(t)
	in, err := Load(writeTempFile(t, minimalConfig), l)
	if err != nil {
		t.Fatalf("Load(minimal): %v", err)
	}

	// Marshal needs the wrapper too; reconstruct a default one since
	// the loader does not preserve the wrapper (its Spec is empty).
	data, err := Marshal(v1alpha1.NewDefault(), in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	out, err := Load(writeTempFile(t, string(data)), l)
	if err != nil {
		t.Fatalf("Load after Marshal: %v", err)
	}
	if out.LocalAPIEndpoint.AdvertiseAddress != in.LocalAPIEndpoint.AdvertiseAddress {
		t.Errorf("advertiseAddress round-trip: %q -> %q",
			in.LocalAPIEndpoint.AdvertiseAddress, out.LocalAPIEndpoint.AdvertiseAddress)
	}
}
