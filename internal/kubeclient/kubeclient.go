// Package kubeclient builds a typed Kubernetes clientset against a
// picokube-managed control plane. Readiness probes (apiserver /readyz,
// node + control-plane static pod waits) live in package healthcheck;
// keeping kubeclient focused on configuration loading lets the same
// probe implementation back boot, init, and the `picokube healthcheck`
// CLI without one wrapping the other.
package kubeclient

import (
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/MatchaScript/picokube/internal/version"
)

// requestTimeout caps per-request blocking on the apiserver. 10s matches
// kubeadm's clientcmd.ConfigOverrides{Timeout: "10s"} default (see
// kubeadm app/util/kubeconfig/kubeconfig.go). Long-lived watches must
// not inherit this — clone the *rest.Config and clear Timeout if you
// need one. Today no picokube caller does (healthcheck uses point
// Get / PollUntilContextTimeout requests, not watches).
const requestTimeout = 10 * time.Second

// LoadAdmin builds a typed clientset from the kubeconfig at path
// (usually /etc/kubernetes/admin.conf).
func LoadAdmin(path string) (kubernetes.Interface, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("build rest config from %s: %w", path, err)
	}
	// CR14: a missing Timeout lets a stalled TLS handshake hang the
	// caller until ctx expires (often controlPlaneTimeout = 3 minutes).
	// Cap per-request blocking so transient apiserver weirdness fails
	// fast instead of consuming greenboot's whole retry budget. The
	// UserAgent makes apiserver audit logs trivially attributable.
	restCfg.Timeout = requestTimeout
	restCfg.UserAgent = "picokube/" + version.KubernetesVersion

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	return clientset, nil
}
