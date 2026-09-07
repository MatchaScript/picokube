// Package layouttest provides a layout.Layout rooted at t.TempDir() for
// use from *_test.go files. Kept in a separate package from layout
// because importing testing in a non-test .go file would pull
// testing.init()'s flag.CommandLine registrations into the production
// picokube binary (visible as bogus -test.* flags on the CLI).
package layouttest

import (
	"path/filepath"
	"testing"

	"github.com/MatchaScript/picokube/internal/layout"
)

// New builds a layout.Layout whose every path lives under t.TempDir().
// Safe to call from t.Parallel() tests — each call returns an
// independent Layout value with no shared mutable state.
func New(t testing.TB) layout.Layout {
	t.Helper()
	root := t.TempDir()
	kdir := filepath.Join(root, "etc/kubernetes")
	pki := filepath.Join(kdir, "pki")
	nkvar := filepath.Join(root, "var/lib/picokube")
	stateDir := filepath.Join(nkvar, "state")
	backups := filepath.Join(nkvar, "backups")
	kubelet := filepath.Join(root, "var/lib/kubelet")
	configDir := filepath.Join(root, "etc/picokube")
	mfs := filepath.Join(kdir, "manifests")
	return layout.Layout{
		ConfigDir:             configDir,
		ConfigFile:            filepath.Join(configDir, "config.yaml"),
		PicoKubeVarDir:        nkvar,
		StateDir:              stateDir,
		LastBootFile:          filepath.Join(stateDir, "last-boot.json"),
		LastEventFile:         filepath.Join(stateDir, "last-event"),
		BackupsDir:            backups,
		RestoreMarker:         filepath.Join(backups, "restore"),
		KubernetesDir:         kdir,
		PKIDir:                pki,
		EtcdPKIDir:            filepath.Join(pki, "etcd"),
		ManifestsDir:          mfs,
		KubeAPIServerManifest: filepath.Join(mfs, "kube-apiserver.yaml"),
		AdminKubeconfig:       filepath.Join(kdir, "admin.conf"),
		KubeletKubeconfig:     filepath.Join(kdir, "kubelet.conf"),
		CMKubeconfig:          filepath.Join(kdir, "controller-manager.conf"),
		SchedKubeconfig:       filepath.Join(kdir, "scheduler.conf"),
		SuperAdminKubeconfig:  filepath.Join(kdir, "super-admin.conf"),
		KubeletDir:            kubelet,
		KubeletConfigFile:     filepath.Join(kubelet, "config.yaml"),
		KubeletFlagsEnvFile:   filepath.Join(kubelet, "kubeadm-flags.env"),
		EtcdDataDir:           filepath.Join(root, "var/lib/etcd"),
	}
}
