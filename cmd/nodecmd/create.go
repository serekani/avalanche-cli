// Copyright (C) 2022, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package nodecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	awsAPI "github.com/ava-labs/avalanche-cli/pkg/cloud/aws"

	"github.com/ava-labs/avalanche-cli/cmd/flags"
	"github.com/ava-labs/avalanche-cli/cmd/subnetcmd"
	"github.com/ava-labs/avalanche-cli/pkg/ansible"
	"github.com/ava-labs/avalanche-cli/pkg/binutils"
	"github.com/ava-labs/avalanche-cli/pkg/ssh"
	"github.com/ava-labs/avalanche-cli/pkg/utils"
	"github.com/ava-labs/avalanche-cli/pkg/vm"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/staking"
	"github.com/ava-labs/avalanchego/utils/logging"
	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"

	"github.com/ava-labs/avalanche-cli/pkg/constants"
	"github.com/ava-labs/avalanche-cli/pkg/models"

	"github.com/ava-labs/avalanche-cli/pkg/ux"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

const (
	addMonitoringFlag = "with-prometheus"
)

var (
	createOnFuji                          bool
	createDevnet                          bool
	createOnMainnet                       bool
	useAWS                                bool
	useGCP                                bool
	cmdLineRegion                         []string
	authorizeAccess                       bool
	numValidatorsNodes                    []int
	nodeType                              string
	existingSeparateInstance              string
	existingMonitoringInstance            string
	useLatestAvalanchegoReleaseVersion    bool
	useLatestAvalanchegoPreReleaseVersion bool
	useCustomAvalanchegoVersion           string
	useAvalanchegoVersionFromSubnet       string
	cmdLineGCPCredentialsPath             string
	cmdLineGCPProjectName                 string
	cmdLineAlternativeKeyPairName         string
	addMonitoring                         bool
	useSSHAgent                           bool
	sshIdentity                           string
	numAPINodes                           []int
	versionComments                       = map[string]string{
		"v1.11.0-fuji": " (recommended for fuji durango)",
	}
)

func newCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create [clusterName]",
		Short: "(ALPHA Warning) Create a new validator on cloud server",
		Long: `(ALPHA Warning) This command is currently in experimental mode. 

The node create command sets up a validator on a cloud server of your choice. 
The validator will be validating the Avalanche Primary Network and Subnet 
of your choice. By default, the command runs an interactive wizard. It 
walks you through all the steps you need to set up a validator.
Once this command is completed, you will have to wait for the validator
to finish bootstrapping on the primary network before running further
commands on it, e.g. validating a Subnet. You can check the bootstrapping
status by running avalanche node status 

The created node will be part of group of validators called <clusterName> 
and users can call node commands with <clusterName> so that the command
will apply to all nodes in the cluster`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE:         createNodes,
	}
	cmd.Flags().BoolVar(&useStaticIP, "use-static-ip", true, "attach static Public IP on cloud servers")
	cmd.Flags().BoolVar(&useAWS, "aws", false, "create node/s in AWS cloud")
	cmd.Flags().BoolVar(&useGCP, "gcp", false, "create node/s in GCP cloud")
	cmd.Flags().StringSliceVar(&cmdLineRegion, "region", []string{}, "create node(s) in given region(s). Use comma to separate multiple regions")
	cmd.Flags().BoolVar(&authorizeAccess, "authorize-access", false, "authorize CLI to create cloud resources")
	cmd.Flags().IntSliceVar(&numValidatorsNodes, "num-validators", []int{}, "number of nodes to create per region(s). Use comma to separate multiple numbers for each region in the same order as --region flag")
	cmd.Flags().StringVar(&nodeType, "node-type", "", "cloud instance type. Use 'default' to use recommended default instance type")
	cmd.Flags().BoolVar(&useLatestAvalanchegoReleaseVersion, "latest-avalanchego-version", false, "install latest avalanchego release version on node/s")
	cmd.Flags().BoolVar(&useLatestAvalanchegoPreReleaseVersion, "latest-avalanchego-pre-release-version", false, "install latest avalanchego pre-release version on node/s")
	cmd.Flags().StringVar(&useCustomAvalanchegoVersion, "custom-avalanchego-version", "", "install given avalanchego version on node/s")
	cmd.Flags().StringVar(&useAvalanchegoVersionFromSubnet, "avalanchego-version-from-subnet", "", "install latest avalanchego version, that is compatible with the given subnet, on node/s")
	cmd.Flags().StringVar(&cmdLineGCPCredentialsPath, "gcp-credentials", "", "use given GCP credentials")
	cmd.Flags().StringVar(&cmdLineGCPProjectName, "gcp-project", "", "use given GCP project")
	cmd.Flags().StringVar(&cmdLineAlternativeKeyPairName, "alternative-key-pair-name", "", "key pair name to use if default one generates conflicts")
	cmd.Flags().StringVar(&awsProfile, "aws-profile", constants.AWSDefaultCredential, "aws profile to use")
	cmd.Flags().BoolVar(&createOnFuji, "fuji", false, "create node/s in Fuji Network")
	cmd.Flags().BoolVar(&createDevnet, "devnet", false, "create node/s into a new Devnet")
	cmd.Flags().BoolVar(&useSSHAgent, "use-ssh-agent", false, "use ssh agent(ex: Yubikey) for ssh auth")
	cmd.Flags().StringVar(&sshIdentity, "ssh-agent-identity", "", "use given ssh identity(only for ssh agent). If not set, default will be used")
	cmd.Flags().BoolVar(&addMonitoring, addMonitoringFlag, false, "set up Prometheus monitoring for created nodes. This option creates a separate monitoring cloud instance and incures additional cost")
	cmd.Flags().IntSliceVar(&numAPINodes, "num-apis", []int{}, "number of API nodes(nodes without stake) to create in the new Devnet")
	return cmd
}

func preCreateChecks() error {
	if !flags.EnsureMutuallyExclusive([]bool{useLatestAvalanchegoReleaseVersion, useLatestAvalanchegoPreReleaseVersion, useAvalanchegoVersionFromSubnet != "", useCustomAvalanchegoVersion != ""}) {
		return fmt.Errorf("latest avalanchego released version, latest avalanchego pre-released version, custom avalanchego version and avalanchego version based on given subnet, are mutually exclusive options")
	}
	if useAWS && useGCP {
		return fmt.Errorf("could not use both AWS and GCP cloud options")
	}
	if !useAWS && awsProfile != constants.AWSDefaultCredential {
		return fmt.Errorf("could not use AWS profile for non AWS cloud option")
	}
	if len(utils.Unique(cmdLineRegion)) != len(numValidatorsNodes) {
		return fmt.Errorf("regions provided is not consistent with number of nodes provided. Please make sure list of regions is unique")
	}
	if len(numValidatorsNodes) > 0 {
		for _, num := range numValidatorsNodes {
			if num <= 0 {
				return fmt.Errorf("number of nodes per region must be greater than 0")
			}
		}
	}
	if sshIdentity != "" && !useSSHAgent {
		return fmt.Errorf("could not use ssh identity without using ssh agent")
	}
	if useSSHAgent && !utils.IsSSHAgentAvailable() {
		return fmt.Errorf("ssh agent is not available")
	}
	if len(numAPINodes) > 0 && !createDevnet {
		return fmt.Errorf("API nodes can only be created in Devnet")
	}
	if createDevnet && len(numAPINodes) != len(numValidatorsNodes) {
		return fmt.Errorf("API nodes and Validator nodes must be deployed to same number of regions")
	}
	if len(numAPINodes) > 0 {
		for _, num := range numValidatorsNodes {
			if num <= 0 {
				return fmt.Errorf("number of API nodes per region must be greater than 0")
			}
		}
	}
	return nil
}

