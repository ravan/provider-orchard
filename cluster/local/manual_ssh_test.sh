#!/usr/bin/env bash
set -euo pipefail

# Manual test script to verify Orchard SSH/port-forward functionality
# This tests the underlying technology before using it via REST API

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
ORCHARD_DATA_DIR="${ORCHARD_DATA_DIR:-.orchard-manual-test}"
ORCHARD_PORT="${ORCHARD_PORT:-6120}"
VM_NAME="ssh-test-vm"
SSH_PORT="${SSH_PORT:-2222}"

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

cleanup() {
    log_info "Cleaning up..."

    # Kill port-forward if running
    if [ -f "$ORCHARD_DATA_DIR/portforward.pid" ]; then
        local pid=$(cat "$ORCHARD_DATA_DIR/portforward.pid")
        if ps -p $pid > /dev/null 2>&1; then
            log_info "Stopping port-forward (PID: $pid)"
            kill $pid 2>/dev/null || true
        fi
        rm -f "$ORCHARD_DATA_DIR/portforward.pid"
    fi

    # Delete VM
    if orchard list vms 2>/dev/null | grep -q "^${VM_NAME}"; then
        log_info "Deleting VM: $VM_NAME"
        orchard delete vm "$VM_NAME" 2>/dev/null || true
    fi

    # Stop orchard if we started it
    if [ -f "$ORCHARD_DATA_DIR/orchard.pid" ]; then
        local pid=$(cat "$ORCHARD_DATA_DIR/orchard.pid")
        if ps -p $pid > /dev/null 2>&1; then
            log_info "Stopping orchard dev (PID: $pid)"
            kill $pid 2>/dev/null || true
            sleep 2
            kill -9 $pid 2>/dev/null || true
        fi
        rm -f "$ORCHARD_DATA_DIR/orchard.pid"
    fi

    # Clean up data dir
    if [ -d "$ORCHARD_DATA_DIR" ]; then
        rm -rf "$ORCHARD_DATA_DIR"
    fi

    log_info "Cleanup complete"
}

trap cleanup EXIT

start_orchard() {
    log_info "Starting orchard dev environment..."

    mkdir -p "$ORCHARD_DATA_DIR"

    # Check if orchard is already running
    if lsof -i :$ORCHARD_PORT >/dev/null 2>&1; then
        log_warn "Port $ORCHARD_PORT already in use, assuming orchard is running"
        return 0
    fi

    # Start orchard in background
    nohup orchard dev --data-dir "$ORCHARD_DATA_DIR" > "$ORCHARD_DATA_DIR/orchard.log" 2>&1 &
    local pid=$!
    echo $pid > "$ORCHARD_DATA_DIR/orchard.pid"

    log_info "Orchard started (PID: $pid)"

    # Wait for orchard to be ready
    log_info "Waiting for orchard API to be ready..."
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if curl -sf http://localhost:$ORCHARD_PORT/v1/ >/dev/null 2>&1; then
            log_info "Orchard API is ready"
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done

    log_error "Orchard failed to start within 30 seconds"
    cat "$ORCHARD_DATA_DIR/orchard.log"
    exit 1
}

create_vm() {
    log_info "Creating VM: $VM_NAME (Ubuntu with admin/admin credentials)..."

    # Delete if exists
    if orchard list vms 2>/dev/null | grep -q "^${VM_NAME}"; then
        log_warn "VM already exists, deleting..."
        orchard delete vm "$VM_NAME"
        sleep 5
    fi

    # Create VM - Ubuntu has admin/admin credentials by default
    orchard create vm "$VM_NAME" \
        --image ghcr.io/cirruslabs/ubuntu:latest \
        --cpu 2 \
        --memory 4096

    log_info "VM created, waiting for it to start..."

    # Wait for VM to be running
    local max_attempts=60
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        # Parse tab-separated output - Status is in the 4th tab-separated field
        local status=$(orchard list vms 2>/dev/null | grep "^${VM_NAME}" | awk -F'\t' '{print $4}' | tr -d ' ' || echo "")

        if [ "$status" = "running" ]; then
            log_info "VM is running"
            return 0
        elif [ "$status" = "failed" ]; then
            log_error "VM failed to start!"
            orchard list vms
            orchard logs vm "$VM_NAME" || true
            return 1
        fi

        if [ $((attempt % 10)) -eq 0 ]; then
            log_info "  VM status: ${status:-pending} (attempt $attempt/$max_attempts)"
        fi

        attempt=$((attempt + 1))
        sleep 2
    done

    log_error "VM did not start within 120 seconds"
    orchard list vms
    return 1
}

