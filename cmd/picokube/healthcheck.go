package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/picokube/internal/healthcheck"
	"github.com/MatchaScript/picokube/internal/kubeclient"
)

// newHealthcheckCmd separates "is the cluster healthy?" from "did the
// picokube binary exit cleanly?" so greenboot's required.d hook can
// judge boot success against the actual control plane rather than the
// service exit code. Mirrors microshift's `microshift healthcheck`
// (reference/microshift/packaging/greenboot/microshift-running-check.sh).
//
// Probes (waits in parallel up to --timeout):
//
//   - apiserver /readyz returns 200
//   - node <hostname> reports Ready=True
//   - kube-apiserver / controller-manager / scheduler static pods Ready
//
// Exits 0 when every probe passes inside the budget; non-zero otherwise.
// Greenboot's boot_counter retries this several times before giving up
// and tripping the rollback path, so a brief startup race is tolerated.
func newHealthcheckCmd(g *globalOpts) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe the local control plane and report whether it is healthy",
		Long: "Single-shot readiness probe used by greenboot's required.d " +
			"to decide whether this boot was successful. Returns once the " +
			"apiserver responds to /readyz and the node + three " +
			"control-plane static pods all report Ready=True, or fails " +
			"after --timeout. Safe to run by hand for diagnostics.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			nodeName, err := defaultNodeName()
			if err != nil {
				return err
			}
			client, err := kubeclient.LoadAdmin(g.layout.AdminKubeconfig)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			if err := healthcheck.WaitForAPIServer(ctx, client); err != nil {
				return err
			}
			if err := healthcheck.WaitForControlPlane(ctx, client, nodeName); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "healthy")
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute,
		"max time to wait for the control plane to report healthy")
	return cmd
}