func createNodes(cmd *cobra.Command, args []string) error {
	if err := preCreateChecks(); err != nil {
		return err
	}
	clusterName := args[0]
	network, err := subnetcmd.GetNetworkFromCmdLineFlags(
		false,
		createDevnet,
		createOnFuji,
		createOnMainnet,
		"",
		false,
		[]models.NetworkKind{models.Fuji, models.Devnet},
	)
	if err != nil {
		return err
	}

	createDevnet = network.Kind == models.Devnet // set createDevnet to true if network is devnet for further use
	avalancheGoVersion, err := getAvalancheGoVersion()
	if err != nil {
		return err
	}
	cloudService, err := setCloudService()
	if err != nil {
		return err
	}
	nodeType, err = setCloudInstanceType(cloudService)
	if err != nil {
		return err
	}

	if cloudService != constants.GCPCloudService && cmdLineGCPCredentialsPath != "" {
		return fmt.Errorf("set to use GCP credentials but cloud option is not GCP")
	}
	if cloudService != constants.GCPCloudService && cmdLineGCPProjectName != "" {
		return fmt.Errorf("set to use GCP project but cloud option is not GCP")
	}
	// for devnet add nonstake api nodes for each region with stake
	cloudConfigMap := models.CloudConfig{}
	publicIPMap := map[string]string{}
	apiNodeIPMap := map[string]string{}
	gcpProjectName := ""
	gcpCredentialFilepath := ""
	// set ssh-Key
	if useSSHAgent && sshIdentity == "" {
		sshIdentity, err = setSSHIdentity()
		if err != nil {
			return err
		}
	}
	monitoringHostRegion := ""
	monitoringNodeConfig := models.RegionConfig{}
	existingMonitoringInstance, err = getExistingMonitoringInstance(clusterName)
	if err != nil {
		return err
	}
	if utils.IsE2E() {
		usr, err := user.Current()
		if err != nil {
			return err
		}
		// override cloudConfig for E2E testing
		defaultAvalancheCLIPrefix := usr.Username + constants.AvalancheCLISuffix
		keyPairName := fmt.Sprintf("%s-keypair", defaultAvalancheCLIPrefix)
		certPath, err := app.GetSSHCertFilePath(keyPairName)
		if createDevnet {
			for i, num := range numAPINodes {
				numValidatorsNodes[i] += num
			}
		}
		dockerNumNodes := utils.Sum(numValidatorsNodes)
		var dockerNodesPublicIPs []string
		var monitoringHostIP string
		if addMonitoring {
			generatedPublicIPs := utils.GenerateDockerHostIPs(dockerNumNodes + 1)
			monitoringHostIP = generatedPublicIPs[len(generatedPublicIPs)-1]
			dockerNodesPublicIPs = generatedPublicIPs[:len(generatedPublicIPs)-1]
		} else {
			dockerNodesPublicIPs = utils.GenerateDockerHostIPs(dockerNumNodes)
		}
		dockerHostIDs := utils.GenerateDockerHostIDs(dockerNumNodes)
		if err != nil {
			return err
		}
		cloudConfigMap = models.CloudConfig{
			"docker": {
				InstanceIDs:       dockerHostIDs,
				PublicIPs:         dockerNodesPublicIPs,
				KeyPair:           keyPairName,
				SecurityGroup:     "docker",
				CertFilePath:      certPath,
				ImageID:           "docker",
				Prefix:            "docker",
				CertName:          "docker",
				SecurityGroupName: "docker",
				NumNodes:          dockerNumNodes,
				InstanceType:      "docker",
			},
		}
		currentRegionConfig := cloudConfigMap["docker"]
		for i, ip := range currentRegionConfig.PublicIPs {
			publicIPMap[dockerHostIDs[i]] = ip
		}
		apiNodeIDs := []string{}
		if len(numAPINodes) > 0 {
			_, apiNodeIDs = utils.SplitSliceAt(currentRegionConfig.InstanceIDs, len(currentRegionConfig.InstanceIDs)-numAPINodes[0])
		}
		currentRegionConfig.APIInstanceIDs = apiNodeIDs
		for _, node := range currentRegionConfig.APIInstanceIDs {
			apiNodeIPMap[node] = publicIPMap[node]
		}
		cloudConfigMap["docker"] = currentRegionConfig
		if addMonitoring {
			monitoringDockerHostID := utils.GenerateDockerHostIDs(1)
			dockerHostIDs = append(dockerHostIDs, monitoringDockerHostID[0])
			monitoringCloudConfig := models.CloudConfig{
				"monitoringDocker": {
					InstanceIDs:       monitoringDockerHostID,
					PublicIPs:         []string{monitoringHostIP},
					KeyPair:           keyPairName,
					SecurityGroup:     "docker",
					CertFilePath:      certPath,
					ImageID:           "docker",
					Prefix:            "docker",
					CertName:          "docker",
					SecurityGroupName: "docker",
					NumNodes:          1,
					InstanceType:      "docker",
				},
			}
			monitoringNodeConfig = monitoringCloudConfig["monitoringDocker"]
		}
		pubKeyString, err := os.ReadFile(fmt.Sprintf("%s.pub", certPath))
		if err != nil {
			return err
		}
		dockerComposeFile, err := utils.SaveDockerComposeFile(constants.E2EDockerComposeFile, len(dockerHostIDs), "focal", strings.TrimSuffix(string(pubKeyString), "\n"))
		if err != nil {
			return err
		}
		if err := utils.StartDockerCompose(dockerComposeFile); err != nil {
			return err
		}
	} else {
		if cloudService == constants.AWSCloudService {
			// Get AWS Credential, region and AMI
			if !(authorizeAccess || authorizedAccessFromSettings()) && (requestCloudAuth(constants.AWSCloudService) != nil) {
				return fmt.Errorf("cloud access is required")
			}
			ec2SvcMap, ami, numNodesMap, err := getAWSCloudConfig(awsProfile, false, nil)
			regions := maps.Keys(ec2SvcMap)
			if err != nil {
				return err
			}
			if existingMonitoringInstance == "" {
				monitoringHostRegion = regions[0]
			}
			if !cmd.Flags().Changed(addMonitoringFlag) {
				if addMonitoring, err = promptSetUpMonitoring(); err != nil {
					return err
				}
			}
			cloudConfigMap, err = createAWSInstances(ec2SvcMap, nodeType, numNodesMap, regions, ami, false)
			if err != nil {
				return err
			}
			monitoringEc2SvcMap := make(map[string]*awsAPI.AwsCloud)
			if addMonitoring && existingMonitoringInstance == "" {
				monitoringEc2SvcMap[monitoringHostRegion] = ec2SvcMap[monitoringHostRegion]
				monitoringCloudConfig, err := createAWSInstances(monitoringEc2SvcMap, nodeType, map[string]NumNodes{monitoringHostRegion: {1, 0}}, []string{monitoringHostRegion}, ami, true)
				if err != nil {
					return err
				}
				monitoringNodeConfig = monitoringCloudConfig[regions[0]]
			}
			if existingMonitoringInstance != "" {
				addMonitoring = true
				monitoringNodeConfig, monitoringHostRegion, err = getNodeCloudConfig(existingMonitoringInstance)
				if err != nil {
					return err
				}
				monitoringEc2SvcMap, err = getAWSMonitoringEC2Svc(awsProfile, monitoringHostRegion)
				if err != nil {
					return err
				}
			}
			if !useStaticIP && addMonitoring {
				monitoringPublicIPMap, err := monitoringEc2SvcMap[monitoringHostRegion].GetInstancePublicIPs(monitoringNodeConfig.InstanceIDs)
				if err != nil {
					return err
				}
				monitoringNodeConfig.PublicIPs = []string{monitoringPublicIPMap[monitoringNodeConfig.InstanceIDs[0]]}
			}
			for region, numNodes := range numNodesMap {
				currentRegionConfig := cloudConfigMap[region]
				if !useStaticIP {
					tmpIPMap, err := ec2SvcMap[region].GetInstancePublicIPs(currentRegionConfig.InstanceIDs)
					if err != nil {
						return err
					}
					for node, ip := range tmpIPMap {
						publicIPMap[node] = ip
					}
				} else {
					for i, node := range currentRegionConfig.InstanceIDs {
						publicIPMap[node] = currentRegionConfig.PublicIPs[i]
					}
				}
				// split publicIPMap to between stake and non-stake(api) nodes
				_, apiNodeIDs := utils.SplitSliceAt(currentRegionConfig.InstanceIDs, len(currentRegionConfig.InstanceIDs)-numNodes.numAPI)
				currentRegionConfig.APIInstanceIDs = apiNodeIDs
				for _, node := range currentRegionConfig.APIInstanceIDs {
					apiNodeIPMap[node] = publicIPMap[node]
				}
				cloudConfigMap[region] = currentRegionConfig
				if addMonitoring {
					if err = AddMonitoringSecurityGroupRule(ec2SvcMap, monitoringNodeConfig.PublicIPs[0], currentRegionConfig.SecurityGroup, region); err != nil {
						return err
					}
				}
			}
		} else {
			if !(authorizeAccess || authorizedAccessFromSettings()) && (requestCloudAuth(constants.GCPCloudService) != nil) {
				return fmt.Errorf("cloud access is required")
			}
			// Get GCP Credential, zone, Image ID, service account key file path, and GCP project name
			gcpClient, numNodesMap, imageID, credentialFilepath, projectName, err := getGCPConfig(false)
			if err != nil {
				return err
			}
			if existingMonitoringInstance == "" {
				monitoringHostRegion = maps.Keys(numNodesMap)[0]
			}
			if !cmd.Flags().Changed(addMonitoringFlag) {
				if addMonitoring, err = promptSetUpMonitoring(); err != nil {
					return err
				}
			}
			cloudConfigMap, err = createGCPInstance(gcpClient, nodeType, numNodesMap, imageID, clusterName, false)
			if err != nil {
				return err
			}
			if addMonitoring && existingMonitoringInstance == "" {
				monitoringCloudConfig, err := createGCPInstance(gcpClient, nodeType, map[string]NumNodes{monitoringHostRegion: {1, 0}}, imageID, clusterName, true)
				if err != nil {
					return err
				}
				monitoringNodeConfig = monitoringCloudConfig[monitoringHostRegion]
			}
			if existingMonitoringInstance != "" {
				addMonitoring = true
				monitoringNodeConfig, monitoringHostRegion, err = getNodeCloudConfig(existingMonitoringInstance)
				if err != nil {
					return err
				}
			}
			if !useStaticIP && addMonitoring {
				monitoringPublicIPMap, err := gcpClient.GetInstancePublicIPs(monitoringHostRegion, monitoringNodeConfig.InstanceIDs)
				if err != nil {
					return err
				}
				monitoringNodeConfig.PublicIPs = []string{monitoringPublicIPMap[monitoringNodeConfig.InstanceIDs[0]]}
			}
			for zone, numNodes := range numNodesMap {
				currentRegionConfig := cloudConfigMap[zone]
				if !useStaticIP {
					tmpIPMap, err := gcpClient.GetInstancePublicIPs(zone, currentRegionConfig.InstanceIDs)
					if err != nil {
						return err
					}
					for node, ip := range tmpIPMap {
						publicIPMap[node] = ip
					}
				} else {
					for i, node := range currentRegionConfig.InstanceIDs {
						publicIPMap[node] = currentRegionConfig.PublicIPs[i]
					}
				}
				// split publicIPMap to between stake and non-stake(api) nodes
				_, apiNodeIDs := utils.SplitSliceAt(currentRegionConfig.InstanceIDs, len(currentRegionConfig.InstanceIDs)-numNodes.numAPI)
				currentRegionConfig.APIInstanceIDs = apiNodeIDs
				for _, node := range currentRegionConfig.APIInstanceIDs {
					apiNodeIPMap[node] = publicIPMap[node]
				}
				cloudConfigMap[zone] = currentRegionConfig
				if addMonitoring {
					prefix, err := defaultAvalancheCLIPrefix("")
					if err != nil {
						return err
					}
					networkName := fmt.Sprintf("%s-network", prefix)
					firewallName := fmt.Sprintf("%s-%s-monitoring", networkName, strings.ReplaceAll(monitoringNodeConfig.PublicIPs[0], ".", ""))
					ports := []string{
						strconv.Itoa(constants.AvalanchegoMachineMetricsPort), strconv.Itoa(constants.AvalanchegoAPIPort),
						strconv.Itoa(constants.AvalanchegoMonitoringPort), strconv.Itoa(constants.AvalanchegoGrafanaPort),
					}
					if err = gcpClient.AddFirewall(
						monitoringNodeConfig.PublicIPs[0],
						networkName,
						projectName,
						firewallName,
						ports,
						true); err != nil {
						return err
					}
				}
			}
			gcpProjectName = projectName
			gcpCredentialFilepath = credentialFilepath
		}
	}

	if err = CreateClusterNodeConfig(
		network,
		cloudConfigMap,
		monitoringNodeConfig,
		monitoringHostRegion,
		clusterName,
		cloudService,
		addMonitoring,
	); err != nil {
		return err
	}
	if cloudService == constants.GCPCloudService {
		if err = updateClustersConfigGCPKeyFilepath(gcpProjectName, gcpCredentialFilepath); err != nil {
			return err
		}
	}

	inventoryPath := app.GetAnsibleInventoryDirPath(clusterName)
	if err = ansible.CreateAnsibleHostInventory(inventoryPath, "", cloudService, publicIPMap, cloudConfigMap); err != nil {
		return err
	}
	monitoringInventoryPath := ""
	var monitoringHosts []*models.Host
	if addMonitoring {
		monitoringInventoryPath = filepath.Join(app.GetAnsibleInventoryDirPath(clusterName), constants.MonitoringDir)
		if existingMonitoringInstance == "" {
			if err = ansible.CreateAnsibleHostInventory(monitoringInventoryPath, monitoringNodeConfig.CertFilePath, cloudService, map[string]string{monitoringNodeConfig.InstanceIDs[0]: monitoringNodeConfig.PublicIPs[0]}, nil); err != nil {
				return err
			}
		}
		monitoringHosts, err = ansible.GetInventoryFromAnsibleInventoryFile(monitoringInventoryPath)
		if err != nil {
			return err
		}
	}
	allHosts, err := ansible.GetInventoryFromAnsibleInventoryFile(inventoryPath)
	if err != nil {
		return err
	}
	hosts := utils.Filter(allHosts, func(h *models.Host) bool { return slices.Contains(cloudConfigMap.GetAllInstanceIDs(), h.GetCloudID()) })
	// waiting for all nodes to become accessible
	failedHosts := waitForHosts(hosts)
	if failedHosts.Len() > 0 {
		for _, result := range failedHosts.GetResults() {
			ux.Logger.PrintToUser("Instance %s failed to provision with error %s. Please check instance logs for more information", result.NodeID, result.Err)
		}
		return fmt.Errorf("failed to provision node(s) %s", failedHosts.GetNodeList())
	}
	ux.Logger.PrintToUser("Installing AvalancheGo and Avalanche-CLI and starting bootstrap process on the newly created Avalanche node(s)...")
	wg := sync.WaitGroup{}
	wgResults := models.NodeResults{}
	spinSession := ux.NewUserSpinner()
	for _, host := range hosts {
		wg.Add(1)
		go func(nodeResults *models.NodeResults, host *models.Host) {
			defer wg.Done()
			if err := host.Connect(0); err != nil {
				nodeResults.AddResult(host.NodeID, nil, err)
				return
			}
			if err := provideStakingCertAndKey(host); err != nil {
				nodeResults.AddResult(host.NodeID, nil, err)
				return
			}
			spinner := spinSession.SpinToUser(utils.ScriptLog(host.NodeID, "Setup Node"))
			if err := ssh.RunSSHSetupNode(host, app.Conf.GetConfigPath(), avalancheGoVersion, network.Kind == models.Devnet); err != nil {
				nodeResults.AddResult(host.NodeID, nil, err)
				ux.SpinFailWithError(spinner, "", err)
				return
			}
			ux.SpinComplete(spinner)
			if addMonitoring {
				spinner := spinSession.SpinToUser(utils.ScriptLog(host.NodeID, "Setup Machine Metrics"))
				if err := ssh.RunSSHSetupMachineMetrics(host); err != nil {
					nodeResults.AddResult(host.NodeID, nil, err)
					ux.SpinFailWithError(spinner, "", err)
					return
				}
				ux.SpinComplete(spinner)
			}
			spinner = spinSession.SpinToUser(utils.ScriptLog(host.NodeID, "Setup Build Env"))
			if err := ssh.RunSSHSetupBuildEnv(host); err != nil {
				nodeResults.AddResult(host.NodeID, nil, err)
				ux.SpinFailWithError(spinner, "", err)
				return
			}
			ux.SpinComplete(spinner)
			spinner = spinSession.SpinToUser(utils.ScriptLog(host.NodeID, "Setup Avalanche-CLI"))
			if err := ssh.RunSSHSetupCLIFromSource(host, constants.SetupCLIFromSourceBranch); err != nil {
				nodeResults.AddResult(host.NodeID, nil, err)
				ux.SpinFailWithError(spinner, "", err)
				return
			}
			ux.SpinComplete(spinner)
		}(&wgResults, host)
	}
	wg.Wait()
	ansibleHostIDs, err := utils.MapWithError(cloudConfigMap.GetAllInstanceIDs(), func(s string) (string, error) { return models.HostCloudIDToAnsibleID(cloudService, s) })
	if err != nil {
		return err
	}
	if addMonitoring {
		if len(monitoringHosts) != 1 {
			return fmt.Errorf("expected only one monitoring host, found %d", len(monitoringHosts))
		}
		monitoringHost := monitoringHosts[0]
		// remove monitoring host from created hosts list
		hosts = utils.Filter(hosts, func(h *models.Host) bool { return h.NodeID != monitoringHost.NodeID })
		avalancheGoPorts := []string{}
		machinePorts := []string{}
		inventoryHosts, err := ansible.GetInventoryFromAnsibleInventoryFile(app.GetAnsibleInventoryDirPath(clusterName))
		if err != nil {
			return err
		}
		for _, host := range inventoryHosts {
			avalancheGoPorts = append(avalancheGoPorts, fmt.Sprintf("'%s:%s'", host.IP, strconv.Itoa(constants.AvalanchegoAPIPort)))
			machinePorts = append(machinePorts, fmt.Sprintf("'%s:%s'", host.IP, strconv.Itoa(constants.AvalanchegoMachineMetricsPort)))
		}
		if existingMonitoringInstance != "" {
			spinner := spinSession.SpinToUser(utils.ScriptLog(monitoringHost.NodeID, "Update Monitoring Targets"))
			if err := ssh.RunSSHUpdatePrometheusConfig(monitoringHost, avalancheGoPorts, machinePorts); err != nil {
				ux.SpinFailWithError(spinner, "", err)
				return err
			}
			ux.SpinComplete(spinner)
		} else {
			spinner := spinSession.SpinToUser(utils.ScriptLog(monitoringHost.NodeID, "Setup Prometheus Monitoring and Grafana"))
			if err = app.SetupMonitoringEnv(); err != nil {
				ux.SpinFailWithError(spinner, "", err)
				return err
			}
			if err := ssh.RunSSHCopyMonitoringDashboards(monitoringHost, app.GetMonitoringDashboardDir()+"/"); err != nil {
				ux.SpinFailWithError(spinner, "", err)
				return err
			}
			if err := ssh.RunSSHSetupSeparateMonitoring(monitoringHost); err != nil {
				ux.SpinFailWithError(spinner, "", err)
				return err
			}
			if err := ssh.RunSSHUpdatePrometheusConfig(monitoringHost, avalancheGoPorts, machinePorts); err != nil {
				ux.SpinFailWithError(spinner, "", err)
				return err
			}
			ux.SpinComplete(spinner)
		}

		for _, ansibleNodeID := range ansibleHostIDs {
			if err = app.CreateAnsibleNodeConfigDir(ansibleNodeID); err != nil {
				return err
			}
		}
		// download node configs
		wg := sync.WaitGroup{}
		wgResults := models.NodeResults{}
		spinner := spinSession.SpinToUser("Configure Monitoring Agents")
		for _, host := range hosts {
			wg.Add(1)
			go func(nodeResults *models.NodeResults, host *models.Host) {
				defer wg.Done()
				nodeDirPath := app.GetNodeInstanceAvaGoConfigDirPath(host.NodeID)
				if err := ssh.RunSSHDownloadNodeMonitoringConfig(host, nodeDirPath); err != nil {
					nodeResults.AddResult(host.NodeID, nil, err)
					return
				}
				if err = addHTTPHostToConfigFile(app.GetNodeConfigJSONFile(host.NodeID)); err != nil {
					nodeResults.AddResult(host.NodeID, nil, err)
					return
				}
				if err := ssh.RunSSHUploadNodeMonitoringConfig(host, nodeDirPath); err != nil {
					nodeResults.AddResult(host.NodeID, nil, err)
					return
				}
				if err := ssh.RunSSHRestartNode(host); err != nil {
					nodeResults.AddResult(host.NodeID, nil, err)
					return
				}
				if err := os.RemoveAll(nodeDirPath); err != nil {
					return
				}
			}(&wgResults, host)
		}
		wg.Wait()
		for _, node := range hosts {
			if wgResults.HasNodeIDWithError(node.NodeID) {
				ux.SpinFailWithError(spinner, node.NodeID, wgResults.GetErrorHostMap()[node.NodeID])
				return fmt.Errorf("node %s failed to setup with error: %w", node.NodeID, wgResults.GetErrorHostMap()[node.NodeID])
			}
		}
		ux.SpinComplete(spinner)
	}
	spinSession.Stop()
	if network.Kind == models.Devnet {
		if err := setupDevnet(clusterName, hosts, apiNodeIPMap); err != nil {
			return err
		}
	}
	for _, node := range hosts {
		if wgResults.HasNodeIDWithError(node.NodeID) {
			ux.Logger.RedXToUser("Node %s is ERROR with error: %s", node.NodeID, wgResults.GetErrorHostMap()[node.NodeID])
		}
	}

	if wgResults.HasErrors() {
		return fmt.Errorf("failed to deploy node(s) %s", wgResults.GetErrorHostMap())
	} else {
		monitoringPublicIP := ""
		if addMonitoring {
			monitoringPublicIP = monitoringNodeConfig.PublicIPs[0]
		}
		printResults(cloudConfigMap, publicIPMap, monitoringPublicIP)
		ux.Logger.PrintToUser(logging.Green.Wrap("AvalancheGo and Avalanche-CLI installed and node(s) are bootstrapping!"))
	}
	return nil
}

