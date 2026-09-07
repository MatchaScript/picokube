package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/picokube/internal/config"
	"github.com/MatchaScript/picokube/internal/kubeadm"
)

func newKubeconfigCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Manage kubeconfig files",
	}
	cmd.AddCommand(newKubeconfigSuperAdminCmd(g))
	return cmd
}

// newKubeconfigSuperAdminCmd recreates /etc/kubernetes/super-admin.conf,
// the system:masters-bound break-glass cred. picokube deletes it at the
// end of `picokube init` so it does not linger on a long-lived node;
// this command exists for the case where RBAC has been broken and the
// operator needs system:masters access to recover. Mirrors `kubeadm
// init phase kubeconfig super-admin`.
func newKubeconfigSuperAdminCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "super-admin",
		Short: "Regenerate /etc/kubernetes/super-admin.conf (break-glass cred)",
		Long: "Re-issues the system:masters-bound super-admin.conf kubeconfig " +
			"from the cluster CA. picokube normally deletes this file at " +
			"the end of `picokube init`; regenerate only when RBAC has " +
			"been broken and admin.conf can no longer reach the " +
			"apiserver. Delete the file again once recovery is complete.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(g.configPath, g.layout)
			if err != nil {
				return err
			}
			if err := kubeadm.WriteSuperAdminKubeconfig(cfg, g.layout); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", g.layout.SuperAdminKubeconfig)
			return nil
		},
	}
}
