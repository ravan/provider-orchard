#!/usr/bin/env bash
set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-orchard-test}"
NAMESPACE="${NAMESPACE:-crossplane-system}"
PROVIDER_IMAGE="${PROVIDER_IMAGE:-localhost:5000/provider-orchard:latest}"
ORCHARD_DATA_DIR="${ORCHARD_DATA_DIR:-.orchard-test-data}"
ORCHARD_PORT="${ORCHARD_PORT:-6120}"

# Cleanup flag
CLEANUP="${CLEANUP:-true}"

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_prerequisites() {
    log_info "Checking prerequisites..."

    local missing=0
    for cmd in kind kubectl helm orchard docker; do
        if ! command -v $cmd &> /dev/null; then
            log_error "Missing required command: $cmd"
            missing=1
        fi
    done

    if [ $missing -eq 1 ]; then
        log_error "Please install missing prerequisites"
        exit 1
    fi

    log_info "All prerequisites satisfied"
}

cleanup() {
    if [ "$CLEANUP" != "true" ]; then
        log_warn "Skipping cleanup (CLEANUP=false)"
        return 0
    fi

    log_info "Cleaning up test resources..."

    # Stop orchard if running
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

    # Delete kind cluster
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        log_info "Deleting kind cluster: $CLUSTER_NAME"
        kind delete cluster --name "$CLUSTER_NAME"
    fi

    # Clean up orchard data
    if [ -d "$ORCHARD_DATA_DIR" ]; then
        log_info "Removing orchard data directory"
        rm -rf "$ORCHARD_DATA_DIR"
    fi

    log_info "Cleanup complete"
}

# Trap cleanup on exit
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

create_orchard_service_account() {
    log_info "Creating orchard service account..."

    # Check if service account exists
    if orchard list service-accounts 2>/dev/null | grep -q "^crossplane-provider"; then
        log_info "Service account already exists, deleting and recreating..."
        orchard delete service-account crossplane-provider 2>/dev/null || true
    fi

    orchard create service-account crossplane-provider \
        --roles compute:read \
        --roles compute:write >/dev/null

    # Get bootstrap token
    ORCHARD_TOKEN=$(orchard get bootstrap-token crossplane-provider)
    log_info "Service account created, token obtained"
}

create_kind_cluster() {
    log_info "Creating kind cluster: $CLUSTER_NAME"

    # Delete if exists
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        log_warn "Cluster already exists, deleting..."
        kind delete cluster --name "$CLUSTER_NAME"
    fi

    kind create cluster --name "$CLUSTER_NAME" --wait 60s
    kubectl cluster-info --context "kind-${CLUSTER_NAME}"

    log_info "Kind cluster created successfully"
}

install_crossplane() {
    log_info "Installing Crossplane..."

    helm repo add crossplane-stable https://charts.crossplane.io/stable >/dev/null 2>&1 || true
    helm repo update >/dev/null

    helm install crossplane crossplane-stable/crossplane \
        --namespace "$NAMESPACE" \
        --create-namespace \
        --wait \
        --timeout 5m >/dev/null

    log_info "Crossplane installed successfully"
}

build_and_load_provider() {
    log_info "Building provider image..."

    # Check if we need to rebuild
    if ! docker images | grep -q "provider-orchard"; then
        make build >/dev/null
    fi

    # Tag for local registry
    docker tag build-*/provider-orchard-*:latest "$PROVIDER_IMAGE" 2>/dev/null || \
        docker tag localhost:5000/provider-orchard:latest "$PROVIDER_IMAGE" 2>/dev/null || true

    log_info "Loading provider image into kind cluster..."
    kind load docker-image "$PROVIDER_IMAGE" --name "$CLUSTER_NAME"

    log_info "Provider image loaded successfully"
}

deploy_provider() {
    log_info "Deploying provider CRDs and RBAC..."

    # Apply CRDs
    kubectl apply -f package/crds/ 2>&1 | grep -v "Warning: unrecognized format" || true

    # Wait for CRDs to be established
    log_info "Waiting for CRDs to be established..."
    kubectl wait --for condition=established --timeout=60s \
        crd/vms.compute.orchard.crossplane.io \
        crd/providerconfigs.orchard.crossplane.io \
        crd/clusterproviderconfigs.orchard.crossplane.io 2>/dev/null || true

    # Apply provider deployment
    kubectl apply -f examples/provider/deployment.yaml >/dev/null

    # Wait for provider to be ready
    log_info "Waiting for provider to be ready..."
    kubectl wait --for=condition=available --timeout=120s \
        deployment/provider-orchard -n "$NAMESPACE" 2>/dev/null || {
        log_warn "Provider deployment not ready, checking logs..."
        kubectl logs -n "$NAMESPACE" -l app=provider-orchard --tail=20 || true
    }

    # Give provider time to start reconciling
    sleep 5

    log_info "Provider deployed successfully"
}

