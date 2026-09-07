package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/picokube/internal/layout"
	"github.com/MatchaScript/picokube/internal/version"
)

type globalOpts struct {
	configPath string
	layout     layout.Layout
}

func newRootCmd() *cobra.Command {
	return newRootCmdWithOpts(&globalOpts{layout: layout.Default()})
}

// newRootCmdWithOpts builds the cobra tree from a caller-supplied opts.
// Tests use this to inject a layouttest layout without touching
// process-global variables.
func newRootCmdWithOpts(opts *globalOpts) *cobra.Command {
	if opts.configPath == "" {
		opts.configPath = opts.layout.ConfigFile
	}
	cmd := &cobra.Command{
		Use:           "picokube",
		Short:         "Minimal single-node Kubernetes for bootc-style edge deployments",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", opts.configPath, "path to PicoKubeConfig YAML")
	cmd.AddCommand(
		newInitCmd(opts),
		newResetCmd(opts),
		newBootCmd(opts), // hidden, invoked by picokube.service
		newHealthcheckCmd(opts),
		newConfigCmd(opts),
		newKubeconfigCmd(opts),
		newVersionCmd(),
	)
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build and target versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "picokube   kubernetes=%s commit=%s built=%s\n",
				version.KubernetesVersion, version.GitCommit, version.BuildDate)
			return nil
		},
	}
}
