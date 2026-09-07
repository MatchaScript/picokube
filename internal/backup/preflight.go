package backup

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/MatchaScript/picokube/internal/layout"
)

// DefaultMinFreeBytes / DefaultHeadroomFactor are the defaults used by
// the per-boot preflight gate. Held here (not on SpacePreflighter) so
// orchestrators can grep for a single name when tuning.
const (
	DefaultMinFreeBytes   uint64  = 2 * 1024 * 1024 * 1024 // 2 GiB
	DefaultHeadroomFactor float64 = 1.5
)

// SpacePreflighter gates the backup directory on (a) write probe and
// (b) free space sufficient for the next snapshot. Constructed by the
// boot orchestrator when ostree-style backups are enabled.
type SpacePreflighter struct {
	Layout       layout.Layout
	MinFreeBytes uint64  // 0 -> DefaultMinFreeBytes
	Headroom     float64 // 0 -> DefaultHeadroomFactor
}

func (s SpacePreflighter) Preflight(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	minFree := s.MinFreeBytes
	if minFree == 0 {
		minFree = DefaultMinFreeBytes
	}
	head := s.Headroom
	if head == 0 {
		head = DefaultHeadroomFactor
	}
	if err := os.MkdirAll(s.Layout.BackupsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", s.Layout.BackupsDir, err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.Layout.BackupsDir, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", s.Layout.BackupsDir, err)
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if free < minFree {
		return fmt.Errorf("%s: free %d bytes below minimum %d", s.Layout.BackupsDir, free, minFree)
	}
	latest, err := LatestSize(s.Layout)
	if err != nil {
		return fmt.Errorf("read latest backup size: %w", err)
	}
	needed := uint64(float64(latest) * head)
	if free < needed {
		return fmt.Errorf("%s: free %d bytes below %d (%.1fx latest backup %d)",
			s.Layout.BackupsDir, free, needed, head, latest)
	}
	return nil
}