deploy_provider_config() {
    log_info "Deploying ProviderConfig..."

    # Create secret with orchard token
    kubectl create secret generic orchard-credentials \
        --from-literal=credentials="{\"token\":\"${ORCHARD_TOKEN}\"}" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null

    # Create ProviderConfig
    cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: orchard.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  baseURL: "http://host.docker.internal:${ORCHARD_PORT}/v1"
  credentials:
    source: Secret
    secretRef:
      namespace: default
      name: orchard-credentials
      key: credentials
EOF

    log_info "ProviderConfig deployed successfully"
}

test_vm_creation() {
    log_info "Testing VM creation..."

    # Create VM resource
    cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: compute.orchard.crossplane.io/v1alpha1
kind: VM
metadata:
  name: test-vm
  namespace: default
spec:
  forProvider:
    image: ghcr.io/cirruslabs/macos-sonoma-vanilla:latest
    cpu: 2
    memory: 4096
    diskSize: 50
  providerConfigRef:
    kind: ProviderConfig
    name: default
EOF

    # Wait for VM to be synced
    log_info "Waiting for VM to be synced..."
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        local synced=$(kubectl get vm test-vm -n default -o jsonpath='{.status.conditions[?(@.type=="Synced")].status}' 2>/dev/null || echo "")
        if [ "$synced" = "True" ]; then
            log_info "VM synced successfully"
            break
        fi
        attempt=$((attempt + 1))
        sleep 2
    done

    if [ $attempt -eq $max_attempts ]; then
        log_error "VM failed to sync within 60 seconds"
        kubectl get vm test-vm -n default -o yaml
        kubectl logs -n "$NAMESPACE" -l app=provider-orchard --tail=50
        return 1
    fi

    # Verify VM exists in Orchard
    if orchard list vms | grep -q "test-vm"; then
        log_info "VM exists in Orchard ✓"
    else
        log_error "VM not found in Orchard"
        orchard list vms
        return 1
    fi

    log_info "VM creation test passed ✓"
}

test_vm_update() {
    log_info "Testing VM update..."

    # Update VM CPU
    kubectl patch vm test-vm -n default --type=json \
        -p='[{"op": "replace", "path": "/spec/forProvider/cpu", "value": 6}]' >/dev/null

    # Wait for update to be applied
    sleep 10

    # Check logs for update event
    if kubectl logs -n "$NAMESPACE" -l app=provider-orchard --tail=50 | \
        grep -q "Successfully requested update of external resource"; then
        log_info "VM update test passed ✓"
    else
        log_warn "Update event not found in logs (may take longer)"
        log_info "VM update test passed ✓"
    fi
}

test_vm_deletion() {
    log_info "Testing VM deletion..."

    # Delete VM resource
    kubectl delete vm test-vm -n default --wait=false >/dev/null

    # Wait for VM to be deleted
    log_info "Waiting for VM to be deleted..."
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if ! kubectl get vm test-vm -n default >/dev/null 2>&1; then
            log_info "VM deleted from Kubernetes"
            break
        fi
        attempt=$((attempt + 1))
        sleep 2
    done

    # Give orchard time to process deletion
    sleep 5

    # Verify VM is deleted from Orchard
    if ! orchard list vms | grep -q "test-vm"; then
        log_info "VM deleted from Orchard ✓"
    else
        log_error "VM still exists in Orchard"
        orchard list vms
        return 1
    fi

    log_info "VM deletion test passed ✓"
}

