package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MatchaScript/picokube/internal/config"
	"github.com/MatchaScript/picokube/internal/initialize"
	"github.com/MatchaScript/picokube/internal/state"
	"github.com/MatchaScript/picokube/internal/version"
)

// newInitCmd is the one-time initialisation verb operators run on a
// fresh node. It mirrors `kubeadm init`'s scope: render PKI / kubeconfigs
// / static pod manifests, start kubelet, wait for the apiserver, seed
// the cluster-admins CRB, mark the node, install addons. On success the
// cluster is healthy on this host and the operator's next step is
// `systemctl enable picokube.service` so future reboots run under
// supervisor control. Refuses to run on a node that already has picokube
// state; operators must `picokube reset --yes` first to start over.
func newInitCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialise a fresh node (run once per install)",
		Long: "Mirrors `kubeadm init`'s scope: writes PKI, kubeconfigs, " +
			"static pod manifests, and kubelet config; starts kubelet; " +
			"waits for the apiserver; seeds the cluster-admins " +
			"ClusterRoleBinding; marks the control-plane node; installs " +
			"addons. After this completes, enable picokube.service so " +
			"subsequent boots reconcile automatically. Refuses to run if " +
			"picokube state already exists on this node; run " +
			"`picokube reset --yes` first to re-init.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			existed, err := state.Exists(g.layout)
			if err != nil {
				return err
			}
			if existed {
				return errors.New("picokube state already exists; run `picokube reset --yes` first to re-initialise")
			}
			cfg, err := config.Load(g.configPath, g.layout)
			if err != nil {
				return err
			}
			return initialize.Run(cmd.Context(), cfg, g.layout, version.KubernetesVersion, cmd.OutOrStdout())
		},
	}
}

// defaultNodeName matches kubeadm/kubelet: lowercased OS hostname.
func defaultNodeName() (string, error) {
	h, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("get hostname: %w", err)
	}
	return strings.ToLower(h), nil
}
