package certs

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/layouttest"
)

// testConfig returns a fully-defaulted *InitConfiguration with the
// picokube-specific overrides tests rely on. Defaulting goes through
// kubeadm's DefaultedStaticInitConfiguration so the fixture matches
// the shape config.Load produces in production — in particular
// Etcd.Local is non-nil, which kubeadm's PKI phases require to
// generate the etcd CA tree.
func testConfig(t *testing.T) *kubeadmapi.InitConfiguration {
	t.Helper()
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	cfg.NodeRegistration.Name = "test-node"
	cfg.LocalAPIEndpoint.AdvertiseAddress = "192.168.10.10"
	cfg.LocalAPIEndpoint.BindPort = 6443
	cfg.APIServer.CertSANs = []string{"picokube.local", "10.0.0.5"}
	cfg.CACertificateValidityPeriod = &metav1.Duration{Duration: 3650 * 24 * time.Hour}
	cfg.CertificateValidityPeriod = &metav1.Duration{Duration: 365 * 24 * time.Hour}
	return cfg
}

func testLayout(t *testing.T) layout.Layout {
	t.Helper()
	return layouttest.New(t)
}