func promptSetUpMonitoring() (bool, error) {
	monitoringInstance, err := app.Prompt.CaptureYesNo("Do you want to set up Prometheus monitoring? (This requires additional cloud instance and may incur additional cost)")
	if err != nil {
		return false, err
	}
	return monitoringInstance, nil
}

// CreateClusterNodeConfig creates node config and save it in .avalanche-cli/nodes/{instanceID}
// also creates cluster config in .avalanche-cli/nodes storing various key pair and security group info for all clusters
func CreateClusterNodeConfig(
	network models.Network,
	cloudConfigMap models.CloudConfig,
	monitorCloudConfig models.RegionConfig,
	monitoringHostRegion,
	clusterName,
	cloudService string,
	addMonitoring bool,
) error {
	for region, cloudConfig := range cloudConfigMap {
		for i := range cloudConfig.InstanceIDs {
			publicIP := ""
			if len(cloudConfig.PublicIPs) > 0 {
				publicIP = cloudConfig.PublicIPs[i]
			}
			nodeConfig := models.NodeConfig{
				NodeID:        cloudConfig.InstanceIDs[i],
				Region:        region,
				AMI:           cloudConfig.ImageID,
				KeyPair:       cloudConfig.KeyPair,
				CertPath:      cloudConfig.CertFilePath,
				SecurityGroup: cloudConfig.SecurityGroup,
				ElasticIP:     publicIP,
				CloudService:  cloudService,
				UseStaticIP:   useStaticIP,
				IsMonitor:     false,
			}
			err := app.CreateNodeCloudConfigFile(cloudConfig.InstanceIDs[i], &nodeConfig)
			if err != nil {
				return err
			}
			if err = addNodeToClustersConfig(network, cloudConfig.InstanceIDs[i], clusterName, slices.Contains(cloudConfig.APIInstanceIDs, cloudConfig.InstanceIDs[i]), false); err != nil {
				return err
			}
		}
		if addMonitoring {
			if err := saveExternalHostConfig(monitorCloudConfig, monitoringHostRegion, cloudService, clusterName); err != nil {
				return err
			}
		}
	}
	return nil
}

