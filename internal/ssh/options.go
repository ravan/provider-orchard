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
	"errors"
	"time"
)

// Sentinel errors for SSH tunnel operations
var (
	ErrConnectionFailed = errors.New("failed to connect to Orchard controller")
	ErrSSHAuthFailed    = errors.New("SSH authentication failed")
	ErrVMNotReady       = errors.New("VM is not ready")
	ErrTimeout          = errors.New("operation timed out")
	ErrSFTPFailed       = errors.New("SFTP operation failed")
)

// TunnelConfig holds configuration for establishing a WebSocket-SSH tunnel
type TunnelConfig struct {
	// OrchardBaseURL is the Orchard controller URL including the API version path
	// (e.g., "http://localhost:6120/v1")
	OrchardBaseURL string

	// BearerToken is the Orchard API authentication token
	BearerToken string

	// VMName is the name of the target VM in Orchard
	VMName string

	// SSHPort is the SSH port on the VM (typically 22)
	SSHPort int

	// WaitSeconds is how long to wait for VM to become running (default: 30)
	WaitSeconds int

	// SSHUsername for VM authentication
	SSHUsername string

	// SSHPassword for VM authentication
	SSHPassword string

	// Timeout for SSH operations (default: 30s)
	Timeout time.Duration
}

// SetDefaults applies default values to the config
func (c *TunnelConfig) SetDefaults() {
	if c.SSHPort == 0 {
		c.SSHPort = 22
	}
	if c.WaitSeconds == 0 {
		c.WaitSeconds = 30
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
}

// CommandResult holds the result of executing a command on the VM
type CommandResult struct {
	// ExitCode is the exit status of the command (0 = success)
	ExitCode int

	// Stdout contains the standard output
	Stdout string

	// Stderr contains the standard error output
	Stderr string
}

// FileUploadOptions configures file upload behavior
type FileUploadOptions struct {
	// RemotePath is the destination path on the VM
	RemotePath string

	// Permissions are the file permissions (e.g., 0644)
	Permissions uint32

	// CreateDirs creates parent directories if they don't exist
	CreateDirs bool
}
