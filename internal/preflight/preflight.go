// Package preflight defines the Preflighter interface that each
// picokube subsystem implements for its cheap pre-condition checks,
// plus Run which executes a slice sequentially and stops on the first
// failure. Implementations live in the subsystem packages: see
// certs.CAExistPreflighter and backup.SpacePreflighter. Generic
// filesystem write checks live here as preflight.FSWritable.
//
// Orchestrators (initialize.Run, boot.Run) build the check slice at
// the call site, choosing which checks apply to that flow.
package preflight

import (
	"context"
	"os"
)

// Preflighter is a single readonly-or-near-readonly pre-condition gate.
// Implementations MUST return promptly on ctx cancellation and MUST NOT
// produce on-disk side effects that survive Preflight returning (a write
// probe that unlinks its own temp file is allowed).
type Preflighter interface {
	Preflight(ctx context.Context) error
}

// Run executes checks sequentially, stopping at the first error.
// Sequential (not parallel) because most checks touch the same
// filesystem; concurrent writeProbe / statfs calls would inflate false
// positives without buying any latency that matters for boot.
func Run(ctx context.Context, checks ...Preflighter) error {
	for _, c := range checks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.Preflight(ctx); err != nil {
			return err
		}
	}
	return nil
}

// writeProbe creates a temp file in dir and removes it. Surfaces
// permission, EROFS, and missing-parent failures without depending on
// any production write reaching them first. Used by FSWritable.
func writeProbe(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".preflight-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
