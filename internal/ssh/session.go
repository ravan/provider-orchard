/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// VMSession represents an active SSH session to a VM via WebSocket tunnel.
type VMSession interface {
	// ExecuteCommand runs a command on the VM and returns the result.
	ExecuteCommand(ctx context.Context, command string) (*CommandResult, error)

	// ExecuteScript runs a multi-line script on the VM with optional environment variables.
	ExecuteScript(ctx context.Context, script string, env map[string]string) (*CommandResult, error)

	// UploadFile uploads content to a file on the VM.
	UploadFile(ctx context.Context, content io.Reader, opts FileUploadOptions) error

	// UploadBytes is a convenience method for uploading byte content.
	UploadBytes(ctx context.Context, data []byte, opts FileUploadOptions) error

	// Close terminates the SSH session and WebSocket connection.
	Close() error
}

// vmSession implements VMSession interface.
type vmSession struct {
	config     TunnelConfig
	wsConn     net.Conn
	sshClient  *ssh.Client
	sftpClient *sftp.Client
}

// NewVMSession establishes a WebSocket-SSH tunnel to a VM.
// It connects to the Orchard controller's port-forward endpoint and establishes
// an SSH connection over that WebSocket tunnel.
func NewVMSession(ctx context.Context, config TunnelConfig) (VMSession, error) {
	config.SetDefaults()

	// Establish WebSocket connection to Orchard's port-forward endpoint
	wsConn, err := dialWebSocket(ctx, config)
	if err != nil {
		return nil, err
	}

	// Configure SSH client with password authentication
	sshConfig := &ssh.ClientConfig{
		User: config.SSHUsername,
		Auth: []ssh.AuthMethod{
			ssh.Password(config.SSHPassword),
		},
		// VM is trusted via Orchard - host key verification not needed for ephemeral VMs
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         config.Timeout,
	}

	// Create SSH connection over WebSocket
	// The wsConn is already connected to the VM's SSH port via Orchard's port-forward
	conn, chans, reqs, err := ssh.NewClientConn(wsConn, "vm", sshConfig)
	if err != nil {
		wsConn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, errors.Wrap(ErrSSHAuthFailed, err.Error())
		}
		return nil, errors.Wrap(err, "failed to establish SSH connection")
	}

	sshClient := ssh.NewClient(conn, chans, reqs)

	return &vmSession{
		config:    config,
		wsConn:    wsConn,
		sshClient: sshClient,
	}, nil
}

// ExecuteCommand runs a single command on the VM.
func (s *vmSession) ExecuteCommand(ctx context.Context, command string) (*CommandResult, error) {
	session, err := s.sshClient.NewSession()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create SSH session")
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run the command
	err = session.Run(command)

	result := &CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			result.ExitCode = exitErr.ExitStatus()
			return result, nil // Non-zero exit is not an error, just captured in result
		}
		return result, errors.Wrap(err, "command execution failed")
	}

	result.ExitCode = 0
	return result, nil
}

// ExecuteScript runs a multi-line script on the VM with optional environment variables.
func (s *vmSession) ExecuteScript(ctx context.Context, script string, env map[string]string) (*CommandResult, error) {
	session, err := s.sshClient.NewSession()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create SSH session")
	}
	defer session.Close()

	// Build script with environment variables
	var scriptBuilder strings.Builder
	scriptBuilder.WriteString("#!/bin/bash\nset -e\n")
	for key, value := range env {
		// Escape the value for shell
		escapedValue := strings.ReplaceAll(value, "'", "'\"'\"'")
		scriptBuilder.WriteString(fmt.Sprintf("export %s='%s'\n", key, escapedValue))
	}
	scriptBuilder.WriteString(script)

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Use stdin to pass the script
	session.Stdin = strings.NewReader(scriptBuilder.String())

	err = session.Run("/bin/bash")

	result := &CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			result.ExitCode = exitErr.ExitStatus()
			return result, nil
		}
		return result, errors.Wrap(err, "script execution failed")
	}

	result.ExitCode = 0
	return result, nil
}

// ensureSFTP initializes the SFTP client if not already done.
func (s *vmSession) ensureSFTP() error {
	if s.sftpClient != nil {
		return nil
	}

	client, err := sftp.NewClient(s.sshClient)
	if err != nil {
		return errors.Wrap(ErrSFTPFailed, err.Error())
	}
	s.sftpClient = client
	return nil
}

// UploadFile uploads content to a file on the VM.
func (s *vmSession) UploadFile(ctx context.Context, content io.Reader, opts FileUploadOptions) error {
	if err := s.ensureSFTP(); err != nil {
		return err
	}

	// Create parent directories if requested
	if opts.CreateDirs {
		dir := filepath.Dir(opts.RemotePath)
		if err := s.sftpClient.MkdirAll(dir); err != nil {
			return errors.Wrapf(ErrSFTPFailed, "failed to create directory %s: %v", dir, err)
		}
	}

	// Create/truncate the file
	f, err := s.sftpClient.Create(opts.RemotePath)
	if err != nil {
		return errors.Wrapf(ErrSFTPFailed, "failed to create file %s: %v", opts.RemotePath, err)
	}
	defer f.Close()

	// Copy content
	if _, err := io.Copy(f, content); err != nil {
		return errors.Wrap(ErrSFTPFailed, "failed to write file content")
	}

	// Set permissions if specified
	if opts.Permissions != 0 {
		if err := s.sftpClient.Chmod(opts.RemotePath, os.FileMode(opts.Permissions)); err != nil {
			return errors.Wrap(ErrSFTPFailed, "failed to set file permissions")
		}
	}

	return nil
}

// UploadBytes is a convenience method for uploading byte content.
func (s *vmSession) UploadBytes(ctx context.Context, data []byte, opts FileUploadOptions) error {
	return s.UploadFile(ctx, bytes.NewReader(data), opts)
}

// Close terminates all connections.
func (s *vmSession) Close() error {
	var errs []error

	if s.sftpClient != nil {
		if err := s.sftpClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if s.sshClient != nil {
		if err := s.sshClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if s.wsConn != nil {
		if err := s.wsConn.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during close: %v", errs)
	}
	return nil
}

// RunCloudInit executes cloud-init style initialization on a VM.
// This is a convenience function that creates a session, runs the script, and closes.
func RunCloudInit(ctx context.Context, config TunnelConfig, script string, env map[string]string) (*CommandResult, error) {
	session, err := NewVMSession(ctx, config)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	return session.ExecuteScript(ctx, script, env)
}

// UploadAndRunScript uploads a script file and executes it.
// This is useful for larger scripts that may have issues with stdin piping.
func UploadAndRunScript(ctx context.Context, config TunnelConfig, script string, env map[string]string) (*CommandResult, error) {
	session, err := NewVMSession(ctx, config)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	// Upload script to temp location
	scriptPath := "/tmp/cloudinit-script.sh"
	if err := session.UploadBytes(ctx, []byte(script), FileUploadOptions{
		RemotePath:  scriptPath,
		Permissions: 0755,
		CreateDirs:  true,
	}); err != nil {
		return nil, errors.Wrap(err, "failed to upload script")
	}

	// Build command with environment variables
	var cmdBuilder strings.Builder
	for key, value := range env {
		escapedValue := strings.ReplaceAll(value, "'", "'\"'\"'")
		cmdBuilder.WriteString(fmt.Sprintf("export %s='%s'; ", key, escapedValue))
	}
	cmdBuilder.WriteString(scriptPath)

	return session.ExecuteCommand(ctx, cmdBuilder.String())
}