func saveExternalHostConfig(externalHostConfig models.RegionConfig, hostRegion, cloudService, clusterName string) error {
	nodeConfig := models.NodeConfig{
		NodeID:        externalHostConfig.InstanceIDs[0],
		Region:        hostRegion,
		AMI:           externalHostConfig.ImageID,
		KeyPair:       externalHostConfig.KeyPair,
		CertPath:      externalHostConfig.CertFilePath,
		SecurityGroup: externalHostConfig.SecurityGroup,
		ElasticIP:     externalHostConfig.PublicIPs[0],
		CloudService:  cloudService,
		UseStaticIP:   useStaticIP,
		IsMonitor:     true,
	}
	if err := app.CreateNodeCloudConfigFile(externalHostConfig.InstanceIDs[0], &nodeConfig); err != nil {
		return err
	}
	if err := addNodeToClustersConfig(models.UndefinedNetwork, externalHostConfig.InstanceIDs[0], clusterName, false, true); err != nil {
		return err
	}
	return updateKeyPairClustersConfig(nodeConfig)
}

func addHTTPHostToConfigFile(filePath string) error {
	jsonFile, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer jsonFile.Close()
	byteValue, _ := io.ReadAll(jsonFile)
	var result map[string]interface{}
	if err := json.Unmarshal(byteValue, &result); err != nil {
		return err
	}
	result["http-host"] = "0.0.0.0"
	byteValue, err = json.MarshalIndent(result, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, byteValue, constants.WriteReadReadPerms)
}

