package main

import (
	"fmt"

	"github.com/spf13/cobra"
	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	v1alpha1 "github.com/MatchaScript/picokube/internal/apis/bootstrap/v1alpha1"
	"github.com/MatchaScript/picokube/internal/config"
	"github.com/MatchaScript/picokube/internal/version"
)

func newConfigCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate PicoKubeConfig",
	}
	cmd.AddCommand(newConfigPrintDefaultsCmd(), newConfigValidateCmd(g))
	return cmd
}

func newConfigPrintDefaultsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print-defaults",
		Short: "Print a PicoKubeConfig with all defaults applied",
		Long: "Prints a multi-document YAML stream containing the " +
			"PicoKubeConfig wrapper plus a defaulted kubeadm " +
			"InitConfiguration / ClusterConfiguration suitable as a " +
			"starting point for /etc/picokube/config.yaml. Edit the " +
			"emitted localAPIEndpoint.advertiseAddress to a routable IP " +
			"before feeding the file to `picokube init`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kubeadmCfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
			if err != nil {
				return fmt.Errorf("default kubeadm config: %w", err)
			}
			// DefaultedStaticInitConfiguration emits the kubeadm
			// placeholder version ("v1.0.0-placeholder-version") so it
			// works without internet access. Replace it with the image-
			// pinned version so the emitted template is round-tripable
			// through validate.
			kubeadmCfg.ClusterConfiguration.KubernetesVersion = version.KubernetesVersion
			data, err := config.Marshal(v1alpha1.NewDefault(), kubeadmCfg)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
}

func newConfigValidateCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Load the config file, apply defaults, and validate it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(g.configPath, g.layout)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config %s is valid (kubernetesVersion=%s, advertiseAddress=%s, nodeName=%s)\n",
				g.configPath, version.KubernetesVersion, cfg.LocalAPIEndpoint.AdvertiseAddress, cfg.NodeRegistration.Name)
			return nil
		},
	}
}
