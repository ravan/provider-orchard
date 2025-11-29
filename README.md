# Crossplane Provider for Orchard

A [Crossplane](https://crossplane.io/) provider for managing [Orchard](https://github.com/cirruslabs/orchard) VMs. Orchard orchestrates macOS virtual machines (Tart VMs) with cloud-init style provisioning.

## Features

- Declarative VM lifecycle management (create, update, delete)
- Kubernetes-native API via Custom Resource Definitions
- Bearer token authentication with configurable Orchard API endpoint
- Support for VM configuration including:
  - Compute resources (CPU, memory, disk)
  - Startup scripts with environment variables
  - Network configuration (bridged, softnet with allow/block lists)
  - Host directory mounts
  - Worker node selection via labels

## Prerequisites

- Kubernetes cluster with Crossplane installed
- Orchard controller running and accessible
- Orchard service account token for authentication

## Installation

Install the provider from Docker Hub:

```bash
kubectl crossplane install provider docker.io/ravan/provider-orchard:v0.1.0
```

Wait for the provider to become healthy:

```bash
kubectl get providers
kubectl get pods -n crossplane-system
```

**Authentication**: Create a Kubernetes Secret with your Orchard token in JSON format:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: orchard-credentials
  namespace: default
type: Opaque
stringData:
  credentials: |
    {
      "token": "orchard-bootstrap-token-v0...."
    }
```

See `examples/provider/config.yaml` for complete ProviderConfig and ClusterProviderConfig examples.

## Usage

### Basic VM

```yaml
apiVersion: compute.orchard.crossplane.io/v1alpha1
kind: VM
metadata:
  name: my-vm
spec:
  forProvider:
    image: ghcr.io/cirruslabs/macos-sonoma-vanilla:latest
    cpu: 4
    memory: 8192
  providerConfigRef:
    name: default
```

### VM with Startup Script

```yaml
apiVersion: compute.orchard.crossplane.io/v1alpha1
kind: VM
metadata:
  name: my-vm
spec:
  forProvider:
    image: ghcr.io/cirruslabs/macos-sonoma-vanilla:latest
    cpu: 4
    memory: 8192
    diskSize: 50
    startupScript:
      scriptContent: |
        #!/bin/zsh
        echo "VM provisioned at $(date)"
      env:
        ENVIRONMENT: "production"
  providerConfigRef:
    name: default
```

**Key Parameters**:

- `image` - VM image reference (required)
- `cpu` - CPU cores (default: from image)
- `memory` - Memory in MB (default: from image)
- `diskSize` - Disk size in GB (default: from image)
- `startupScript` - Initialization script with environment variables

See `examples/compute/vm.yaml` for advanced configuration including networking, host directory mounts, and worker selection.

## Available Resources

### VM (`compute.orchard.crossplane.io/v1alpha1`)

Manages Orchard virtual machines.

**Spec Parameters**:

- **Required**:
  - `image` - Container image reference for the VM
- **Compute Resources**:
  - `cpu` - Number of CPU cores
  - `memory` - Memory in MB
  - `diskSize` - Disk size in GB
- **Startup Configuration**:
  - `startupScript.scriptContent` - Shell script to run on startup
  - `startupScript.env` - Environment variables for the script
- **SSH Credentials**:
  - `username` - SSH username
  - `password` - SSH password
- **Network**:
  - `netBridged` - Bridged network interface
  - `netSoftnet` - Enable softnet networking
  - `netSoftnetAllow` - CIDR list for allowed networks
  - `netSoftnetBlock` - CIDR list for blocked networks
- **Storage**:
  - `hostDirs` - Host directories to mount (with readOnly flag)
- **Worker Selection**:
  - `labels` - Worker node labels for scheduling
  - `resources` - Resource requirements for worker selection
- **Advanced**:
  - `nested` - Enable nested virtualization
  - `suspendable` - Allow VM suspension
  - `headless` - Run in headless mode
  - `imagePullPolicy` - Image pull policy (Always, IfNotPresent, Never)
  - `restartPolicy` - Restart policy (Always, OnFailure, Never)

**Status Fields**:

- `status` - VM status (pending, running, failed, etc.)
- `statusMessage` - Detailed status message
- `worker` - Assigned worker node name
- `generation` / `observedGeneration` - Spec change tracking

### ProviderConfig (`orchard.crossplane.io/v1alpha1`)

Configures authentication and connection to Orchard API (namespaced).

**Spec**:

- `baseURL` - Orchard API endpoint (default: `http://localhost:6120`)
- `credentials.source` - Credential source: `Secret`, `Environment`, `Filesystem`, `InjectedIdentity`
- `credentials.secretRef` - Reference to Secret containing credentials

### ClusterProviderConfig (`orchard.crossplane.io/v1alpha1`)

Cluster-scoped version of ProviderConfig with identical spec.

## Publishing

For maintainers publishing new versions to Docker Hub:

### Prerequisites

- Docker Hub account with access to ravan/provider-orchard
- Logged in: `docker login`

### Publishing Process

1. Check what will be published:

   ```bash
   make dockerhub-dryrun
   ```

2. Build and publish (version auto-detected from git tags):

   ```bash
   make dockerhub-publish
   ```

   Or specify version explicitly:

   ```bash
   make dockerhub-publish VERSION=v0.2.0
   ```

3. Verify the published artifacts:

   ```bash
   # Pull container image
   docker pull docker.io/ravan/provider-orchard-arm64:v0.2.0

   # Test installation
   kubectl crossplane install provider docker.io/ravan/provider-orchard:v0.2.0
   kubectl get providers
   ```

**Note:** Currently builds for single architecture (arm64 on M-series Macs). For multi-arch support, use Docker buildx with QEMU or build on multiple machines.

## Development

### Building from Source

```bash
# Initialize build submodule
make submodules

# Build provider binary
make build

# Run unit tests
make test

# Run linters, tests, and code generation
make reviewable
```

### Local Testing with Kind

```bash
# Build container image
make docker-build

# Load image into Kind cluster
kind load docker-image provider-orchard:latest

# Apply examples
kubectl apply -f examples/
```

### Running with Local Orchard

1. Start Orchard development server:

   ```bash
   orchard dev
   ```

2. Create a service account and obtain token:

   ```bash
   orchard create service-account crossplane-provider
   orchard get service-account crossplane-provider
   ```

3. Configure ProviderConfig with local Orchard endpoint:

   ```yaml
   spec:
     baseURL: "http://host.docker.internal:6120/v1"  # For Docker Desktop
     # or
     baseURL: "http://localhost:6120/v1"              # For host network
   ```

## Testing

Unit tests cover the complete reconciliation lifecycle:

- **Connect**: ProviderConfig retrieval and credential extraction
- **Observe**: VM status fetching and spec comparison
- **Create**: VM creation with conflict handling
- **Update**: VM spec updates
- **Delete**: VM deletion and cleanup

Run tests:

```bash
make test
```

Test files: `internal/controller/vm/vm_test.go`

## Architecture

### Components

- **VM Controller** (`internal/controller/vm/`): Reconciles VM resources against Orchard API
- **Config Controller** (`internal/controller/config/`): Manages ProviderConfig resources
- **Orchard Client** (`internal/clients/orchard/`): Auto-generated API client from OpenAPI spec (oapi-codegen)
- **Authentication**: Bearer token-based authentication with configurable base URL

### Reconciliation Flow

1. **Connect**: Extract credentials from Kubernetes Secret, parse bearer token, create authenticated Orchard client
2. **Observe**: Fetch VM status from Orchard API, compare desired vs actual state
3. **Create/Update**: Synchronize Kubernetes spec to Orchard via POST/PUT
4. **Delete**: Remove VM from Orchard via DELETE

The controller uses Crossplane's managed resource reconciler pattern with external client interface.

## Contributing

Refer to Crossplane's [CONTRIBUTING.md](https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md) for general contribution guidelines. The [Provider Development Guide](https://github.com/crossplane/crossplane/blob/master/contributing/guide-provider-development.md) provides additional context on Crossplane provider architecture.

## License

Apache-2.0

## References

- [Orchard Project](https://github.com/cirruslabs/orchard)
- [Crossplane Documentation](https://docs.crossplane.io/)
- [Provider Development Guide](https://github.com/crossplane/crossplane/blob/master/contributing/guide-provider-development.md)
