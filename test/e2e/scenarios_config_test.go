//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/MatchaScript/picokube/test/e2etest"
)

// Test01Config_PrintDefaultsIsValid asserts `picokube config
// print-defaults` emits a multi-document starter template (PicoKubeConfig
// wrapper + kubeadm InitConfiguration) that passes `config validate`
// after the advertiseAddress placeholder is filled in and criSocket is
// pointed at this host's CRI runtime.
// Mirrors test/e2e/e2e.sh:test_normal_print_defaults_is_valid, adapted
// to the post-23b7b53 schema (the bespoke `selfSigned: true` field is
// gone; cert-handling moved out of the wrapper).
func (s *PicokubeE2ESuite) Test01Config_PrintDefaultsIsValid() {
	out, _ := s.H.Picokube("config", "print-defaults")
	e2etest.AssertContains(s.T(), out, "apiVersion: bootstrap.picokube.io/v1alpha1", "print-defaults")
	e2etest.AssertContains(s.T(), out, "kind: InitConfiguration", "print-defaults")

	rewritten := regexp.MustCompile(`(?m)^(\s+advertiseAddress:\s).*$`).
		ReplaceAllString(out, "${1}192.168.1.10")
	rewritten = regexp.MustCompile(`(?m)^(\s+criSocket:\s).*$`).
		ReplaceAllString(rewritten, "${1}unix:///var/run/crio/crio.sock")
	s.Require().Contains(rewritten, "advertiseAddress: 192.168.1.10",
		"sed substitution failed on advertiseAddress")
	s.Require().Contains(rewritten, "criSocket: unix:///var/run/crio/crio.sock",
		"sed substitution failed on criSocket")

	tmp := filepath.Join(s.T().TempDir(), "defaults.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(rewritten), 0o644))
	s.H.Picokube("--config", tmp, "config", "validate")
}

// Test02Config_InvalidConfigRejected asserts validate rejects a config
// with a bad advertiseAddress and names the offending field. The bash
// original also asserted the error mentioned criSocket; that no longer
// holds under the kubeadm multi-document schema (kubeadm's validator
// fails fast on the first bad field and a non-scheme CRI URI only
// trips a deprecation warning), so the assertion is narrowed to
// advertiseAddress — the consistently-rejected case.
// Mirrors bash :test_abnormal_invalid_config_rejected, adapted to the
// post-23b7b53 schema.
func (s *PicokubeE2ESuite) Test02Config_InvalidConfigRejected() {
	body := `apiVersion: bootstrap.picokube.io/v1alpha1
kind: PicoKubeConfig
metadata:
  name: bad
spec: {}
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: nope-not-an-ip
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
`
	tmp := filepath.Join(s.T().TempDir(), "bad.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(body), 0o644))

	stdout, stderr := s.H.PicokubeExpectFail("--config", tmp, "config", "validate")
	// kubeadm reports the field via its CLI-flag name
	// (apiserver-advertise-address); that substring keeps both "advertise"
	// and "address" visible in the error so the assertion still verifies
	// "the offending field is named".
	e2etest.AssertContains(s.T(), stdout+stderr, "advertise-address", "validate error")
}

// Test03Config_UnknownFieldRejected asserts an unknown field under
// spec is rejected (PicoKubeConfigSpec is empty {} now; any field is
// strict-unknown). Mirrors bash :test_abnormal_unknown_field_rejected.
func (s *PicokubeE2ESuite) Test03Config_UnknownFieldRejected() {
	body := `apiVersion: bootstrap.picokube.io/v1alpha1
kind: PicoKubeConfig
spec:
  typoField: oops
`
	tmp := filepath.Join(s.T().TempDir(), "typo.yaml")
	s.Require().NoError(os.WriteFile(tmp, []byte(body), 0o644))

	stdout, stderr := s.H.PicokubeExpectFail("--config", tmp, "config", "validate")
	e2etest.AssertContains(s.T(), stdout+stderr, "typoField", "unknown-field error")
}
