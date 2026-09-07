//go:build e2e

package e2e

import (
	"github.com/MatchaScript/picokube/test/e2etest"
)

// Test14Reset_WipesEverything asserts `picokube reset --yes` removes
// every managed path and leaves kubelet.service inactive. Without
// kubelet stopped, static pods would restart with half-wiped state.
// Mirrors bash :test_normal_reset_wipes_everything.
func (s *PicokubeE2ESuite) Test14Reset_WipesEverything() {
	s.H.Picokube("reset", "--yes")

	for _, p := range []string{
		"/etc/kubernetes/manifests",
		"/var/lib/etcd",
		"/var/lib/picokube",
	} {
		e2etest.AssertFileAbsent(s.T(), p, "reset")
	}

	s.Require().False(s.H.SystemctlIsActive("kubelet.service"),
		"kubelet.service still active after reset")
}

// Test15Reset_RequiresYes asserts reset without --yes refuses,
// protecting against accidental wipe.
// Mirrors bash :test_abnormal_reset_requires_yes.
func (s *PicokubeE2ESuite) Test15Reset_RequiresYes() {
	s.H.PicokubeExpectFail("reset")
}

// Test16Reset_BootstrapAfterResetIsClean asserts reset truly cleared
// state.Exists() — a follow-up `init` (no --force) must succeed.
// Cleans up with a final reset --yes so any diagnostics dump from
// TearDownTest is from a known clean state.
// Mirrors bash :test_normal_bootstrap_after_reset_is_clean.
func (s *PicokubeE2ESuite) Test16Reset_BootstrapAfterResetIsClean() {
	s.H.Picokube("init")
	e2etest.AssertFilePresent(s.T(),
		"/etc/kubernetes/manifests/kube-apiserver.yaml", "post-reset init")
	s.H.Picokube("reset", "--yes")
}
