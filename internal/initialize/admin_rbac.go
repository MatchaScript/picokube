package initialize

import (
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/kubeconfig"

	"github.com/MatchaScript/picokube/internal/layout"
)

// initAdminRBAC seeds the kubeadm:cluster-admins ClusterRoleBinding so
// that admin.conf (which is bound to that Group, not to the built-in
// system:masters) can authenticate. The kubeadm helper tries admin.conf
// first; on a fresh cluster that fails with Forbidden because the CRB
// does not yet exist, and it falls back to super-admin.conf to create
// it. The fallback is safe here precisely because Run wrote
// super-admin.conf moments earlier and will remove it moments later;
// outside this narrow window the cred does not exist.
//
// Lives in package initialize specifically so lifecycle.Boot cannot
// import it — circular-import-by-design. The cluster-admins CRB is a
// one-time seeding action; reconciling it every boot is what motivated
// this whole refactor.
func initAdminRBAC(l layout.Layout) (kubernetes.Interface, error) {
	client, err := kubeconfig.EnsureAdminClusterRoleBinding(l.KubernetesDir, nil)
	if err != nil {
		return nil, fmt.Errorf("seed admin cluster role binding: %w", err)
	}
	return client, nil
}

// removeSuperAdminKubeconfig deletes l.SuperAdminKubeconfig.
// Called immediately after initAdminRBAC so the system:masters-bound
// break-glass cred does not linger on a long-lived node. Idempotent: a
// missing file is not an error (a partially-completed prior init may
// have already removed it).
func removeSuperAdminKubeconfig(l layout.Layout) error {
	if err := os.Remove(l.SuperAdminKubeconfig); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove super-admin.conf: %w", err)
	}
	return nil
}
