// Package certs owns the lifecycle of /etc/kubernetes/pki and the
// kubeconfig-embedded client certificates: initial issuance during
// `nanokube init` (always self-signed by nanokube) and per-boot
// rotation that handles both CA and leaf expiry.
//
// CA rotation is in scope: at boot, any CA whose remaining lifetime
// has fallen below the rotation threshold is regenerated, and every
// leaf signed by that CA is re-issued in the same pass. Leaves whose
// CAs are still healthy are renewed in place via the kubeadm renewal
// manager.
package certs

// LeafKind enumerates every renewable leaf certificate the kubeadm
// renewal manager recognises. Values match the renewal manager's
// internal name keys 1:1 (see kubeadm certs/renewal/manager.go), so a
// LeafKind can be passed straight to RenewUsingLocalCA.
//
// kubelet.conf is intentionally absent, matching kubeadm's own renewal
// manager. Once kubeadm.FinalizeKubeletKubeconfig has run, kubelet.conf
// holds no certificate of its own: it references
// /var/lib/kubelet/pki/kubelet-client-current.pem, the store kubelet
// renews through the CSR API. Renewing it from here would write an
// embedded certificate back over that reference.
type LeafKind string

const (
	LeafAPIServer              LeafKind = "apiserver"
	LeafAPIServerKubeletClient LeafKind = "apiserver-kubelet-client"
	LeafAPIServerEtcdClient    LeafKind = "apiserver-etcd-client"
	LeafFrontProxyClient       LeafKind = "front-proxy-client"
	LeafEtcdServer             LeafKind = "etcd-server"
	LeafEtcdPeer               LeafKind = "etcd-peer"
	LeafEtcdHealthcheckClient  LeafKind = "etcd-healthcheck-client"
	LeafAdminConf              LeafKind = "admin.conf"
	LeafSuperAdminConf         LeafKind = "super-admin.conf"
	LeafControllerManagerConf  LeafKind = "controller-manager.conf"
	LeafSchedulerConf          LeafKind = "scheduler.conf"
)

// AllLeaves returns every LeafKind value in a stable order. Used by
// CheckLeaves and by tests asserting the full leaf set.
func AllLeaves() []LeafKind {
	return []LeafKind{
		LeafAPIServer,
		LeafAPIServerKubeletClient,
		LeafAPIServerEtcdClient,
		LeafFrontProxyClient,
		LeafEtcdServer,
		LeafEtcdPeer,
		LeafEtcdHealthcheckClient,
		LeafAdminConf,
		LeafSuperAdminConf,
		LeafControllerManagerConf,
		LeafSchedulerConf,
	}
}

// CAKind enumerates the three certificate authorities nanokube owns.
// String values are PKI-dir-relative directories holding ca.{crt,key}.
type CAKind string

const (
	CACluster    CAKind = "ca"             // signs apiserver + kubeconfig client certs (admin/cm/scheduler)
	CAEtcd       CAKind = "etcd/ca"        // signs etcd/server, etcd/peer, etcd/healthcheck-client, apiserver-etcd-client
	CAFrontProxy CAKind = "front-proxy-ca" // signs front-proxy-client
)

// AllCAs returns every CAKind in a stable order.
func AllCAs() []CAKind {
	return []CAKind{CACluster, CAEtcd, CAFrontProxy}
}

// dependentLeaves returns the PKI leaves whose signature chains back to
// the given CA. Kubeconfig-embedded client certs are tracked separately
// via dependentKubeconfigs; the split lets RegenerateCA delete
// .crt+.key pairs for PKI leaves while deleting/recreating whole
// kubeconfig YAML files for the rest.
//
// kubelet.conf is reported by dependentKubeconfigs but is not a
// LeafKind: routine leaf-rotation flows ignore it (kubelet's CSR API
// owns that), while CA-rotation flows must rewrite it (CSR rotation
// can't run without a valid bootstrap cred).
func dependentLeaves(ca CAKind) []LeafKind {
	switch ca {
	case CACluster:
		return []LeafKind{
			LeafAPIServer,
			LeafAPIServerKubeletClient,
		}
	case CAEtcd:
		return []LeafKind{
			LeafEtcdServer,
			LeafEtcdPeer,
			LeafEtcdHealthcheckClient,
			LeafAPIServerEtcdClient,
		}
	case CAFrontProxy:
		return []LeafKind{LeafFrontProxyClient}
	}
	panic("unhandled CAKind: " + string(ca))
}

// dependentKubeconfigs returns the kubeconfig filenames whose embedded
// client cert is signed by the given CA. Only the cluster CA signs
// kubeconfigs; etcd and front-proxy CAs return nil.
func dependentKubeconfigs(ca CAKind) []string {
	if ca == CACluster {
		return []string{
			"admin.conf",
			"controller-manager.conf",
			"scheduler.conf",
			"kubelet.conf",
		}
	}
	return nil
}
