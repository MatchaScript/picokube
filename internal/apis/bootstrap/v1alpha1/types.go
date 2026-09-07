// Package v1alpha1 defines the PicoKubeConfig wrapper document. picokube
// configuration files are multi-document YAML streams modelled on
// kubeadm's `--config`: a single PicoKubeConfig document plus the kubeadm
// InitConfiguration / ClusterConfiguration / KubeletConfiguration
// documents that picokube hands to kubeadm at runtime.
//
// The wrapper carries picokube-only metadata (apiVersion / kind for the
// loader to identify the file as ours) and a reserved Spec for future
// picokube-only knobs. Cluster, node-registration, and kubelet settings
// live exclusively in the kubeadm documents — picokube does not
// re-expose them.
//
// This package is intentionally small: it owns the wrapper type and the
// load-time gate (apiVersion / kind / JoinConfiguration absent /
// CertificatesDir at the pinned path). The kubeadm portion of the
// configuration is parsed by config.Load via kubeadm's own
// BytesToInitConfiguration and returned as the upstream internal type
// *kubeadmapi.InitConfiguration; downstream code talks to that type
// directly rather than to a picokube-shaped re-packaging.
package v1alpha1

const (
	GroupName  = "bootstrap.picokube.io"
	Version    = "v1alpha1"
	APIVersion = GroupName + "/" + Version
	Kind       = "PicoKubeConfig"
)

type TypeMeta struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

type ObjectMeta struct {
	Name string `json:"name,omitempty"`
}

// PicoKubeConfig is the wrapper document that identifies a YAML stream as
// a picokube configuration file.
type PicoKubeConfig struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta         `json:"metadata,omitempty"`
	Spec     PicoKubeConfigSpec `json:"spec,omitempty"`
}

// PicoKubeConfigSpec is reserved for picokube-only settings that have no
// kubeadm equivalent (e.g. a node-local etcd snapshot policy). Empty
// today — every current knob lives in the sibling kubeadm documents.
type PicoKubeConfigSpec struct{}
