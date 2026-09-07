//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MatchaScript/picokube/test/e2etest"
)

// Test07Boot_ServiceBootsToReady starts picokube.service and waits
// for the node and every kubeadm-style control-plane static pod to
// reach Ready. A Ready node alone is insufficient — controller-manager
// or scheduler crash loops would otherwise be invisible.
// Mirrors bash :test_normal_service_boots_to_ready.
func (s *PicokubeE2ESuite) Test07Boot_ServiceBootsToReady() {
	s.T().Log("starting picokube.service")
	s.H.SystemctlStart("picokube.service")
	s.Require().True(s.H.SystemctlIsActive("picokube.service"),
		"picokube.service inactive after start")

	s.H.WaitForNodeReady(5 * time.Minute)

	for _, role := range []string{"etcd", "kube-apiserver", "kube-controller-manager", "kube-scheduler"} {
		pod := role + "-" + s.H.NodeName()
		s.T().Logf("waiting for static pod %s Ready", pod)
		s.H.WaitForStaticPodReady(pod, "kube-system", 3*time.Minute)
	}
}

// Test08Boot_AdminRBACBound asserts admin.conf is fully authorised
// thanks to the kubeadm:cluster-admins ClusterRoleBinding seeded by
// EnsureAdminClusterRoleBinding.
// Mirrors bash :test_normal_admin_rbac_bound.
func (s *PicokubeE2ESuite) Test08Boot_AdminRBACBound() {
	s.H.Kubectl("auth", "can-i", "*", "*", "--all-namespaces")
	s.H.Kubectl("get", "clusterrolebinding", "kubeadm:cluster-admins")
}

// Test09Boot_NodeMarkedControlPlane verifies the markcontrolplane
// phase ran (the control-plane label is present) and that the
// nodeRegistration.taints=[] in the e2e config (written by the suite)
// flowed through to MarkControlPlane — the default control-plane
// taint must NOT be present.
// Mirrors bash :test_normal_node_marked_controlplane.
func (s *PicokubeE2ESuite) Test09Boot_NodeMarkedControlPlane() {
	raw := s.H.Kubectl("get", "node", s.H.NodeName(), "-o", "json")

	var node struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Taints []struct {
				Key string `json:"key"`
			} `json:"taints"`
		} `json:"spec"`
	}
	s.Require().NoError(json.Unmarshal([]byte(raw), &node), "parse node json")

	const cpLabel = "node-role.kubernetes.io/control-plane"
	_, hasLabel := node.Metadata.Labels[cpLabel]
	s.Require().True(hasLabel, "control-plane label missing")

	for _, t := range node.Spec.Taints {
		s.Require().NotEqualf(cpLabel, t.Key,
			"control-plane taint present despite nodeRegistration.taints=[] in config")
	}
}

// Test10Boot_AddonsDeployed asserts CoreDNS deployment and kube-proxy
// DaemonSet are present — these are the only addons picokube manages
// via EnsureAddons. (Readiness is checked in Test11 after CNI is up.)
// Mirrors bash :test_normal_addons_deployed.
func (s *PicokubeE2ESuite) Test10Boot_AddonsDeployed() {
	s.H.Kubectl("-n", "kube-system", "get", "deployment", "coredns")
	s.H.Kubectl("-n", "kube-system", "get", "daemonset", "kube-proxy")
	// Belt-and-braces: kubectl get prints headers even when the resource
	// is missing if -o name is used; confirm a non-empty resource name.
	out := s.H.Kubectl("-n", "kube-system", "get", "deployment", "coredns", "-o", "name")
	s.Require().Equal("deployment.apps/coredns", strings.TrimSpace(out))
}

// Test10Boot_KubeletCSRApprovedAndIssued asserts kubelet's certificate
// rotation completes end to end: kubelet asks for its own client
// certificate via the CSR API, and the csrapprover controller approves
// and issues it. Approval needs system:nodes bound to the
// selfnodeclient ClusterRole (nodebootstraptoken's
// AutoApproveNodeCertificateRotation); without that binding the CSR
// sits Pending forever and this is the only test that would notice.
//
// Sorts after Test10Boot_AddonsDeployed and before Test11 under
// testify's lexicographic dispatch, so it runs on the booted cluster.
func (s *PicokubeE2ESuite) Test10Boot_KubeletCSRApprovedAndIssued() {
	const signer = "kubernetes.io/kube-apiserver-client-kubelet"
	user := "system:node:" + s.H.NodeName()

	var name string
	err := e2etest.Retry(30, 2*time.Second, func() error {
		raw, err := s.H.KubectlRaw("get", "csr", "-o", "json")
		if err != nil {
			return err
		}
		var list struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					SignerName string `json:"signerName"`
					Username   string `json:"username"`
				} `json:"spec"`
				Status struct {
					// Non-empty certificate is what kubectl prints as
					// the "Issued" half of Approved,Issued.
					Certificate []byte `json:"certificate"`
					Conditions  []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return fmt.Errorf("parse csr list: %w", err)
		}
		for _, csr := range list.Items {
			if csr.Spec.SignerName != signer || csr.Spec.Username != user {
				continue
			}
			for _, c := range csr.Status.Conditions {
				if c.Type == "Approved" && c.Status == "True" && len(csr.Status.Certificate) > 0 {
					name = csr.Metadata.Name
					return nil
				}
			}
		}
		return fmt.Errorf("no Approved,Issued CSR for %s (signer %s) among %d CSRs",
			user, signer, len(list.Items))
	})
	s.Require().NoError(err, "kubelet client certificate never issued")
	s.T().Logf("kubelet rotation CSR %s is Approved,Issued", name)
}
