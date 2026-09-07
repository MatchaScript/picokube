package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatchaScript/picokube/internal/layout"
)

// runCmd executes the cobra root with args and returns stdout/stderr and
// the error returned from Execute(). The root is reconstructed per call
// so cobra's persistent flag state does not bleed across tests.
func runCmd(args ...string) (stdout, stderr string, err error) {
	return runCmdWithLayout(layout.Default(), args...)
}

// runCmdWithLayout is like runCmd but injects l into globalOpts so tests
// can redirect state paths without touching process-global variables.
func runCmdWithLayout(l layout.Layout, args ...string) (stdout, stderr string, err error) {
	var out, errBuf bytes.Buffer
	root := newRootCmdWithOpts(&globalOpts{configPath: l.ConfigFile, layout: l})
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestCLI_Version(t *testing.T) {
	out, _, err := runCmd("version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "kubernetes=") {
		t.Errorf("version output missing kubernetes=...: %q", out)
	}
}

// print-defaults output must itself be accepted by validate. This is the
// starter-template contract operators rely on.
func TestCLI_PrintDefaultsThenValidate(t *testing.T) {
	out, _, err := runCmd("config", "print-defaults")
	if err != nil {
		t.Fatalf("print-defaults: %v", err)
	}
	if !strings.Contains(out, "apiVersion: bootstrap.picokube.io/v1alpha1") {
		t.Errorf("print-defaults output missing apiVersion: %q", out)
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	vOut, _, err := runCmd("--config", cfg, "config", "validate")
	if err != nil {
		t.Fatalf("validate default: %v", err)
	}
	if !strings.Contains(vOut, "is valid") {
		t.Errorf("validate output = %q", vOut)
	}
}

func TestCLI_ValidateRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "bad.yaml")
	body := `apiVersion: bootstrap.picokube.io/v1alpha1
kind: PicoKubeConfig
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: "not-an-ip"
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
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCmd("--config", cfg, "config", "validate")
	if err == nil {
		t.Fatal("validate of bad config = nil; want error")
	}
	if !strings.Contains(err.Error(), "advertise") {
		t.Errorf("error = %v; want advertise-address mention", err)
	}
}

func TestCLI_ResetRequiresYes(t *testing.T) {
	_, _, err := runCmd("reset")
	if err == nil {
		t.Fatal("reset without --yes = nil; must refuse")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %v; want --yes mention", err)
	}
}

func TestCLI_InitRefusesWhenStateExists(t *testing.T) {
	// We can't run real init (needs kubeadm PKI write + hostname
	// permissions), but we can exercise the state.Exists() refusal path
	// by pre-populating the manifest first, then confirming init exits
	// before reaching Ensure.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := `apiVersion: bootstrap.picokube.io/v1alpha1
kind: PicoKubeConfig
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
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	l := seedStateAsExisting(t)

	_, _, err := runCmdWithLayout(l, "--config", cfg, "init")
	if err == nil {
		t.Fatal("init on existing state = nil; must refuse")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v; want 'already exists' message", err)
	}
	if !strings.Contains(err.Error(), "reset") {
		t.Errorf("error = %v; want 'reset' guidance", err)
	}
}