wait_for_vm_ready() {
    log_info "Waiting for VM to be fully ready..."

    # Give the VM time to fully boot and network to initialize
    sleep 10
    log_info "VM should be ready now"
}

start_port_forward() {
    log_info "Starting port-forward to VM SSH port (local:$SSH_PORT -> vm:22)..."

    # Kill any existing port-forward on this port
    lsof -ti :$SSH_PORT | xargs kill 2>/dev/null || true
    sleep 1

    # Start port-forward in background
    nohup orchard port-forward vm "$VM_NAME" $SSH_PORT:22 > "$ORCHARD_DATA_DIR/portforward.log" 2>&1 &
    local pid=$!
    echo $pid > "$ORCHARD_DATA_DIR/portforward.pid"

    log_info "Port-forward started (PID: $pid)"

    # Wait for port to be available
    log_info "Waiting for port-forward to be ready..."
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if nc -z localhost $SSH_PORT 2>/dev/null; then
            log_info "Port-forward is ready"
            return 0
        fi
        attempt=$((attempt + 1))
        sleep 1
    done

    log_error "Port-forward failed to start"
    cat "$ORCHARD_DATA_DIR/portforward.log" || true
    return 1
}

create_test_script() {
    log_info "Creating test script..."

    cat > "$ORCHARD_DATA_DIR/test_script.sh" << 'SCRIPT'
#!/bin/bash
echo "=== Test Script Started ==="
echo "Date: $(date)"
echo "Hostname: $(hostname)"
echo "User: $(whoami)"
echo "Working directory: $(pwd)"

# Test environment variable
if [ -n "$TEST_VAR" ]; then
    echo "TEST_VAR: $TEST_VAR"
fi

# Create marker file
echo "Cloud-init test completed at $(date)" > /tmp/cloudinit-marker.txt
cat /tmp/cloudinit-marker.txt

echo "=== Test Script Completed Successfully ==="
exit 0
SCRIPT

    chmod +x "$ORCHARD_DATA_DIR/test_script.sh"
    log_info "Test script created at $ORCHARD_DATA_DIR/test_script.sh"
}

test_ssh_via_portforward() {
    log_info ""
    log_info "=========================================="
    log_info "TEST A: SSH via port-forward (localhost:$SSH_PORT)"
    log_info "=========================================="

    # Wait for SSH to be ready
    log_info "Waiting for SSH to be ready..."
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if sshpass -p admin ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 -p $SSH_PORT admin@localhost "echo 'SSH ready'" 2>/dev/null; then
            log_info "SSH is ready"
            break
        fi
        attempt=$((attempt + 1))
        sleep 2
    done

    if [ $attempt -eq $max_attempts ]; then
        log_error "SSH not ready after 60 seconds"
        return 1
    fi

    # Copy script via SCP
    log_info "Copying test script via SCP..."
    if ! sshpass -p admin scp -o StrictHostKeyChecking=no -P $SSH_PORT \
        "$ORCHARD_DATA_DIR/test_script.sh" admin@localhost:/tmp/test_script.sh; then
        log_error "SCP failed"
        return 1
    fi
    log_info "Script copied successfully"

    # Execute script via SSH
    log_info "Executing test script via SSH..."
    local output
    if output=$(sshpass -p admin ssh -o StrictHostKeyChecking=no -p $SSH_PORT admin@localhost \
        "export TEST_VAR='hello-from-portforward'; chmod +x /tmp/test_script.sh && /tmp/test_script.sh" 2>&1); then
        log_info "Script output:"
        echo "$output" | sed 's/^/  /'
        log_info "TEST A: PASSED"
        return 0
    else
        log_error "Script execution failed:"
        echo "$output" | sed 's/^/  /'
        log_error "TEST A: FAILED"
        return 1
    fi
}

