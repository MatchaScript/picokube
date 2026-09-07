package v1alpha1

import (
	"strings"
	"testing"

	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"

	"github.com/MatchaScript/picokube/internal/layouttest"
	"github.com/MatchaScript/picokube/internal/version"
)

func validInputs(t *testing.T) (*PicoKubeConfig, *kubeadmapi.InitConfiguration) {
	t.Helper()
	l := layouttest.New(t)
	wrapper := NewDefault()
	kc := &kubeadmapi.InitConfiguration{}
	kc.ClusterConfiguration.KubernetesVersion = version.KubernetesVersion
	kc.ClusterConfiguration.CertificatesDir = l.PKIDir
	return wrapper, kc
}

func TestValidate_AcceptsCanonicalInputs(t *testing.T) {
	l := layouttest.New(t)
	wrapper, kc := validInputs(t)
	kc.ClusterConfiguration.CertificatesDir = l.PKIDir
	if err := Validate(wrapper, kc, l); err != nil {
		t.Fatalf("Validate(canonical) = %v; want nil", err)
	}
}

func TestValidate_RejectsWrongAPIVersion(t *testing.T) {
	l := layouttest.New(t)
	wrapper, kc := validInputs(t)
	kc.ClusterConfiguration.CertificatesDir = l.PKIDir
	wrapper.APIVersion = "bootstrap.example.com/v1"
	err := Validate(wrapper, kc, l)
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("Validate = %v; want apiVersion error", err)
	}
}

func TestValidate_RejectsWrongKind(t *testing.T) {
	l := layouttest.New(t)
	wrapper, kc := validInputs(t)
	kc.ClusterConfiguration.CertificatesDir = l.PKIDir
	wrapper.Kind = "NotPicoKubeConfig"
	err := Validate(wrapper, kc, l)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("Validate = %v; want kind error", err)
	}
}

func TestValidate_RejectsNilKubeadm(t *testing.T) {
	l := layouttest.New(t)
	wrapper := NewDefault()
	err := Validate(wrapper, nil, l)
	if err == nil || !strings.Contains(err.Error(), "kubeadm") {
		t.Fatalf("Validate = %v; want kubeadm-nil error", err)
	}
}

func TestValidate_RejectsMismatchedKubernetesVersion(t *testing.T) {
	l := layouttest.New(t)
	wrapper, kc := validInputs(t)
	kc.ClusterConfiguration.CertificatesDir = l.PKIDir
	kc.ClusterConfiguration.KubernetesVersion = "v0.0.1-evil"
	err := Validate(wrapper, kc, l)
	if err == nil || !strings.Contains(err.Error(), "kubernetesVersion") {
		t.Fatalf("Validate = %v; want kubernetesVersion error", err)
	}
}

func TestValidate_AcceptsEmptyKubernetesVersion(t *testing.T) {
	// kubeadm defaults to a non-empty version, but the wrapper must
	// tolerate an empty value too (config.Load fills it in from the
	// image-pinned version before downstream code reads it).
	l := layouttest.New(t)
	wrapper, kc := validInputs(t)
	kc.ClusterConfiguration.CertificatesDir = l.PKIDir
	kc.ClusterConfiguration.KubernetesVersion = ""
	if err := Validate(wrapper, kc, l); err != nil {
		t.Fatalf("Validate(empty KubernetesVersion) = %v; want nil", err)
	}
}

func TestValidate_RejectsMismatchedCertificatesDir(t *testing.T) {
	l := layouttest.New(t)
	wrapper, kc := validInputs(t)
	kc.ClusterConfiguration.CertificatesDir = "/tmp/elsewhere"
	err := Validate(wrapper, kc, l)
	if err == nil || !strings.Contains(err.Error(), "certificatesDir") {
		t.Fatalf("Validate = %v; want certificatesDir error", err)
	}
}
