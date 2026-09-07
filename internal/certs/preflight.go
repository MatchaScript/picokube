package certs

import (
	"context"
	"fmt"
	"os"

	"github.com/MatchaScript/picokube/internal/layout"
)

// CAExistPreflighter gates boot on the presence of every CA file
// kubeadm.Ensure assumes. Without this check, a CA accidentally deleted
// post-init would let Ensure write a static-pod manifest that points at
// a non-existent ca.crt, deferring the failure to apiserver crash-loop
// where the root cause is harder to read.
//
// Only meaningful at boot. `picokube init` runs certs.Init seconds
// later, so CAs absent at init time are expected — the orchestrator
// omits this Preflighter from init's check slice.
type CAExistPreflighter struct {
	Layout layout.Layout
}

func (c CAExistPreflighter) Preflight(ctx context.Context) error {
	for _, ca := range AllCAs() {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := caCertPath(c.Layout, ca)
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("CA %s missing: %s: %w", ca, p, err)
		}
	}
	return nil
}