func getExistingMonitoringInstance(clusterName string) (string, error) {
	if app.ClustersConfigExists() {
		clustersConfig, err := app.LoadClustersConfig()
		if err != nil {
			return "", err
		}
		if _, ok := clustersConfig.Clusters[clusterName]; ok {
			if clustersConfig.Clusters[clusterName].MonitoringInstance != "" {
				return clustersConfig.Clusters[clusterName].MonitoringInstance, nil
			}
		}
	}
	return "", nil
}

func updateKeyPairClustersConfig(cloudConfig models.NodeConfig) error {
	clustersConfig := models.ClustersConfig{}
	var err error
	if app.ClustersConfigExists() {
		clustersConfig, err = app.LoadClustersConfig()
		if err != nil {
			return err
		}
	}
	if clustersConfig.KeyPair == nil {
		clustersConfig.KeyPair = make(map[string]string)
	}
	if _, ok := clustersConfig.KeyPair[cloudConfig.KeyPair]; !ok {
		clustersConfig.KeyPair[cloudConfig.KeyPair] = cloudConfig.CertPath
	}
	return app.WriteClustersConfigFile(&clustersConfig)
}

func getNodeCloudConfig(node string) (models.RegionConfig, string, error) {
	config, err := app.LoadClusterNodeConfig(node)
	if err != nil {
		return models.RegionConfig{}, "", err
	}
	elasticIP := []string{}
	if config.ElasticIP != "" {
		elasticIP = append(elasticIP, config.ElasticIP)
	}
	instanceIDs := []string{}
	instanceIDs = append(instanceIDs, config.NodeID)
	return models.RegionConfig{
		InstanceIDs:       instanceIDs,
		PublicIPs:         elasticIP,
		KeyPair:           config.KeyPair,
		SecurityGroupName: config.SecurityGroup,
		CertFilePath:      config.CertPath,
		ImageID:           config.AMI,
	}, config.Region, nil
}

func addNodeToClustersConfig(network models.Network, nodeID, clusterName string, isAPIInstance bool, isMonitoringInstance bool) error {
	clustersConfig := models.ClustersConfig{}
	if app.ClustersConfigExists() {
		var err error
		clustersConfig, err = app.LoadClustersConfig()
		if err != nil {
			return err
		}
	}
	if clustersConfig.Clusters == nil {
		clustersConfig.Clusters = make(map[string]models.ClusterConfig)
	}
	clusterConfig := clustersConfig.Clusters[clusterName]
	clusterConfig.Network = network
	if isMonitoringInstance {
		clusterConfig.MonitoringInstance = nodeID
	} else {
		clusterConfig.Nodes = append(clusterConfig.Nodes, nodeID)
	}
	if isAPIInstance {
		clusterConfig.APINodes = append(clusterConfig.APINodes, nodeID)
	}
	clustersConfig.Clusters[clusterName] = clusterConfig
	return app.WriteClustersConfigFile(&clustersConfig)
}

