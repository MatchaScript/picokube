package backup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MatchaScript/picokube/internal/layout"
)

const stagingSubdir = ".staging"

// Workspace is the host-side scratch area allocated by AllocateWorkspace
// before backup.Create runs. Create writes inside BackupTmp; on success
// it renames the directory to the final backup name, on failure the
// deferred cleanup wipes whatever remains.
type Workspace struct {
	BackupTmp string
}

// AllocateWorkspace stages the scratch directory backup.Create writes
// into. Callers MUST have invoked their preflight.Run() gate first; this
// function does not re-run any checks. useBackups=false short-circuits
// to a zero Workspace and a no-op cleanup so non-ostree call sites can
// keep the same defer cleanup() pattern.
//
// The returned cleanup is safe to call exactly once. It becomes a benign
// no-op once backup.Create renames the staging directory to its final
// name (rename destroys the staging path; RemoveAll on a missing path
// is benign).
func AllocateWorkspace(l layout.Layout, useBackups bool) (Workspace, func(), error) {
	noop := func() {}
	if !useBackups {
		return Workspace{}, noop, nil
	}
	if err := os.MkdirAll(l.BackupsDir, 0o700); err != nil {
		return Workspace{}, noop, fmt.Errorf("mkdir %s: %w", l.BackupsDir, err)
	}
	staging := filepath.Join(l.BackupsDir, stagingSubdir)
	if err := os.RemoveAll(staging); err != nil {
		return Workspace{}, noop, fmt.Errorf("clear stale staging %s: %w", staging, err)
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return Workspace{}, noop, fmt.Errorf("mkdir staging %s: %w", staging, err)
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	return Workspace{BackupTmp: staging}, cleanup, nil
}
