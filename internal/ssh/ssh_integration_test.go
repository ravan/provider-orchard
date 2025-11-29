//go:build integration

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
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Integration tests bootstrap their own Orchard environment.
// Run with: go test -tags=integration ./internal/ssh/...
//
// The tests will:
// 1. Start 'orchard dev' if not already running
// 2. Create a service account and get a token (cached in .dev-data/test-token)
// 3. Create a test VM
// 4. Run the tests
// 5. Clean up the VM (even on failure)

const (
	orchardPort     = "6120"
	orchardURL      = "http://localhost:" + orchardPort + "/v1"
	vmName          = "ssh-test"
	vmImage         = "ghcr.io/cirruslabs/ubuntu:22.04"
	vmCPU           = "2"
	vmMemory        = "4096"
	sshUsername     = "admin"
	sshPassword     = "admin"
	serviceAccount  = "ssh-test-sa"
	apiReadyTimeout = 60 * time.Second
	vmReadyTimeout  = 180 * time.Second
	sshReadyTimeout = 120 * time.Second
)

var (
	// Global test config populated by TestMain
	testConfig     TunnelConfig
	testConfigured bool

	// Track if we started orchard so we know whether to stop it
	orchardCmd     *exec.Cmd
	weStartedOrchard bool

	// Data directory for orchard
	dataDir string
)

func TestMain(m *testing.M) {
	code := 0
	defer func() { os.Exit(code) }()

	// Find project root (where .dev-data should be)
	projectRoot, err := findProjectRoot()
	if err != nil {
		log.Fatalf("Failed to find project root: %v", err)
	}
	dataDir = filepath.Join(projectRoot, ".dev-data")

	if err := setupOrchardEnv(); err != nil {
		log.Fatalf("Failed to setup orchard environment: %v", err)
	}
	defer cleanupOrchardEnv()

	code = m.Run()
}

func findProjectRoot() (string, error) {
	// Start from current directory and walk up looking for go.mod
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}

func setupOrchardEnv() error {
	log.Println("Setting up Orchard environment...")

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	// Check if orchard is already running
	if isOrchardRunning() {
		log.Println("Orchard is already running, reusing existing instance")
		weStartedOrchard = false
	} else {
		log.Println("Starting orchard dev...")
		if err := startOrchard(); err != nil {
			return fmt.Errorf("failed to start orchard: %w", err)
		}
		weStartedOrchard = true

		// Wait for API to be ready
		if err := waitForAPI(apiReadyTimeout); err != nil {
			return fmt.Errorf("orchard API not ready: %w", err)
		}
		log.Println("Orchard API is ready")
	}

	// Get or create service account token
	token, err := getOrCreateToken()
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}
	log.Println("Got service account token")

	// Delete existing VM if it exists (cleanup from previous failed run)
	_ = deleteVM(vmName)

	// Create test VM
	if err := createVM(); err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}
	log.Println("Created test VM")

	// Wait for VM to be running
	if err := waitForVM(vmReadyTimeout); err != nil {
		return fmt.Errorf("VM not ready: %w", err)
	}
	log.Println("VM is running")

	// Build test config
	testConfig = TunnelConfig{
		OrchardBaseURL: orchardURL,
		BearerToken:    token,
		VMName:         vmName,
		SSHUsername:    sshUsername,
		SSHPassword:    sshPassword,
		SSHPort:        22,
		WaitSeconds:    60,
		Timeout:        30 * time.Second,
	}

	// Wait for SSH to be available
	if err := waitForSSH(sshReadyTimeout); err != nil {
		return fmt.Errorf("SSH not ready: %w", err)
	}
	log.Println("SSH is ready")

	testConfigured = true
	log.Println("Orchard environment setup complete")
	return nil
}

func cleanupOrchardEnv() {
	log.Println("Cleaning up Orchard environment...")

	// Always try to delete the VM
	if err := deleteVM(vmName); err != nil {
		log.Printf("Warning: failed to delete VM: %v", err)
	} else {
		log.Println("Deleted test VM")
	}

	// Only stop orchard if we started it
	if weStartedOrchard && orchardCmd != nil {
		log.Println("Stopping orchard dev...")
		if err := orchardCmd.Process.Kill(); err != nil {
			log.Printf("Warning: failed to kill orchard: %v", err)
		}
		orchardCmd.Wait()
		log.Println("Orchard stopped")
	}
}

func isOrchardRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Use orchardURL directly since it already includes /v1
	req, err := http.NewRequestWithContext(ctx, "GET", orchardURL+"/", nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func startOrchard() error {
	orchardCmd = exec.Command("orchard", "dev", "--data-dir", dataDir)

	// Redirect output to log file to avoid flooding test output
	logFile, err := os.Create(filepath.Join(dataDir, "orchard.log"))
	if err != nil {
		return fmt.Errorf("failed to create orchard log file: %w", err)
	}
	orchardCmd.Stdout = logFile
	orchardCmd.Stderr = logFile

	if err := orchardCmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start orchard: %w", err)
	}

	// Store log file handle for cleanup (don't close it while orchard is running)
	log.Printf("Orchard dev started (PID: %d), logs at %s/orchard.log", orchardCmd.Process.Pid, dataDir)
	return nil
}

func waitForAPI(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if isOrchardRunning() {
			return nil
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for orchard API")
}

func getOrCreateToken() (string, error) {
	tokenFile := filepath.Join(dataDir, "test-token")

	// Check if we have a cached token and orchard is running
	if data, err := os.ReadFile(tokenFile); err == nil {
		token := strings.TrimSpace(string(data))
		if token != "" {
			log.Println("Using cached token from", tokenFile)
			return token, nil
		}
	}

	// Create service account (ignore error if it already exists)
	_, _ = runOrchardCommand("create", "service-account", serviceAccount,
		"--roles", "compute:read",
		"--roles", "compute:write")

	// Get bootstrap token
	output, err := runOrchardCommand("get", "bootstrap-token", serviceAccount)
	if err != nil {
		return "", fmt.Errorf("failed to get bootstrap token: %w", err)
	}

	token := strings.TrimSpace(output)
	if token == "" {
		return "", fmt.Errorf("empty token returned")
	}

	// Cache the token
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		log.Printf("Warning: failed to cache token: %v", err)
	}

	return token, nil
}

func createVM() error {
	_, err := runOrchardCommand("create", "vm", vmName,
		"--image", vmImage,
		"--cpu", vmCPU,
		"--memory", vmMemory)
	return err
}

func deleteVM(name string) error {
	_, err := runOrchardCommand("delete", "vm", name)
	return err
}

func waitForVM(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		output, err := runOrchardCommand("list", "vms")
		if err == nil && strings.Contains(output, vmName) {
			// Check if VM is running (look for "running" status in output)
			// The output format varies, so we just check if the VM exists
			// SSH readiness check will confirm it's actually usable
			return nil
		}
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("timeout waiting for VM to be ready")
}

func waitForSSH(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		session, err := NewVMSession(ctx, testConfig)
		if err == nil {
			// Try a simple command
			result, err := session.ExecuteCommand(ctx, "echo ready")
			session.Close()
			cancel()
			if err == nil && result.ExitCode == 0 {
				return nil
			}
		} else {
			cancel()
		}
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("timeout waiting for SSH to be ready")
}

func runOrchardCommand(args ...string) (string, error) {
	cmd := exec.Command("orchard", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("orchard %s failed: %w\nstderr: %s", strings.Join(args, " "), err, stderr.String())
	}

	return stdout.String(), nil
}

func getTestConfig(t *testing.T) TunnelConfig {
	if !testConfigured {
		t.Fatal("Test environment not configured - TestMain setup failed")
	}
	return testConfig
}

func TestIntegration_NewVMSession(t *testing.T) {
	config := getTestConfig(t)
	ctx := context.Background()

	session, err := NewVMSession(ctx, config)
	if err != nil {
		t.Fatalf("NewVMSession failed: %v", err)
	}
	defer session.Close()

	t.Log("Successfully established SSH session via WebSocket tunnel")
}

