package main

import (
	"github.com/spf13/cobra"

	"github.com/MatchaScript/picokube/internal/boot"
	"github.com/MatchaScript/picokube/internal/config"
	"github.com/MatchaScript/picokube/internal/version"
)

// newBootCmd returns the hidden subcommand picokube.service invokes.
// Operators do not see this in help output; their supported verbs are
// `init` and `reset`. `boot` runs the boot reconciliation
// (restore-if-needed -> snapshot -> Ensure -> kubelet -> /readyz ->
// mark valid). On success the process stays alive in Active(running)
// state until SIGTERM/SIGINT (so the matching systemd unit keeps
// holding kubelet's ordering dep without an artificial RemainAfterExit
// flag). On failure it returns non-zero so greenboot can roll back.
func newBootCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:    "boot",
		Short:  "Internal: run the boot lifecycle (invoked by picokube.service)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(g.configPath, g.layout)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if err := boot.Run(ctx, cfg, g.layout, version.KubernetesVersion, cmd.ErrOrStderr()); err != nil {
				return err
			}
			// Healthy boot complete. Park here until systemd asks us to
			// stop — picokube.service is Type=notify with no
			// RemainAfterExit, so a clean exit would flip it to
			// inactive and break any unit ordered After=us.
			<-ctx.Done()
			return nil
		},
	}
}
