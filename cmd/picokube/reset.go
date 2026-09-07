package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/picokube/internal/teardown"
)

func newResetCmd(g *globalOpts) *cobra.Command {
	var confirm bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Tear down all picokube-managed state (matches `kubeadm reset --force`)",
		Long: "Stops kubelet, lazy-unmounts /var/lib/kubelet, removes every CRI " +
			"pod sandbox, and wipes /etc/kubernetes, /var/lib/etcd, " +
			"/var/lib/kubelet, /var/lib/picokube. Network state (CNI interfaces, " +
			"iptables, IPVS, nftables) is left untouched — clean it up manually " +
			"if you need a pristine slate. Intended for test beds or when " +
			"re-initialising from scratch.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !confirm {
				return errors.New("refusing to proceed without --yes (this is destructive)")
			}
			return teardown.Run(cmd.Context(), g.layout, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&confirm, "yes", false, "confirm the destructive operation")
	return cmd
}
