package certs

import (
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"

	"github.com/MatchaScript/picokube/internal/layout"
)

// Init performs first-time PKI provisioning for a fresh node. Every CA
// and leaf is self-signed by picokube; no operator-supplied material is
// consumed.
//
// Called exclusively from initialize.Run. boot.Run must NOT
// invoke Init: by then PKIDir is the build artifact and reseeding
// would silently change identities under a running cluster.
func Init(cfg *kubeadmapi.InitConfiguration, l layout.Layout) error {
	return NewSigner(cfg, l).EnsureAll()
}
