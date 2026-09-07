// Package version exposes build-time constants. Values are overridden via
// -ldflags "-X github.com/MatchaScript/picokube/internal/version.<Name>=<value>"
// during release builds.
package version

// KubernetesVersion is the single k8s minor this picokube build targets.
// picokube minor == kubelet minor is a hard constraint; the entire boot
// flow and config defaults are pinned to this value.
var KubernetesVersion = "v1.35.0"

// GitCommit is the commit hash of the picokube source tree at build time.
var GitCommit = "unknown"

// BuildDate is the RFC3339 build timestamp.
var BuildDate = "unknown"
