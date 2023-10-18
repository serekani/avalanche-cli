// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package models

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/ava-labs/avalanche-cli/pkg/constants"
	"github.com/melbahja/goph"
	"golang.org/x/crypto/ssh"
)

type Host struct {
	NodeID            string
	IP                string
	SSHUser           string
	SSHPrivateKeyPath string
	SSHCommonArgs     string
	TCPProxy          *bytes.Buffer
}

const (
	shell     = "/bin/bash"
	localhost = "127.0.0.1"
)

// GetNodeID returns the node ID of the host.
//
// It checks if the node ID has a prefix of constants.AnsibleAWSNodePrefix
// and removes the prefix if present. Otherwise, it joins the first two parts
// of the node ID split by "_" and returns the result.
//
// Returns:
//   - string: The node ID of the host.
func (h Host) GetNodeID() string {
	if strings.HasPrefix(h.NodeID, constants.AnsibleAWSNodePrefix) {
		return strings.TrimPrefix(h.NodeID, constants.AnsibleAWSNodePrefix)
	}
	//default behaviour - TODO refactor for other clouds
	return strings.Join(strings.Split(h.NodeID, "_")[:2], "_")
}

// Connect starts a new SSH connection with the provided private key.
//
// It returns a pointer to a goph.Client and an error.
func (h Host) Connect() (*goph.Client,error) {
	// Start new ssh connection with private key.
	auth, err := goph.Key(h.SSHPrivateKeyPath, "")
	if err != nil {
		return nil,err
	}
	client, err := goph.NewConn(&goph.Config{
		User:     h.SSHUser,
		Addr:     h.IP,
		Port:     22,
		Auth:     auth,
		Timeout:  constants.DefaultSSHTimeout,
		Callback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		return nil,err
	}
	return client,nil
}

// Upload uploads a local file to a remote file on the host.
//
// localFile: the path of the local file to be uploaded.
// remoteFile: the path of the remote file to be created or overwritten.
// error: an error if there was a problem during the upload process.
func (h Host) Upload(localFile string, remoteFile string) error {
	client, err := h.Connect()
	if err != nil {
		return err
	} 
	defer client.Close()
	return client.Upload(localFile, remoteFile)
}

// Download downloads a file from the remote server to the local machine.
//
// remoteFile: the path to the file on the remote server.
// localFile: the path to the file on the local machine.
// error: returns an error if there was a problem downloading the file.
func (h Host) Download(remoteFile string, localFile string) error {
	client, err := h.Connect()
	if err != nil {
		return err
	} 
	defer client.Close()
	return client.Download(remoteFile, localFile)
}

// Command executes a shell command on a remote host.
//
// It takes a script string, an environment []string, and a context.Context as parameters.
// It returns a *goph.Cmd and an error.
func (h Host) Command(script string, env []string, ctx context.Context) error {
	client, err := h.Connect()
	if err != nil {
		return err
	}
	defer client.Close()
	cmd, err := client.CommandContext(ctx, shell, script)
	if err != nil {
		return err
	}
	if env != nil {
		cmd.Env = env
	}
	return cmd.Run()
}

// Forward forwards the TCP connection to a remote address.
//
// It returns an error if there was an issue connecting to the remote address or if there was an error in the port forwarding process.
func (h Host) Forward() error {
	client, err := h.Connect()
	if err != nil {
		return err
	}
	defer client.Close()
	remoteAddr, err := net.ResolveTCPAddr("tcp", constants.LocalAPIEndpoint)
	if err != nil {
		return err
	}
	proxy, err := client.DialTCP("tcp", nil, remoteAddr)
	if err != nil {
		return fmt.Errorf("unable to port forward to %s via %s", constants.LocalAPIEndpoint, "ssh")
	}

	errorChan := make(chan error)

	// Copy localConn.Reader to sshConn.Writer
	go func() {
		_, err = io.Copy(h.TCPProxy, proxy)
		if err != nil {
			errorChan <- err
		}
	}()

	// Copy sshConn.Reader to localConn.Writer
	go func() {
		_, err = io.Copy(proxy, h.TCPProxy)
		if err != nil {
			errorChan <- err
		}
	}()
	return nil
}

// ConvertToNodeID converts a node name to a node ID.
//
// It takes a nodeName string as a parameter and returns a string representing the node ID.
func (h Host) ConvertToNodeID(nodeName string) string {
	h = Host{
		NodeID:            nodeName,
		SSHUser:           "ubuntu",
		SSHPrivateKeyPath: "",
		SSHCommonArgs:     "",
	}
	return h.GetNodeID()
}

// GetAnsibleParams returns the string representation of the Ansible parameters for the Host.
//
// No parameters.
// Returns a string.
func (h Host) GetAnsibleParams() string {
	return strings.Join([]string{
		fmt.Sprintf("ansible_host=%s", h.IP),
		fmt.Sprintf("ansible_user=%s", h.SSHUser),
		fmt.Sprintf("ansible_ssh_private_key_file=%s", h.SSHPrivateKeyPath),
		fmt.Sprintf("ansible_ssh_common_args='%s'", h.SSHCommonArgs),
	}, " ")
}
