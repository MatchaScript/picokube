package kubeadm_test

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/MatchaScript/picokube/internal/kubeadm"
	"github.com/MatchaScript/picokube/internal/layout"
)

// writeEmbeddedKubeletConf writes a kubelet.conf shaped like the one
// certs.Init produces: a single context whose user carries the client
// certificate and key inline.
func writeEmbeddedKubeletConf(t *testing.T, l layout.Layout) {
	t.Helper()
	if err := os.MkdirAll(l.KubernetesDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", l.KubernetesDir, err)
	}
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["default-cluster"] = &clientcmdapi.Cluster{Server: "https://192.168.10.10:6443"}
	cfg.AuthInfos["system:node:test-node"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: []byte("embedded-cert"),
		ClientKeyData:         []byte("embedded-key"),
	}
	cfg.Contexts["system:node:test-node@default-cluster"] = &clientcmdapi.Context{
		Cluster:  "default-cluster",
		AuthInfo: "system:node:test-node",
	}
	cfg.CurrentContext = "system:node:test-node@default-cluster"
	if err := clientcmd.WriteToFile(*cfg, l.KubeletKubeconfig); err != nil {
		t.Fatalf("write %s: %v", l.KubeletKubeconfig, err)
	}
}

// writeRotatedPEM stands in for the certificate kubelet writes once its
// first CSR is approved.
func writeRotatedPEM(t *testing.T, l layout.Layout) string {
	t.Helper()
	pkiDir := filepath.Join(l.KubeletDir, "pki")
	if err := os.MkdirAll(pkiDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", pkiDir, err)
	}
	pemPath := filepath.Join(pkiDir, "kubelet-client-current.pem")
	if err := os.WriteFile(pemPath, []byte("rotated"), 0o600); err != nil {
		t.Fatalf("write %s: %v", pemPath, err)
	}
	return pemPath
}

func currentAuthInfo(t *testing.T, l layout.Layout) *clientcmdapi.AuthInfo {
	t.Helper()
	cfg, err := clientcmd.LoadFromFile(l.KubeletKubeconfig)
	if err != nil {
		t.Fatalf("load %s: %v", l.KubeletKubeconfig, err)
	}
	kctx, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		t.Fatalf("current-context %q not in context list", cfg.CurrentContext)
	}
	info, ok := cfg.AuthInfos[kctx.AuthInfo]
	if !ok {
		t.Fatalf("no user %q for current-context", kctx.AuthInfo)
	}
	return info
}

func TestFinalizeKubeletKubeconfigRewritesEmbeddedCert(t *testing.T) {
	l := testLayout(t)
	writeEmbeddedKubeletConf(t, l)
	pemPath := writeRotatedPEM(t, l)

	changed, err := kubeadm.FinalizeKubeletKubeconfig(l)
	if err != nil {
		t.Fatalf("FinalizeKubeletKubeconfig: %v", err)
	}
	if !changed {
		t.Fatal("first call reported no change; want the embedded cert rewritten")
	}

	info := currentAuthInfo(t, l)
	if info.ClientCertificate != pemPath || info.ClientKey != pemPath {
		t.Errorf("client cert/key = %q/%q, want both %q", info.ClientCertificate, info.ClientKey, pemPath)
	}
	if len(info.ClientCertificateData) != 0 || len(info.ClientKeyData) != 0 {
		t.Error("embedded client certificate data still present after finalize")
	}

	// Second call must be a no-op: kubelet.conf already references the pem.
	changed, err = kubeadm.FinalizeKubeletKubeconfig(l)
	if err != nil {
		t.Fatalf("second FinalizeKubeletKubeconfig: %v", err)
	}
	if changed {
		t.Error("second call reported a change; want no-op")
	}
}

func TestFinalizeKubeletKubeconfigWithoutRotatedPEM(t *testing.T) {
	l := testLayout(t)
	writeEmbeddedKubeletConf(t, l)

	changed, err := kubeadm.FinalizeKubeletKubeconfig(l)
	if err != nil {
		t.Fatalf("FinalizeKubeletKubeconfig: %v", err)
	}
	if changed {
		t.Error("reported a change without kubelet-client-current.pem")
	}
	if info := currentAuthInfo(t, l); len(info.ClientCertificateData) == 0 {
		t.Error("kubelet.conf was rewritten although the rotated pem is absent")
	}
}
