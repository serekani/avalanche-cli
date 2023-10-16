package ssh

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"text/template"
	"time"

	"github.com/ava-labs/avalanche-cli/pkg/constants"
	"github.com/ava-labs/avalanche-cli/pkg/models"
	"github.com/ava-labs/avalanche-cli/pkg/utils"
)

type scriptInputs struct {
	avalancheGoVersion   string
	subnetExportFileName string
	subnetName           string
	goVersion            string
	cliBranch            string
}

//go:embed shell/*
var script embed.FS

// RunSSHSetupNode runs provided script path over ssh.
// This script can be template as it will be rendered using scriptInputs vars
func RunOverSSH(id string, host models.Host, scriptPath string, templateVars scriptInputs) error {
	shellScript, err := script.ReadFile(scriptPath)
	if err != nil {
		return err
	}

	var script bytes.Buffer
	t, err := template.New(id).Parse(string(shellScript))
	if err != nil {
		return err
	}
	err = t.Execute(&script, templateVars)
	if err != nil {
		return err
	}
	cmd, err := host.Command(script.String(), nil, context.Background(), time.Second*300)
	if err != nil {
		return err
	}

	stdoutBuffer, stderrBuffer := utils.SetupRealtimeCLISSHOutput(cmd, true, true)
	// execute commands in script
	cmdErr := cmd.Run()
	if err := utils.DisplayErrMsg(stdoutBuffer); err != nil {
		return err
	}
	if err := utils.DisplayErrMsg(stderrBuffer); err != nil {
		return err
	}
	return cmdErr
}

func PostOverSSH(host models.Host, path string, requestBody string) ([]byte, error) {
	if path == "" {
		path = "/ext/info"
	}
	host.Forward()
	requestHeaders := fmt.Sprintf("POST %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Content-Length: %d\r\n"+
		"Content-Type: application/json\r\n\r\n", path, "127.0.0.1", len(requestBody))
	httpRequest := requestHeaders + requestBody
	_, err := host.TCPProxy.Write([]byte(httpRequest))
	if err != nil {
		return nil, err
	}
	// Read and print the server's response
	response := make([]byte, 10240)
	responseLength, err := host.TCPProxy.Read(response)
	if err != nil {
		return nil, err
	}
	return response[0 : responseLength-1], nil
}

// RunSSHSetupNode runs script to setup node
func RunSSHSetupNode(host models.Host, configPath, avalancheGoVersion string) error {
	//name: setup node
	if err := RunOverSSH("SetupNode", host, "ssh/shell/setupNode.sh", scriptInputs{avalancheGoVersion: avalancheGoVersion}); err != nil {
		return err
	}

	//name: copy metrics config to cloud server
	if err := host.Upload(configPath, fmt.Sprintf("/home/ubuntu/.avalanche-cli/%s", filepath.Base(configPath))); err != nil {
		return err
	}
	return nil
}

func RunSSHCopyStakingFiles(host models.Host, configPath, nodeInstanceDirPath string) error {
	//name: copy staker.crt to local machine
	if err := host.Download("/home/ubuntu/.avalanchego/staking/staker.crt", fmt.Sprintf("%s/staker.crt", nodeInstanceDirPath)); err != nil {
		return err
	}
	//name: copy staker.key to local machine
	if err := host.Download("/home/ubuntu/.avalanchego/staking/staker.key", fmt.Sprintf("%s/staker.key", nodeInstanceDirPath)); err != nil {
		return err
	}
	//name: copy signer.key to local machine
	if err := host.Download("/home/ubuntu/.avalanchego/staking/signer.key", fmt.Sprintf("%s/signer.key", nodeInstanceDirPath)); err != nil {
		return err
	}
	return nil
}

// RunSSHExportSubnet exports deployed Subnet from local machine to cloud server
func RunSSHExportSubnet(host models.Host, exportPath, cloudServerSubnetPath string) error {
	//name: copy exported subnet VM spec to cloud server
	return host.Upload(exportPath, cloudServerSubnetPath)
}

// RunSSHExportSubnet exports deployed Subnet from local machine to cloud server
// targets a specific host ansibleHostID in ansible inventory file
func RunSSHTrackSubnet(host models.Host, subnetName, importPath string) error {
	return RunOverSSH("TrackSubnet", host, "ssh/shell/trackSubnet.sh", scriptInputs{subnetName: subnetName, subnetExportFileName: importPath})
}

// RunSSHUpdateSubnet runs avalanche subnet join <subnetName> in cloud server using update subnet info
func RunSSHUpdateSubnet(host models.Host, subnetName, importPath string) error {
	return RunOverSSH("TrackSubnet", host, "ssh/shell/updateSubnet.sh", scriptInputs{subnetName: subnetName, subnetExportFileName: importPath})
}

// RunSSHSetupBuildEnv installs gcc, golang, rust and etc
func RunSSHSetupBuildEnv(host models.Host) error {
	return RunOverSSH("setupBuildEnv", host, "ssh/shell/setupBuildEnv.sh", scriptInputs{goVersion: constants.BuildEnvGolangVersion})
}

// RunSSHSetupCLIFromSource installs any CLI branch from source
func RunSSHSetupCLIFromSource(host models.Host, cliBranch string) error {
	return RunOverSSH("setupCLIFromSource", host, "ssh/shell/setupCLIFromSource.sh", scriptInputs{cliBranch: cliBranch})
}

// RunSSHCheckAvalancheGoVersion checks node avalanchego version
func RunSSHCheckAvalancheGoVersion(host models.Host) ([]byte, error) {
	// Craft and send the HTTP POST request
	requestBody := "{\"jsonrpc\":\"2.0\", \"id\":1,\"method\" :\"info.getNodeVersion\"}"
	return PostOverSSH(host, "", requestBody)
}

// RunSSHCheckBootstrapped checks if node is bootstrapped to primary network
func RunSSHCheckBootstrapped(host models.Host) ([]byte, error) {
	// Craft and send the HTTP POST request
	requestBody := "{\"jsonrpc\":\"2.0\", \"id\":1,\"method\" :\"info.isBootstrapped\", \"params\": {\"chain\":\"X\"}}"
	return PostOverSSH(host, "", requestBody)
}

// RunSSHGetNodeID reads nodeID from avalanchego
func RunSSHGetNodeID(host models.Host) ([]byte, error) {
	// Craft and send the HTTP POST request
	requestBody := "{\"jsonrpc\":\"2.0\", \"id\":1,\"method\" :\"info.getNodeID\"}"
	return PostOverSSH(host, "", requestBody)
}

// SubnetSyncStatus checks if node is synced to subnet
func SubnetSyncStatus(host models.Host, blockchainID string) ([]byte, error) {
	// Craft and send the HTTP POST request
	requestBody := fmt.Sprintf("{\"jsonrpc\":\"2.0\", \"id\":1,\"method\" :\"platform.getBlockchainStatus\", \"params\": {\"blockchainID\":\"%s\"}}", blockchainID)
	return PostOverSSH(host, "/ext/bc/P", requestBody)
}