// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package nodecmd

import (
	"fmt"

	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/ux"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "(ALPHA Warning) List all clusters together with their nodes",
		Long: `(ALPHA Warning) This command is currently in experimental mode.

The node list command lists all clusters together with their nodes.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(0),
		RunE:         list,
	}

	return cmd
}

func list(_ *cobra.Command, _ []string) error {
	var err error
	clusterConfig := models.ClustersConfig{}
	if app.ClustersConfigExists() {
		clusterConfig, err = app.LoadClustersConfig()
		if err != nil {
			return err
		}
	}
	if len(clusterConfig.Clusters) == 0 {
		ux.Logger.PrintToUser("There are no clusters defined.")
	}
	for clusterName, clusterConf := range clusterConfig.Clusters {
		ux.Logger.PrintToUser(fmt.Sprintf("Cluster %q (%s)", clusterName, clusterConf.Network.String()))
		if err := checkCluster(clusterName); err != nil {
			return err
		}
		if err := setupAnsible(clusterName); err != nil {
			return err
		}
		for _, clusterNode := range clusterConf.Nodes {
			nodeID, err := getNodeID(app.GetNodeInstanceDirPath(clusterNode))
			if err != nil {
				return err
			}
			ux.Logger.PrintToUser(fmt.Sprintf("  Node %s (%s)", clusterNode, nodeID.String()))
		}
	}
	return nil
}