func getNodeID(nodeDir string) (ids.NodeID, error) {
	certBytes, err := os.ReadFile(filepath.Join(nodeDir, constants.StakerCertFileName))
	if err != nil {
		return ids.EmptyNodeID, err
	}
	keyBytes, err := os.ReadFile(filepath.Join(nodeDir, constants.StakerKeyFileName))
	if err != nil {
		return ids.EmptyNodeID, err
	}
	nodeID, err := utils.ToNodeID(certBytes, keyBytes)
	if err != nil {
		return ids.EmptyNodeID, err
	}
	return nodeID, nil
}

func generateNodeCertAndKeys(stakerCertFilePath, stakerKeyFilePath, blsKeyFilePath string) (ids.NodeID, error) {
	certBytes, keyBytes, err := staking.NewCertAndKeyBytes()
	if err != nil {
		return ids.EmptyNodeID, err
	}
	nodeID, err := utils.ToNodeID(certBytes, keyBytes)
	if err != nil {
		return ids.EmptyNodeID, err
	}
	if err := os.MkdirAll(filepath.Dir(stakerCertFilePath), constants.DefaultPerms755); err != nil {
		return ids.EmptyNodeID, err
	}
	if err := os.WriteFile(stakerCertFilePath, certBytes, constants.WriteReadUserOnlyPerms); err != nil {
		return ids.EmptyNodeID, err
	}
	if err := os.MkdirAll(filepath.Dir(stakerKeyFilePath), constants.DefaultPerms755); err != nil {
		return ids.EmptyNodeID, err
	}
	if err := os.WriteFile(stakerKeyFilePath, keyBytes, constants.WriteReadUserOnlyPerms); err != nil {
		return ids.EmptyNodeID, err
	}
	blsSignerKeyBytes, err := utils.NewBlsSecretKeyBytes()
	if err != nil {
		return ids.EmptyNodeID, err
	}
	if err := os.MkdirAll(filepath.Dir(blsKeyFilePath), constants.DefaultPerms755); err != nil {
		return ids.EmptyNodeID, err
	}
	if err := os.WriteFile(blsKeyFilePath, blsSignerKeyBytes, constants.WriteReadUserOnlyPerms); err != nil {
		return ids.EmptyNodeID, err
	}
	return nodeID, nil
}

func provideStakingCertAndKey(host *models.Host) error {
	instanceID := host.GetCloudID()
	keyPath := filepath.Join(app.GetNodesDir(), instanceID)
	nodeID, err := generateNodeCertAndKeys(
		filepath.Join(keyPath, constants.StakerCertFileName),
		filepath.Join(keyPath, constants.StakerKeyFileName),
		filepath.Join(keyPath, constants.BLSKeyFileName),
	)
	if err != nil {
		ux.Logger.PrintToUser("Failed to generate staking keys for host %s", instanceID)
		return err
	} else {
		ux.Logger.GreenCheckmarkToUser("Generated staking keys for host %s[%s] ", instanceID, nodeID.String())
	}
	return ssh.RunSSHUploadStakingFiles(host, keyPath)
}

// getAvalancheGoVersion asks users whether they want to install the newest Avalanche Go version
// or if they want to use the newest Avalanche Go Version that is still compatible with Subnet EVM
// version of their choice
func getAvalancheGoVersion() (string, error) {
	latestReleaseVersion, err := app.Downloader.GetLatestReleaseVersion(binutils.GetGithubLatestReleaseURL(
		constants.AvaLabsOrg,
		constants.AvalancheGoRepoName,
	))
	if err != nil {
		return "", err
	}
	latestPreReleaseVersion, err := app.Downloader.GetLatestPreReleaseVersion(
		constants.AvaLabsOrg,
		constants.AvalancheGoRepoName,
	)
	if err != nil {
		return "", err
	}

	if !useLatestAvalanchegoReleaseVersion && !useLatestAvalanchegoPreReleaseVersion && useCustomAvalanchegoVersion == "" && useAvalanchegoVersionFromSubnet == "" {
		err := promptAvalancheGoVersionChoice(latestReleaseVersion, latestPreReleaseVersion)
		if err != nil {
			return "", err
		}
	}

	var version string
	switch {
	case useLatestAvalanchegoReleaseVersion:
		version = latestReleaseVersion
	case useLatestAvalanchegoPreReleaseVersion:
		version = latestPreReleaseVersion
	case useCustomAvalanchegoVersion != "":
		if !semver.IsValid(useCustomAvalanchegoVersion) {
			return "", errors.New("custom avalanchego version must be a legal semantic version (ex: v1.1.1)")
		}
		version = useCustomAvalanchegoVersion
	case useAvalanchegoVersionFromSubnet != "":
		sc, err := app.LoadSidecar(useAvalanchegoVersionFromSubnet)
		if err != nil {
			return "", err
		}
		version, err = GetLatestAvagoVersionForRPC(sc.RPCVersion, latestPreReleaseVersion)
		if err != nil {
			return "", err
		}
	}
	return version, nil
}

func GetLatestAvagoVersionForRPC(configuredRPCVersion int, latestPreReleaseVersion string) (string, error) {
	desiredAvagoVersion, err := vm.GetLatestAvalancheGoByProtocolVersion(
		app, configuredRPCVersion, constants.AvalancheGoCompatibilityURL)
	if err == vm.ErrNoAvagoVersion {
		ux.Logger.PrintToUser("No Avago version found for subnet. Defaulting to latest pre-release version")
		return latestPreReleaseVersion, nil
	}
	if err != nil {
		return "", err
	}
	return desiredAvagoVersion, nil
}

// promptAvalancheGoVersionChoice sets flags for either using the latest Avalanche Go
// version or using the latest Avalanche Go version that is still compatible with the subnet that user
// wants the cloud server to track
func promptAvalancheGoVersionChoice(latestReleaseVersion string, latestPreReleaseVersion string) error {
	latestReleaseVersionOption := "Use latest Avalanche Go Release Version" + versionComments[latestReleaseVersion]
	latestPreReleaseVersionOption := "Use latest Avalanche Go Pre-release Version" + versionComments[latestPreReleaseVersion]
	subnetBasedVersionOption := "Use the deployed Subnet's VM version that the node will be validating"
	customOption := "Custom"

	txt := "What version of Avalanche Go would you like to install in the node?"
	versionOptions := []string{latestReleaseVersionOption, subnetBasedVersionOption, customOption}
	if latestPreReleaseVersion != latestReleaseVersion {
		versionOptions = []string{latestPreReleaseVersionOption, latestReleaseVersionOption, subnetBasedVersionOption, customOption}
	}
	versionOption, err := app.Prompt.CaptureList(txt, versionOptions)
	if err != nil {
		return err
	}

	switch versionOption {
	case latestReleaseVersionOption:
		useLatestAvalanchegoReleaseVersion = true
	case latestPreReleaseVersionOption:
		useLatestAvalanchegoPreReleaseVersion = true
	case customOption:
		useCustomAvalanchegoVersion, err = app.Prompt.CaptureVersion("Which version of AvalancheGo would you like to install? (Use format v1.10.13)")
		if err != nil {
			return err
		}
	default:
		for {
			useAvalanchegoVersionFromSubnet, err = app.Prompt.CaptureString("Which Subnet would you like to use to choose the avalanche go version?")
			if err != nil {
				return err
			}
			_, err = subnetcmd.ValidateSubnetNameAndGetChains([]string{useAvalanchegoVersionFromSubnet})
			if err == nil {
				break
			}
			ux.Logger.PrintToUser(fmt.Sprintf("no subnet named %s found", useAvalanchegoVersionFromSubnet))
		}
	}
	return nil
}