test_ssh_via_orchard() {
    log_info ""
    log_info "=========================================="
    log_info "TEST B: SSH via 'orchard ssh' command"
    log_info "=========================================="

    # Execute script via orchard ssh
    log_info "Executing test script via 'orchard ssh'..."
    local output
    if output=$(orchard ssh vm "$VM_NAME" -u admin -p admin \
        "export TEST_VAR='hello-from-orchard-ssh'; /tmp/test_script.sh" 2>&1); then
        log_info "Script output:"
        echo "$output" | sed 's/^/  /'
        log_info "TEST B: PASSED"
        return 0
    else
        log_error "Script execution failed:"
        echo "$output" | sed 's/^/  /'
        log_error "TEST B: FAILED"
        return 1
    fi
}

check_vm_status() {
    log_info ""
    log_info "=========================================="
    log_info "Checking final VM status..."
    log_info "=========================================="

    sleep 5  # Wait a bit for any status updates

    local status=$(orchard list vms 2>/dev/null | grep "^${VM_NAME}" | awk -F'\t' '{print $4}' | tr -d ' ' || echo "unknown")

    log_info "VM Status: $status"

    if [ "$status" = "failed" ]; then
        log_error "VM is in FAILED state!"
        log_info "VM logs:"
        orchard logs vm "$VM_NAME" 2>&1 | tail -50 | sed 's/^/  /'
        return 1
    elif [ "$status" = "running" ]; then
        log_info "VM is still running (good!)"
        return 0
    else
        log_warn "VM status is: $status"
        return 0
    fi
}

print_summary() {
    log_info ""
    log_info "=========================================="
    log_info "Test Summary"
    log_info "=========================================="
    log_info "VM Name: $VM_NAME"
    log_info "SSH Port (local): $SSH_PORT"
    log_info "Orchard Port: $ORCHARD_PORT"
    log_info "=========================================="
}

main() {
    log_info "Manual SSH Test for Orchard"
    log_info ""

    # Check prerequisites
    for cmd in orchard sshpass nc jq curl; do
        if ! command -v $cmd &> /dev/null; then
            log_error "Missing required command: $cmd"
            if [ "$cmd" = "sshpass" ]; then
                log_info "Install with: brew install hudochenkov/sshpass/sshpass"
            fi
            exit 1
        fi
    done

    local test_a_passed=false
    local test_b_passed=false

    start_orchard
    create_vm || exit 1
    wait_for_vm_ready
    start_port_forward || exit 1
    create_test_script

    # Run tests
    if test_ssh_via_portforward; then
        test_a_passed=true
    fi

    if test_ssh_via_orchard; then
        test_b_passed=true
    fi

    check_vm_status
    local vm_ok=$?

    print_summary

    log_info ""
    log_info "=========================================="
    log_info "Results:"
    log_info "=========================================="

    if $test_a_passed; then
        log_info "  TEST A (SSH via port-forward): ${GREEN}PASSED${NC}"
    else
        log_error "  TEST A (SSH via port-forward): ${RED}FAILED${NC}"
    fi

    if $test_b_passed; then
        log_info "  TEST B (SSH via orchard ssh):  ${GREEN}PASSED${NC}"
    else
        log_error "  TEST B (SSH via orchard ssh):  ${RED}FAILED${NC}"
    fi

    if [ $vm_ok -eq 0 ]; then
        log_info "  VM Status Check:               ${GREEN}PASSED${NC}"
    else
        log_error "  VM Status Check:               ${RED}FAILED${NC}"
    fi

    log_info "=========================================="

    if $test_a_passed && $test_b_passed && [ $vm_ok -eq 0 ]; then
        log_info "${GREEN}All tests passed!${NC}"
        exit 0
    else
        log_error "${RED}Some tests failed${NC}"
        exit 1
    fi
}

main "$@"