func TestIntegration_ExecuteCommand(t *testing.T) {
	config := getTestConfig(t)
	ctx := context.Background()

	session, err := NewVMSession(ctx, config)
	if err != nil {
		t.Fatalf("NewVMSession failed: %v", err)
	}
	defer session.Close()

	result, err := session.ExecuteCommand(ctx, "echo hello")
	if err != nil {
		t.Fatalf("ExecuteCommand failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello\n")
	}
	if result.Stderr != "" {
		t.Errorf("Stderr = %q, want empty", result.Stderr)
	}
}

func TestIntegration_ExecuteCommand_NonZeroExit(t *testing.T) {
	config := getTestConfig(t)
	ctx := context.Background()

	session, err := NewVMSession(ctx, config)
	if err != nil {
		t.Fatalf("NewVMSession failed: %v", err)
	}
	defer session.Close()

	result, err := session.ExecuteCommand(ctx, "exit 42")
	if err != nil {
		t.Fatalf("ExecuteCommand failed: %v", err)
	}

	if result.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", result.ExitCode)
	}
}

func TestIntegration_ExecuteScript(t *testing.T) {
	config := getTestConfig(t)
	ctx := context.Background()

	session, err := NewVMSession(ctx, config)
	if err != nil {
		t.Fatalf("NewVMSession failed: %v", err)
	}
	defer session.Close()

	script := `
echo "Starting script"
echo "Value is: $MY_VAR"
echo "Done"
`
	env := map[string]string{
		"MY_VAR": "test-value",
	}

	result, err := session.ExecuteScript(ctx, script, env)
	if err != nil {
		t.Fatalf("ExecuteScript failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0\nStderr: %s", result.ExitCode, result.Stderr)
	}

	expected := "Starting script\nValue is: test-value\nDone\n"
	if result.Stdout != expected {
		t.Errorf("Stdout = %q, want %q", result.Stdout, expected)
	}
}

func TestIntegration_UploadFile(t *testing.T) {
	config := getTestConfig(t)
	ctx := context.Background()

	session, err := NewVMSession(ctx, config)
	if err != nil {
		t.Fatalf("NewVMSession failed: %v", err)
	}
	defer session.Close()

	// Upload a test file
	content := []byte("test content from integration test\n")
	remotePath := "/tmp/ssh-integration-test.txt"

	err = session.UploadBytes(ctx, content, FileUploadOptions{
		RemotePath:  remotePath,
		Permissions: 0644,
		CreateDirs:  true,
	})
	if err != nil {
		t.Fatalf("UploadBytes failed: %v", err)
	}

	// Verify the file was uploaded correctly
	result, err := session.ExecuteCommand(ctx, "cat "+remotePath)
	if err != nil {
		t.Fatalf("ExecuteCommand failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Stdout != string(content) {
		t.Errorf("File content = %q, want %q", result.Stdout, string(content))
	}

	// Clean up
	session.ExecuteCommand(ctx, "rm "+remotePath)
}

func TestIntegration_UploadAndRunScript(t *testing.T) {
	config := getTestConfig(t)
	ctx := context.Background()

	script := `#!/bin/bash
echo "Running uploaded script"
echo "Environment: $TEST_ENV"
`
	env := map[string]string{
		"TEST_ENV": "integration-test",
	}

	result, err := UploadAndRunScript(ctx, config, script, env)
	if err != nil {
		t.Fatalf("UploadAndRunScript failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0\nStderr: %s", result.ExitCode, result.Stderr)
	}

	t.Logf("Script output: %s", result.Stdout)
}

func TestIntegration_RunCloudInit(t *testing.T) {
	config := getTestConfig(t)
	ctx := context.Background()

	script := `
echo "Cloud-init simulation"
whoami
pwd
`
	result, err := RunCloudInit(ctx, config, script, nil)
	if err != nil {
		t.Fatalf("RunCloudInit failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0\nStderr: %s", result.ExitCode, result.Stderr)
	}

	t.Logf("Cloud-init output:\n%s", result.Stdout)
}