func setCloudService() (string, error) {
	if utils.IsE2E() && utils.E2EDocker() {
		return constants.E2EDocker, nil
	}
	if useAWS {
		return constants.AWSCloudService, nil
	}
	if useGCP {
		return constants.GCPCloudService, nil
	}
	txt := "Which cloud service would you like to launch your Avalanche Node(s) in?"
	cloudOptions := []string{constants.AWSCloudService, constants.GCPCloudService}
	chosenCloudService, err := app.Prompt.CaptureList(txt, cloudOptions)
	if err != nil {
		return "", err
	}
	return chosenCloudService, nil
}

func setCloudInstanceType(cloudService string) (string, error) {
	if utils.IsE2E() && utils.E2EDocker() {
		return constants.E2EDocker, nil
	}
	switch { // backwards compatibility
	case nodeType == constants.DefaultNodeType && cloudService == constants.AWSCloudService:
		nodeType = constants.AWSDefaultInstanceType
		return nodeType, nil
	case nodeType == constants.DefaultNodeType && cloudService == constants.GCPCloudService:
		nodeType = constants.GCPDefaultInstanceType
		return nodeType, nil
	}
	defaultNodeType := ""
	nodeTypeOption2 := ""
	nodeTypeOption3 := ""
	customNodeType := "Choose custom instance type"
	switch {
	case cloudService == constants.AWSCloudService:
		defaultNodeType = constants.AWSDefaultInstanceType
		nodeTypeOption2 = "t3a.2xlarge" // burst
		nodeTypeOption3 = "c5n.2xlarge"
	case cloudService == constants.GCPCloudService:
		defaultNodeType = constants.GCPDefaultInstanceType
		nodeTypeOption2 = "c3-highcpu-8"
		nodeTypeOption3 = "n2-standard-8"
	}
	if nodeType == "" {
		defaultStr := "[default] (recommended)"
		nodeTypeStr, err := app.Prompt.CaptureList(
			"Instance type to use",
			[]string{fmt.Sprintf("%s %s", defaultNodeType, defaultStr), nodeTypeOption2, nodeTypeOption3, customNodeType},
		)
		if err != nil {
			ux.Logger.PrintToUser("Failed to capture node type with error: %s", err.Error())
			return "", err
		}
		nodeTypeStr = strings.ReplaceAll(nodeTypeStr, defaultStr, "") // remove (default) if any
		if nodeTypeStr == customNodeType {
			nodeTypeStr, err = app.Prompt.CaptureString("What instance type would you like to use? Please refer to https://docs.avax.network/nodes/run/node-manually#hardware-and-os-requirements for minimum hardware requirements")
			if err != nil {
				ux.Logger.PrintToUser("Failed to capture custom node type with error: %s", err.Error())
				return "", err
			}
		}
		return strings.Trim(nodeTypeStr, " "), nil
	}
	return nodeType, nil
}

func printResults(cloudConfigMap models.CloudConfig, publicIPMap map[string]string, monitoringHostIP string) {
	ux.Logger.PrintToUser(" 											 ")
	ux.Logger.PrintLineSeparator()
	ux.Logger.PrintToUser("AVALANCHE NODE(S) SUCCESSFULLY SET UP!")
	ux.Logger.PrintLineSeparator()
	ux.Logger.PrintToUser("Please wait until the node(s) are successfully bootstrapped to run further commands on the node(s)")
	ux.Logger.PrintToUser("You can check status of the node(s) using %s command", logging.LightBlue.Wrap("avalanche node status"))
	ux.Logger.PrintToUser("Please use %s to ssh into the node(s). More details: %s", logging.LightBlue.Wrap("avalanche node ssh"), "https://docs.avax.network/tooling/cli-create-nodes/node-ssh")

	for region, cloudConfig := range cloudConfigMap {
		ux.Logger.PrintToUser(" ")
		ux.Logger.PrintToUser("Region: [%s] ", logging.LightBlue.Wrap(region))
		ux.Logger.PrintToUser(" ")
		if len(cloudConfig.APIInstanceIDs) > 0 {
			ux.Logger.PrintLineSeparator()
			ux.Logger.PrintToUser("API Endpoint(s) for region [%s]: ", logging.LightBlue.Wrap(region))
			for _, apiNode := range cloudConfig.APIInstanceIDs {
				ux.Logger.PrintToUser(logging.Green.Wrap(fmt.Sprintf("    http://%s:9650", publicIPMap[apiNode])))
			}
			ux.Logger.PrintLineSeparator()
			ux.Logger.PrintToUser("")
		}
		ux.Logger.PrintToUser("Don't delete or replace your ssh private key file at %s as you won't be able to access your cloud server without it", cloudConfig.CertFilePath)
		ux.Logger.PrintLineSeparator()
		for _, instanceID := range cloudConfig.InstanceIDs {
			nodeID, _ := getNodeID(app.GetNodeInstanceDirPath(instanceID))
			publicIP := ""
			publicIP = publicIPMap[instanceID]
			if slices.Contains(cloudConfig.APIInstanceIDs, instanceID) {
				ux.Logger.PrintToUser("%s [API] Cloud Instance ID: %s | Public IP:%s | %s", logging.Green.Wrap(">"), instanceID, publicIP, logging.Green.Wrap(nodeID.String()))
			} else {
				ux.Logger.PrintToUser("%s Cloud Instance ID: %s | Public IP:%s | %s ", logging.Green.Wrap(">"), instanceID, publicIP, logging.Green.Wrap(nodeID.String()))
			}
			ux.Logger.PrintToUser("staker.crt, staker.key and signer.key are stored at %s. Please keep them safe, as these files can be used to fully recreate your node.", app.GetNodeInstanceDirPath(instanceID))
			ux.Logger.PrintLineSeparator()
		}
	}
	if addMonitoring {
		getMonitoringHint(monitoringHostIP)
	}
}

// getMonitoringHint prints the monitoring help message including the link to the monitoring dashboard
func getMonitoringHint(monitoringHostIP string) {
	ux.Logger.PrintToUser("")
	ux.Logger.PrintLineSeparator()
	ux.Logger.PrintToUser("To view unified node %s, visit the following link in your browser: ", logging.LightBlue.Wrap("monitoring dashboard"))
	ux.Logger.PrintToUser(logging.Green.Wrap(fmt.Sprintf("http://%s:3000/dashboards", monitoringHostIP)))
	ux.Logger.PrintToUser("Log in with username: admin, password: admin")
	ux.Logger.PrintLineSeparator()
	ux.Logger.PrintToUser("")
}

