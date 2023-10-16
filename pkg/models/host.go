// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.
package models

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/ava-labs/avalanche-cli/pkg/constants"
	"github.com/melbahja/goph"
)

type Host struct {
	NodeID            string
	IP                string
	SSHUser           string
	SSHPrivateKeyPath string
	SSHCommonArgs     string
	client            *goph.Client
	TCPProxy          *bytes.Buffer
}

const (
	shell     = "/bin/bash"
	localhost = "127.0.0.1"
)

func (h Host) Connect() error {
	// Start new ssh connection with private key.
	auth, err := goph.Key(h.SSHPrivateKeyPath, "")
	if err != nil {
		return err
	}

	client, err := goph.NewUnknown(h.SSHUser, h.IP, auth)
	if err != nil {
		return err
	}
	h.client = client
	return nil
}

func (h Host) Upload(localFile string, remoteFile string) error {
	if h.client == nil {
		if err := h.Connect(); err != nil {
			return err
		}
	}
	return h.client.Upload(localFile, remoteFile)
}

func (h Host) Download(remoteFile string, localFile string) error {
	if h.client == nil {
		if err := h.Connect(); err != nil {
			return err
		}
	}
	return h.client.Download(remoteFile, localFile)
}

func (h Host) Close() error {
	if h.client == nil {
		return nil
	}

	return h.client.Close()
}

func (h Host) Command(script string, env []string, ctx context.Context, timeout time.Duration) (*goph.Cmd, error) {
	if h.client == nil {
		if err := h.Connect(); err != nil {
			return nil, err
		}
	}
	if timeout > 0 {
		h.client.Config.Timeout = timeout
	}
	cmd, err := h.client.CommandContext(ctx, shell, script)
	if err != nil {
		return nil, err
	}
	cmd.Env = env
	return cmd, nil
}

func (h Host) Forward() error {
	if h.client == nil {
		if err := h.Connect(); err != nil {
			return err
		}
	}
	remoteAddr, err := net.ResolveTCPAddr("tcp", constants.LocalAPIEndpoint)
	if err != nil {
		return err
	}
	proxy, err := h.client.DialTCP("tcp", nil, remoteAddr)
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