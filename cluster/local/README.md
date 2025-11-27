# Integration Tests

This directory contains end-to-end integration tests for the Crossplane Provider for Orchard.

## Prerequisites

The following tools must be installed and available in your PATH:

- [Docker](https://docs.docker.com/get-docker/) - Container runtime
- [kind](https://kind.sigs.k8s.io/) - Kubernetes in Docker
- [kubectl](https://kubernetes.io/docs/tasks/tools/) - Kubernetes CLI
- [helm](https://helm.sh/docs/intro/install/) - Kubernetes package manager
- [orchard](https://github.com/cirruslabs/orchard) - Orchard CLI for macOS VM orchestration

## Running Integration Tests

### Quick Start

Run the full integration test suite with automatic cleanup:

```bash
make test-integration
```

This will:

1. Start Orchard dev environment
2. Create a kind cluster
3. Install Crossplane
4. Build and deploy the provider
5. Run VM lifecycle tests (create, update, delete)
6. Clean up all resources

### Development Mode

Run tests and keep the environment running for debugging:

```bash
make test-integration-dev
```

This keeps the kind cluster and Orchard running so you can:

- Inspect the provider logs: `kubectl logs -n crossplane-system -l app=provider-orchard`
- Check VM status: `kubectl get vm -A`
- View Orchard VMs: `orchard list vms`
- Debug issues manually

When done, clean up with:

```bash
make test-integration-clean
```

### Manual Execution

You can run the test script directly with custom configuration:

```bash
# Run with custom cluster name
CLUSTER_NAME=my-test ./cluster/local/integration_test.sh

# Skip cleanup for debugging
CLEANUP=false ./cluster/local/integration_test.sh

# Keep environment running after tests
KEEP_RUNNING=true ./cluster/local/integration_test.sh

# Use different Orchard port
ORCHARD_PORT=7120 ./cluster/local/integration_test.sh
```

## What Gets Tested

The integration test suite covers:

### 1. VM Creation

- Deploys a VM resource with specified configuration
- Verifies VM is created in Orchard
- Checks Crossplane resource is synced and ready

### 2. VM Updates

- Modifies VM spec (CPU count)
- Verifies provider detects drift
- Confirms update request is sent to Orchard API

### 3. VM Deletion

- Deletes VM resource from Kubernetes
- Verifies VM is removed from Orchard
- Confirms proper cleanup

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLUSTER_NAME` | `orchard-test` | Name of the kind cluster |
| `NAMESPACE` | `crossplane-system` | Kubernetes namespace for provider |
| `PROVIDER_IMAGE` | `localhost:5000/provider-orchard:latest` | Provider container image |
| `ORCHARD_DATA_DIR` | `.orchard-test-data` | Directory for Orchard data |
| `ORCHARD_PORT` | `6120` | Port for Orchard API |
| `CLEANUP` | `true` | Whether to cleanup resources on exit |
| `KEEP_RUNNING` | `false` | Keep environment running after tests |

## Test Output

Successful test run output:

```
[INFO] Starting Crossplane Provider Orchard Integration Tests
[INFO] Checking prerequisites...
[INFO] All prerequisites satisfied
[INFO] Starting orchard dev environment...
[INFO] Orchard started (PID: 12345)
[INFO] Creating kind cluster: orchard-test
...
[INFO] Testing VM creation...
[INFO] VM synced successfully
[INFO] VM exists in Orchard ✓
[INFO] VM creation test passed ✓
[INFO] Testing VM update...
[INFO] VM update test passed ✓
[INFO] Testing VM deletion...
[INFO] VM deleted from Orchard ✓
[INFO] VM deletion test passed ✓

========================================
All integration tests passed! ✓
========================================
```

## Troubleshooting

### Tests Fail to Start

Check that all prerequisites are installed:

```bash
docker version
kind version
kubectl version --client
helm version
orchard --version
```

### Orchard Fails to Start

Check the Orchard logs:

```bash
cat .orchard-test-data/orchard.log
```

Common issues:

- Port 6120 already in use
- Insufficient disk space
- Tart not properly configured (macOS only)

### Provider Pod Crashes

Check provider logs:

```bash
kubectl logs -n crossplane-system -l app=provider-orchard
```

Common issues:

- RBAC permissions missing
- CRDs not installed
- Provider image not loaded into kind

### VM Creation Fails

1. Check Orchard is accessible from within cluster:

   ```bash
   kubectl run -i --rm debug --image=curlimages/curl --restart=Never -- \
     curl -v http://host.docker.internal:6120/v1/
   ```

2. Verify ProviderConfig:

   ```bash
   kubectl get providerconfig default -o yaml
   ```

3. Check provider logs for connection errors

### Cleanup Issues

If cleanup fails, manually remove resources:

```bash
# Delete kind cluster
kind delete cluster --name orchard-test

# Stop Orchard
pkill -f "orchard dev"

# Remove data directory
rm -rf .orchard-test-data
```

## CI/CD Integration

The integration tests are designed to run in CI/CD pipelines. Example GitHub Actions workflow:

```yaml
name: Integration Tests
on: [push, pull_request]

jobs:
  integration:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      - name: Install dependencies
        run: |
          brew install kind kubectl helm
          brew install cirruslabs/cli/orchard

      - name: Run integration tests
        run: make test-integration
```

## Development Tips

1. **Faster iterations**: Use `test-integration-dev` to keep the environment running between test runs
2. **Debug specific tests**: Comment out tests in the script you don't need
3. **Custom configurations**: Set environment variables to match your local setup
4. **Logs**: Provider logs are the best source of debugging information
5. **Orchard CLI**: Use `orchard` commands to inspect VMs directly

## References

- [Crossplane Documentation](https://docs.crossplane.io/)
- [Orchard Documentation](https://github.com/cirruslabs/orchard)
- [kind Documentation](https://kind.sigs.k8s.io/)
