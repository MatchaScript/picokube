// Package layout enumerates every filesystem location picokube reads or
// writes. A single value is built in cmd/picokube/root.go via Default()
// and passed down to every component, replacing the global var-based
// internal/paths package.
//
// Layout is a value type. Components extract the fields they need at
// the call site; the package exposes no subsystem accessors so the
// "certsLayout" / "kubeadm.DefaultLayout" duplication never returns.
//
// Tests build Layout via internal/layouttest.New(t), which roots every
// path under t.TempDir() with no global state — safe under t.Parallel().
package layout

// Layout holds every filesystem path picokube reads or writes. Production
// callers receive Default(); tests receive layouttest.New(t).
type Layout struct {
	// picokube own config and state
	ConfigDir      string // /etc/picokube
	ConfigFile     string // /etc/picokube/config.yaml
	PicoKubeVarDir string // /var/lib/picokube
	StateDir       string // /var/lib/picokube/state
	LastBootFile   string
	LastEventFile  string
	BackupsDir     string // /var/lib/picokube/backups
	RestoreMarker  string

	// Kubernetes (KubernetesDir is also the kubeconfig directory)
	KubernetesDir         string // /etc/kubernetes
	PKIDir                string // /etc/kubernetes/pki
	EtcdPKIDir            string // /etc/kubernetes/pki/etcd
	ManifestsDir          string // /etc/kubernetes/manifests
	KubeAPIServerManifest string
	AdminKubeconfig       string
	KubeletKubeconfig     string
	CMKubeconfig          string
	SchedKubeconfig       string
	SuperAdminKubeconfig  string

	// Kubelet
	KubeletDir          string // /var/lib/kubelet
	KubeletConfigFile   string
	KubeletFlagsEnvFile string

	// etcd
	EtcdDataDir string // /var/lib/etcd
}

// Default returns the production layout — the canonical kubeadm +
// picokube on-disk locations.
func Default() Layout {
	const (
		configDir = "/etc/picokube"
		pkVarDir  = "/var/lib/picokube"
		stateDir  = pkVarDir + "/state"
		backups   = pkVarDir + "/backups"

		kdir = "/etc/kubernetes"
		pki  = kdir + "/pki"
		etcd = pki + "/etcd"
		mfs  = kdir + "/manifests"

		kubelet = "/var/lib/kubelet"
	)
	return Layout{
		ConfigDir:      configDir,
		ConfigFile:     configDir + "/config.yaml",
		PicoKubeVarDir: pkVarDir,
		StateDir:       stateDir,
		LastBootFile:   stateDir + "/last-boot.json",
		LastEventFile:  stateDir + "/last-event",
		BackupsDir:     backups,
		RestoreMarker:  backups + "/restore",

		KubernetesDir:         kdir,
		PKIDir:                pki,
		EtcdPKIDir:            etcd,
		ManifestsDir:          mfs,
		KubeAPIServerManifest: mfs + "/kube-apiserver.yaml",
		AdminKubeconfig:       kdir + "/admin.conf",
		KubeletKubeconfig:     kdir + "/kubelet.conf",
		CMKubeconfig:          kdir + "/controller-manager.conf",
		SchedKubeconfig:       kdir + "/scheduler.conf",
		SuperAdminKubeconfig:  kdir + "/super-admin.conf",

		KubeletDir:          kubelet,
		KubeletConfigFile:   kubelet + "/config.yaml",
		KubeletFlagsEnvFile: kubelet + "/kubeadm-flags.env",

		EtcdDataDir: "/var/lib/etcd",
	}
}
