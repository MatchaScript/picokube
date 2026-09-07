// Package kubeadm carries picokube's adaptation of kubeadm's static-pod
// and bootstrap phases. After config.Load parses the kubeadm
// InitConfiguration from the user's multi-document YAML, the helpers
// here drive kubeadm's own certs / controlplane / etcd / kubelet
// phases against that already-defaulted, already-validated object.
//
// Layout values flow from layout.Default() (built in cmd/picokube/root.go)
// down to each function call directly; this package owns no Layout type.
package kubeadm
