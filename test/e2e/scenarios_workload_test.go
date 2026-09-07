//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MatchaScript/picokube/test/e2etest"
)

// Test11Workload_CNIAndConnectivity is the only end-to-end data-plane
// test: install flannel, wait for CoreDNS to schedule (it could not
// before CNI), deploy nginx behind a ClusterIP Service, and curl the
// ClusterIP. Mirrors bash :test_normal_cni_and_workload_connectivity.
//
// The bash original used curl-via-bash; we use net/http to avoid the
// runtime curl dependency and to get typed timeouts. Service IP
// routing takes a few seconds to settle after the deployment becomes
// Available, so a Retry loop (10 × 3s) wraps the HTTP probe.
func (s *PicokubeE2ESuite) Test11Workload_CNIAndConnectivity() {
	s.T().Logf("installing flannel from %s", s.H.FlannelURL())
	s.H.Kubectl("apply", "-f", s.H.FlannelURL())
	s.H.WaitForPodsReady("kube-flannel", 5*time.Minute)
	s.H.WaitForPodsReady("kube-system", 5*time.Minute)

	s.T().Log("deploying nginx test workload")
	s.H.Kubectl("create", "deployment", "e2e-nginx", "--image=nginx:alpine")
	s.H.Kubectl("expose", "deployment", "e2e-nginx", "--port=80", "--target-port=80")
	s.H.Kubectl("wait", "--for=condition=Available",
		"deployment/e2e-nginx", "--timeout=3m")

	svcIP := strings.TrimSpace(s.H.Kubectl(
		"get", "svc", "e2e-nginx", "-o", "jsonpath={.spec.clusterIP}"))
	s.Require().NotEmpty(svcIP, "service has no ClusterIP")
	s.T().Logf("curling ClusterIP http://%s", svcIP)

	err := e2etest.Retry(10, 3*time.Second, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+svcIP, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if !bytes.Contains(body, []byte("Welcome to nginx")) {
			return fmt.Errorf("unexpected body: %s", body)
		}
		return nil
	})
	s.Require().NoError(err, "workload not reachable via ClusterIP")
}