// waitForHosts waits for all hosts to become available via SSH.
func waitForHosts(hosts []*models.Host) *models.NodeResults {
	hostErrors := models.NodeResults{}
	createdWaitGroup := sync.WaitGroup{}
	spinSession := ux.NewUserSpinner()
	for _, host := range hosts {
		createdWaitGroup.Add(1)
		go func(nodeResults *models.NodeResults, host *models.Host) {
			defer createdWaitGroup.Done()
			spinner := spinSession.SpinToUser(utils.ScriptLog(host.NodeID, "Waiting for instance response"))
			if err := host.WaitForSSHShell(constants.SSHServerStartTimeout); err != nil {
				nodeResults.AddResult(host.NodeID, nil, err)
				ux.SpinFailWithError(spinner, "", err)
				return
			}
			ux.SpinComplete(spinner)
		}(&hostErrors, host)
	}
	createdWaitGroup.Wait()
	spinSession.Stop()
	return &hostErrors
}

// requestCloudAuth makes sure user agree to
func requestCloudAuth(cloudName string) error {
	ux.Logger.PrintToUser("Do you authorize Avalanche-CLI to access your %s account?", cloudName)
	ux.Logger.PrintToUser("By clicking yes, you are authorizing Avalanche-CLI to:")
	ux.Logger.PrintToUser("- Create Cloud instance(s) and other components (such as elastic IPs)")
	ux.Logger.PrintToUser("- Start/Stop Cloud instance(s) and other components (such as elastic IPs) previously created by Avalanche-CLI")
	ux.Logger.PrintToUser("- Delete Cloud instance(s) and other components (such as elastic IPs) previously created by Avalanche-CLI")
	yes, err := app.Prompt.CaptureYesNo(fmt.Sprintf("I authorize Avalanche-CLI to access my %s account", cloudName))
	if err != nil {
		return err
	}
	if err := app.Conf.SetConfigValue(constants.ConfigAuthorizeCloudAccessKey, yes); err != nil {
		return err
	}
	if !yes {
		return fmt.Errorf("user did not give authorization to Avalanche-CLI to access %s account", cloudName)
	}
	return nil
}

func getSeparateHostNodeParam(cloudName string) (
	string,
	error,
) {
	type CloudPrompt struct {
		defaultLocations []string
		locationName     string
		locationsListURL string
	}

	supportedClouds := map[string]CloudPrompt{
		constants.AWSCloudService: {
			defaultLocations: []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2"},
			locationName:     "AWS Region",
			locationsListURL: "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/using-regions-availability-zones.html",
		},
		constants.GCPCloudService: {
			defaultLocations: []string{"us-east1", "us-central1", "us-west1"},
			locationName:     "Google Region",
			locationsListURL: "https://cloud.google.com/compute/docs/regions-zones/",
		},
	}

	if _, ok := supportedClouds[cloudName]; !ok {
		return "", fmt.Errorf("cloud %s is not supported", cloudName)
	}

	awsCustomRegion := fmt.Sprintf("Choose custom %s (list of %ss available at %s)", supportedClouds[cloudName].locationName, supportedClouds[cloudName].locationName, supportedClouds[cloudName].locationsListURL)
	userRegion, err := app.Prompt.CaptureList(
		fmt.Sprintf("Which %s do you want to set up your separate node in?", supportedClouds[cloudName].locationName),
		append(supportedClouds[cloudName].defaultLocations, awsCustomRegion),
	)
	if err != nil {
		return "", err
	}
	if userRegion == awsCustomRegion {
		userRegion, err = app.Prompt.CaptureString(fmt.Sprintf("Which %s do you want to set up your node in?", supportedClouds[cloudName].locationName))
		if err != nil {
			return "", err
		}
	}
	return userRegion, nil
}

func getRegionsNodeNum(cloudName string) (
	map[string]NumNodes,
	error,
) {
	type CloudPrompt struct {
		defaultLocations []string
		locationName     string
		locationsListURL string
	}

	supportedClouds := map[string]CloudPrompt{
		constants.AWSCloudService: {
			defaultLocations: []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2"},
			locationName:     "AWS Region",
			locationsListURL: "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/using-regions-availability-zones.html",
		},
		constants.GCPCloudService: {
			defaultLocations: []string{"us-east1", "us-central1", "us-west1"},
			locationName:     "Google Region",
			locationsListURL: "https://cloud.google.com/compute/docs/regions-zones/",
		},
	}

	if _, ok := supportedClouds[cloudName]; !ok {
		return nil, fmt.Errorf("cloud %s is not supported", cloudName)
	}

	nodes := map[string]NumNodes{}
	awsCustomRegion := fmt.Sprintf("Choose custom %s (list of %ss available at %s)", supportedClouds[cloudName].locationName, supportedClouds[cloudName].locationName, supportedClouds[cloudName].locationsListURL)
	additionalRegionPrompt := fmt.Sprintf("Would you like to add additional %s?", supportedClouds[cloudName].locationName)
	for {
		userRegion, err := app.Prompt.CaptureList(
			fmt.Sprintf("Which %s do you want to set up your node(s) in?", supportedClouds[cloudName].locationName),
			append(supportedClouds[cloudName].defaultLocations, awsCustomRegion),
		)
		if err != nil {
			return nil, err
		}
		if userRegion == awsCustomRegion {
			userRegion, err = app.Prompt.CaptureString(fmt.Sprintf("Which %s do you want to set up your node in?", supportedClouds[cloudName].locationName))
			if err != nil {
				return nil, err
			}
		}
		numAPINodes := uint32(0)
		numNodes, err := app.Prompt.CaptureUint32(fmt.Sprintf("How many nodes do you want to set up in %s %s?", userRegion, supportedClouds[cloudName].locationName))
		if err != nil {
			return nil, err
		}
		if createDevnet {
			numAPINodes, err = app.Prompt.CaptureUint32(fmt.Sprintf("How many API nodes (nodes without stake) do you want to set up in %s %s?", userRegion, supportedClouds[cloudName].locationName))
			if err != nil {
				return nil, err
			}
		}
		if numNodes > uint32(math.MaxInt32) || numAPINodes > uint32(math.MaxInt32) {
			return nil, fmt.Errorf("number of nodes exceeds the range of a signed 32-bit integer")
		}
		nodes[userRegion] = NumNodes{int(numNodes), int(numAPINodes)}

		currentInput := utils.Map(maps.Keys(nodes), func(region string) string { return fmt.Sprintf("[%s]:%d", region, nodes[region]) })
		ux.Logger.PrintToUser("Current selection: " + strings.Join(currentInput, " "))
		yes, err := app.Prompt.CaptureNoYes(additionalRegionPrompt)
		if err != nil {
			return nil, err
		}
		if !yes {
			return nodes, nil
		}
	}
}

func setSSHIdentity() (string, error) {
	const yubikeyMark = " [YubiKey] (recommended)"
	const yubikeyPattern = `cardno:(\d+(_\d+)*)`
	sshIdentities, err := utils.ListSSHAgentIdentities()
	if err != nil {
		return "", err
	}
	yubikeyRegexp := regexp.MustCompile(yubikeyPattern)
	sshIdentities = utils.Map(sshIdentities, func(id string) string {
		if len(yubikeyRegexp.FindStringSubmatch(id)) > 0 {
			return fmt.Sprintf("%s%s", id, yubikeyMark)
		}
		return id
	})
	sshIdentity, err := app.Prompt.CaptureList(
		"Which SSH identity do you want to use?", sshIdentities,
	)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(sshIdentity, yubikeyMark, ""), nil
}

// defaultAvalancheCLIPrefix returns the default Avalanche CLI prefix.
func defaultAvalancheCLIPrefix(region string) (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", err
	}
	if region == "" {
		return usr.Username + constants.AvalancheCLISuffix, nil
	}
	return usr.Username + "-" + region + constants.AvalancheCLISuffix, nil
}
