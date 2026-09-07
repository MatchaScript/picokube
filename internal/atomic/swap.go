// Package atomic provides filesystem primitives that picokube's
// state-mutating paths rely on for crash safety. SwapDir is the
// load-bearing one: it lets restoreDir and RegenerateCA replace a live
// data tree without ever leaving an in-progress half-state on disk.
//
// Linux-only by design. picokube ships on Fedora bootc; we make no
// effort to abstract over Windows/macOS.
package atomic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// SwapDir atomically swaps stage and live. After SwapDir returns
// successfully:
//   - live contains what was previously at stage
//   - stage contains what was previously at live (the caller is
//     expected to RemoveAll(stage) afterwards to drop the displaced data)
//   - parent(live) is fsync'd so the swap is durable across power loss
//
// Requires Linux kernel >= 3.15 and both paths on the same filesystem
// (RENAME_EXCHANGE constraints).
//
// If live does not exist, falls through to plain os.Rename(stage, live)
// — which is also atomic when the destination is absent.
func SwapDir(stage, live string) error {
	if _, err := os.Stat(live); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stage, live); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", stage, live, err)
		}
		return SyncParent(live)
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", live, err)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, live, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("renameat2 EXCHANGE %s <-> %s: %w", stage, live, err)
	}
	return SyncParent(live)
}

// SyncParent opens the parent directory of path, fsyncs it, and closes
// it. Required for rename durability on power loss.
func SyncParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open parent of %s: %w", path, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync parent of %s: %w", path, err)
	}
	return nil
}
