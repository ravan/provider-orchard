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
	"testing"
	"time"
)

func TestTunnelConfig_SetDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   TunnelConfig
		expected TunnelConfig
	}{
		{
			name:   "empty config gets defaults",
			config: TunnelConfig{},
			expected: TunnelConfig{
				SSHPort:     22,
				WaitSeconds: 30,
				Timeout:     30 * time.Second,
			},
		},
		{
			name: "custom values are preserved",
			config: TunnelConfig{
				SSHPort:     2222,
				WaitSeconds: 60,
				Timeout:     60 * time.Second,
			},
			expected: TunnelConfig{
				SSHPort:     2222,
				WaitSeconds: 60,
				Timeout:     60 * time.Second,
			},
		},
		{
			name: "partial config gets partial defaults",
			config: TunnelConfig{
				SSHPort: 2222,
			},
			expected: TunnelConfig{
				SSHPort:     2222,
				WaitSeconds: 30,
				Timeout:     30 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.SetDefaults()
			if tt.config.SSHPort != tt.expected.SSHPort {
				t.Errorf("SSHPort = %d, want %d", tt.config.SSHPort, tt.expected.SSHPort)
			}
			if tt.config.WaitSeconds != tt.expected.WaitSeconds {
				t.Errorf("WaitSeconds = %d, want %d", tt.config.WaitSeconds, tt.expected.WaitSeconds)
			}
			if tt.config.Timeout != tt.expected.Timeout {
				t.Errorf("Timeout = %v, want %v", tt.config.Timeout, tt.expected.Timeout)
			}
		})
	}
}

func TestBuildWebSocketURL(t *testing.T) {
	tests := []struct {
		name        string
		config      TunnelConfig
		expectedURL string
		expectError bool
	}{
		{
			name: "http URL converts to ws with path preserved",
			config: TunnelConfig{
				OrchardBaseURL: "http://localhost:6120/v1",
				VMName:         "test-vm",
				SSHPort:        22,
				WaitSeconds:    30,
			},
			expectedURL: "ws://localhost:6120/v1/vms/test-vm/port-forward?port=22&wait=30",
		},
		{
			name: "https URL converts to wss with path preserved",
			config: TunnelConfig{
				OrchardBaseURL: "https://orchard.example.com/v1",
				VMName:         "my-vm",
				SSHPort:        22,
				WaitSeconds:    60,
			},
			expectedURL: "wss://orchard.example.com/v1/vms/my-vm/port-forward?port=22&wait=60",
		},
		{
			name: "custom port",
			config: TunnelConfig{
				OrchardBaseURL: "http://localhost:6120/v1",
				VMName:         "test-vm",
				SSHPort:        2222,
				WaitSeconds:    10,
			},
			expectedURL: "ws://localhost:6120/v1/vms/test-vm/port-forward?port=2222&wait=10",
		},
		{
			name: "VM name with special characters is escaped",
			config: TunnelConfig{
				OrchardBaseURL: "http://localhost:6120/v1",
				VMName:         "test vm/special",
				SSHPort:        22,
				WaitSeconds:    30,
			},
			expectedURL: "ws://localhost:6120/v1/vms/test%20vm%2Fspecial/port-forward?port=22&wait=30",
		},
		{
			name: "zero wait seconds omits wait parameter",
			config: TunnelConfig{
				OrchardBaseURL: "http://localhost:6120/v1",
				VMName:         "test-vm",
				SSHPort:        22,
				WaitSeconds:    0,
			},
			expectedURL: "ws://localhost:6120/v1/vms/test-vm/port-forward?port=22",
		},
		{
			name: "base URL without path still works",
			config: TunnelConfig{
				OrchardBaseURL: "http://localhost:6120",
				VMName:         "test-vm",
				SSHPort:        22,
				WaitSeconds:    30,
			},
			expectedURL: "ws://localhost:6120/vms/test-vm/port-forward?port=22&wait=30",
		},
		{
			name: "invalid URL returns error",
			config: TunnelConfig{
				OrchardBaseURL: "://invalid",
				VMName:         "test-vm",
				SSHPort:        22,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, err := buildWebSocketURL(tt.config)
			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if url != tt.expectedURL {
				t.Errorf("URL = %q, want %q", url, tt.expectedURL)
			}
		})
	}
}

func TestCommandResult(t *testing.T) {
	result := &CommandResult{
		ExitCode: 0,
		Stdout:   "hello world\n",
		Stderr:   "",
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "hello world\n" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello world\n")
	}
}

func TestFileUploadOptions(t *testing.T) {
	opts := FileUploadOptions{
		RemotePath:  "/tmp/test.sh",
		Permissions: 0755,
		CreateDirs:  true,
	}

	if opts.RemotePath != "/tmp/test.sh" {
		t.Errorf("RemotePath = %q, want %q", opts.RemotePath, "/tmp/test.sh")
	}
	if opts.Permissions != 0755 {
		t.Errorf("Permissions = %o, want %o", opts.Permissions, 0755)
	}
	if !opts.CreateDirs {
		t.Error("CreateDirs = false, want true")
	}
}