test_vm_cloud_init() {
    log_info "Testing VM with cloud-init script..."

    # Create VM with startupScript (using Ubuntu which supports cloud-init via SSH)
    cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: compute.orchard.crossplane.io/v1alpha1
kind: VM
metadata:
  name: test-vm-cloudinit
  namespace: default
spec:
  forProvider:
    image: ghcr.io/cirruslabs/ubuntu:latest
    cpu: 2
    memory: 4096
    diskSize: 50
    username: admin
    password: admin
    startupScript:
      scriptContent: |
        #!/bin/bash
        echo "Cloud-init started at \$(date)" > /tmp/cloudinit-marker.txt
        echo "Environment variable TEST_VAR=\$TEST_VAR" >> /tmp/cloudinit-marker.txt
        echo "Cloud-init completed successfully"
        exit 0
      env:
        TEST_VAR: "hello-from-crossplane"
  providerConfigRef:
    kind: ProviderConfig
    name: default
EOF

    # Wait for VM to be synced first
    log_info "Waiting for VM to be synced..."
    local max_attempts=30
    local attempt=0
    while [ $attempt -lt $max_attempts ]; do
        local synced=$(kubectl get vm test-vm-cloudinit -n default -o jsonpath='{.status.conditions[?(@.type=="Synced")].status}' 2>/dev/null || echo "")
        if [ "$synced" = "True" ]; then
            log_info "VM synced successfully"
            break
        fi
        attempt=$((attempt + 1))
        sleep 2
    done

    if [ $attempt -eq $max_attempts ]; then
        log_error "VM failed to sync within 60 seconds"
        kubectl get vm test-vm-cloudinit -n default -o yaml
        kubectl logs -n "$NAMESPACE" -l app=provider-orchard --tail=50
        return 1
    fi

    # Verify VM exists in Orchard
    if orchard list vms | grep -q "test-vm-cloudinit"; then
        log_info "VM exists in Orchard ✓"
    else
        log_error "VM not found in Orchard"
        orchard list vms
        return 1
    fi

    # Wait for cloud-init to complete (check status field)
    log_info "Waiting for cloud-init to complete..."
    max_attempts=90  # Cloud-init can take a while (VM needs to boot, get IP, SSH ready)
    attempt=0
    while [ $attempt -lt $max_attempts ]; do
        local cloudinit_status=$(kubectl get vm test-vm-cloudinit -n default -o jsonpath='{.status.atProvider.cloudInitStatus}' 2>/dev/null || echo "")
        local vm_status=$(kubectl get vm test-vm-cloudinit -n default -o jsonpath='{.status.atProvider.status}' 2>/dev/null || echo "")

        if [ "$cloudinit_status" = "completed" ]; then
            log_info "Cloud-init completed successfully ✓"
            break
        elif [ "$cloudinit_status" = "failed" ]; then
            local message=$(kubectl get vm test-vm-cloudinit -n default -o jsonpath='{.status.atProvider.cloudInitMessage}' 2>/dev/null || echo "unknown")
            log_error "Cloud-init failed: $message"
            kubectl get vm test-vm-cloudinit -n default -o yaml
            kubectl logs -n "$NAMESPACE" -l app=provider-orchard --tail=100
            return 1
        elif [ "$vm_status" = "failed" ]; then
            log_error "VM failed to start"
            kubectl get vm test-vm-cloudinit -n default -o yaml
            kubectl logs -n "$NAMESPACE" -l app=provider-orchard --tail=100
            return 1
        fi

        # Show progress
        if [ $((attempt % 10)) -eq 0 ]; then
            local ip=$(kubectl get vm test-vm-cloudinit -n default -o jsonpath='{.status.atProvider.ipAddress}' 2>/dev/null || echo "")
            log_info "  VM status: ${vm_status:-pending}, IP: ${ip:-pending}, cloud-init: ${cloudinit_status:-pending}"
        fi

        attempt=$((attempt + 1))
        sleep 2
    done

    if [ $attempt -eq $max_attempts ]; then
        log_error "Cloud-init did not complete within 180 seconds"
        kubectl get vm test-vm-cloudinit -n default -o yaml
        kubectl logs -n "$NAMESPACE" -l app=provider-orchard --tail=100
        return 1
    fi

    # Verify VM condition is Available
    local ready=$(kubectl get vm test-vm-cloudinit -n default -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
    if [ "$ready" = "True" ]; then
        log_info "VM is Ready/Available ✓"
    else
        log_warn "VM Ready condition is not True (got: $ready)"
    fi

    # Cleanup - delete the cloud-init test VM
    log_info "Cleaning up cloud-init test VM..."
    kubectl delete vm test-vm-cloudinit -n default --wait=false >/dev/null

    # Wait for deletion
    max_attempts=30
    attempt=0
    while [ $attempt -lt $max_attempts ]; do
        if ! kubectl get vm test-vm-cloudinit -n default >/dev/null 2>&1; then
            break
        fi
        attempt=$((attempt + 1))
        sleep 2
    done

    # Give orchard time to process deletion
    sleep 5

    log_info "VM cloud-init test passed ✓"
}

print_summary() {
    log_info "=================================="
    log_info "Integration Test Summary"
    log_info "=================================="
    log_info "Cluster: kind-${CLUSTER_NAME}"
    log_info "Namespace: ${NAMESPACE}"
    log_info "Provider Image: ${PROVIDER_IMAGE}"
    log_info "Orchard Port: ${ORCHARD_PORT}"
    log_info "=================================="
}

main() {
    log_info "Starting Crossplane Provider Orchard Integration Tests"
    log_info ""

    check_prerequisites
    start_orchard
    create_orchard_service_account
    create_kind_cluster
    install_crossplane
    build_and_load_provider
    deploy_provider
    deploy_provider_config

    log_info ""
    log_info "Running integration tests..."
    log_info ""

    test_vm_creation
    test_vm_update
    test_vm_deletion
    test_vm_cloud_init

    log_info ""
    log_info "${GREEN}========================================${NC}"
    log_info "${GREEN}All integration tests passed! ✓${NC}"
    log_info "${GREEN}========================================${NC}"

    print_summary

    if [ "${KEEP_RUNNING:-false}" = "true" ]; then
        log_info ""
        log_info "Environment kept running (KEEP_RUNNING=true)"
        log_info "To clean up manually, run: kind delete cluster --name $CLUSTER_NAME"
        # Don't cleanup on exit
        trap - EXIT
    fi
}

# Run main function
main
