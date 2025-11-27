# ====================================================================================
# Setup Project
PROJECT_NAME := provider-orchard
PROJECT_REPO := github.com/ravan/$(PROJECT_NAME)

PLATFORMS ?= linux_amd64 linux_arm64
-include build/makelib/common.mk

# ====================================================================================
# Setup Output

-include build/makelib/output.mk

# ====================================================================================
# Setup Go

NPROCS ?= 1
GO_TEST_PARALLEL := $(shell echo $$(( $(NPROCS) / 2 )))
GO_STATIC_PACKAGES = $(GO_PROJECT)/cmd/provider
GO_LDFLAGS += -X $(GO_PROJECT)/internal/version.Version=$(VERSION)
GO_SUBDIRS += cmd internal apis
GO111MODULE = on
GOLANGCILINT_VERSION = 2.1.2
-include build/makelib/golang.mk

# ====================================================================================
# Setup Kubernetes tools

-include build/makelib/k8s_tools.mk

# ====================================================================================
# Docker Hub Configuration
# IMPORTANT: Must be set BEFORE including imagelight.mk and xpkg.mk
# so the build targets use these values

# Override default registries for Docker Hub publishing
REGISTRY_ORGS := docker.io/ravan
XPKG_REG_ORGS := docker.io/ravan

# CRITICAL: Use Docker Hub as build registry so XPKG embeds pullable image references
BUILD_REGISTRY := docker.io/ravan

# Single architecture build for Linux (providers run in Kubernetes/Linux)
# Note: Even though you're on macOS, the provider runs in Linux containers
PLATFORMS := linux_arm64

# ====================================================================================
# Setup Images

IMAGES = provider-orchard
-include build/makelib/imagelight.mk

# ====================================================================================
# Setup XPKG

# NOTE(hasheddan): skip promoting on xpkg.upbound.io as channel tags are
# inferred.
XPKG_REG_ORGS_NO_PROMOTE ?= xpkg.upbound.io/crossplane
XPKGS = provider-orchard
-include build/makelib/xpkg.mk

# NOTE(hasheddan): we force image building to happen prior to xpkg build so that
# we ensure image is present in daemon.
xpkg.build.provider-orchard: do.build.images

fallthrough: submodules
	@echo Initial setup complete. Running make again . . .
	@make

# integration tests
e2e.run: test-integration

# Run end-to-end integration tests with Orchard
test-integration:
	@$(INFO) running integration tests with Orchard
	@./cluster/local/integration_test.sh || $(FAIL)
	@$(OK) integration tests passed

# Run integration tests and keep environment running
test-integration-dev:
	@$(INFO) running integration tests with Orchard (keeping environment)
	@KEEP_RUNNING=true CLEANUP=false ./cluster/local/integration_test.sh || $(FAIL)
	@$(OK) integration tests passed, environment still running

# Clean up integration test environment
test-integration-clean:
	@$(INFO) cleaning up integration test environment
	@CLUSTER_NAME=orchard-test kind delete cluster --name orchard-test 2>/dev/null || true
	@rm -rf .orchard-test-data
	@$(OK) integration test environment cleaned

# Update the submodules, such as the common build scripts.
submodules:
	@git submodule sync
	@git submodule update --init --recursive

# NOTE(hasheddan): the build submodule currently overrides XDG_CACHE_HOME in
# order to force the Helm 3 to use the .work/helm directory. This causes Go on
# Linux machines to use that directory as the build cache as well. We should
# adjust this behavior in the build submodule because it is also causing Linux
# users to duplicate their build cache, but for now we just make it easier to
# identify its location in CI so that we cache between builds.
go.cachedir:
	@go env GOCACHE

go.mod.cachedir:
	@go env GOMODCACHE

# NOTE(hasheddan): we must ensure up is installed in tool cache prior to build
# as including the k8s_tools machinery prior to the xpkg machinery sets UP to
# point to tool cache.
build.init: $(CROSSPLANE_CLI)

# This is for running out-of-cluster locally, and is for convenience. Running
# this make target will print out the command which was used. For more control,
# try running the binary directly with different arguments.
run: go.build
	@$(INFO) Running Crossplane locally out-of-cluster . . .
	@# To see other arguments that can be provided, run the command with --help instead
	$(GO_OUT_DIR)/provider --debug

dev: $(KIND) $(KUBECTL)
	@$(INFO) Creating kind cluster
	@$(KIND) create cluster --name=$(PROJECT_NAME)-dev
	@$(KUBECTL) cluster-info --context kind-$(PROJECT_NAME)-dev
	@$(INFO) Installing Provider Orchard CRDs
	@$(KUBECTL) apply -R -f package/crds
	@$(INFO) Starting Provider Orchard controllers
	@$(GO) run cmd/provider/main.go --debug

dev-clean: $(KIND) $(KUBECTL)
	@$(INFO) Deleting kind cluster
	@$(KIND) delete cluster --name=$(PROJECT_NAME)-dev

.PHONY: submodules fallthrough test-integration test-integration-dev test-integration-clean run dev dev-clean

# ====================================================================================
# Special Targets

# Install gomplate
GOMPLATE_VERSION := 3.10.0
GOMPLATE := $(TOOLS_HOST_DIR)/gomplate-$(GOMPLATE_VERSION)

$(GOMPLATE):
	@$(INFO) installing gomplate $(SAFEHOSTPLATFORM)
	@mkdir -p $(TOOLS_HOST_DIR)
	@curl -fsSLo $(GOMPLATE) https://github.com/hairyhenderson/gomplate/releases/download/v$(GOMPLATE_VERSION)/gomplate_$(SAFEHOSTPLATFORM) || $(FAIL)
	@chmod +x $(GOMPLATE)
	@$(OK) installing gomplate $(SAFEHOSTPLATFORM)

export GOMPLATE

# This target prepares repo for your provider by replacing all "orchard"
# occurrences with your provider name.
# This target can only be run once, if you want to rerun for some reason,
# consider stashing/resetting your git state.
# Arguments:
#   provider: Camel case name of your provider, e.g. GitHub, PlanetScale
provider.prepare:
	@[ "${provider}" ] || ( echo "argument \"provider\" is not set"; exit 1 )
	@PROVIDER=$(provider) ./hack/helpers/prepare.sh

# This target adds a new api type and its controller.
# You would still need to register new api in "apis/<provider>.go" and
# controller in "internal/controller/<provider>.go".
# Arguments:
#   provider: Camel case name of your provider, e.g. GitHub, PlanetScale
#   group: API group for the type you want to add.
#   kind: Kind of the type you want to add
#	apiversion: API version of the type you want to add. Optional and defaults to "v1alpha1"
provider.addtype: $(GOMPLATE)
	@[ "${provider}" ] || ( echo "argument \"provider\" is not set"; exit 1 )
	@[ "${group}" ] || ( echo "argument \"group\" is not set"; exit 1 )
	@[ "${kind}" ] || ( echo "argument \"kind\" is not set"; exit 1 )
	@PROVIDER=$(provider) GROUP=$(group) KIND=$(kind) APIVERSION=$(apiversion) PROJECT_REPO=$(PROJECT_REPO) ./hack/helpers/addtype.sh

define CROSSPLANE_MAKE_HELP
Crossplane Targets:
    submodules              Update the submodules, such as the common build scripts.
    run                     Run crossplane locally, out-of-cluster. Useful for development.
    test-integration        Run end-to-end integration tests with Orchard (auto cleanup).
    test-integration-dev    Run integration tests and keep environment running for debugging.
    test-integration-clean  Clean up integration test environment (kind cluster and orchard).

endef
# The reason CROSSPLANE_MAKE_HELP is used instead of CROSSPLANE_HELP is because the crossplane
# binary will try to use CROSSPLANE_HELP if it is set, and this is for something different.
export CROSSPLANE_MAKE_HELP

crossplane.help:
	@echo "$$CROSSPLANE_MAKE_HELP"

help-special: crossplane.help

.PHONY: crossplane.help help-special

# ====================================================================================
# Docker Hub Publishing

.PHONY: dockerhub-publish dockerhub-login dockerhub-check dockerhub-dryrun

# Check Docker Hub authentication before publishing
dockerhub-check:
	@$(INFO) Checking Docker Hub authentication
	@test -f ~/.docker/config.json && grep -q "auths" ~/.docker/config.json || (echo "ERROR: Not logged in. Run 'docker login'." && exit 1)
	@$(OK) Docker Hub authentication verified

# Main publishing target - builds and pushes everything to Docker Hub
dockerhub-publish: dockerhub-check
	@$(INFO) Building and publishing to Docker Hub
	@$(MAKE) -j2 build.all
	@$(INFO) Tagging and pushing controller images
	@for platform in $(subst _,/,$(PLATFORMS)); do \
		arch=$$(echo $$platform | cut -d/ -f2); \
		docker tag docker.io/ravan/provider-orchard-$$arch:latest docker.io/ravan/provider-orchard-$$arch:$(VERSION) || $(FAIL); \
		docker push docker.io/ravan/provider-orchard-$$arch:$(VERSION) || $(FAIL); \
	done
	@$(OK) Controller images pushed
	@$(INFO) Pushing Crossplane package
	@$(MAKE) xpkg.release.publish.docker.io/ravan.provider-orchard
	@$(OK) Successfully published to Docker Hub!
	@echo ""
	@echo "Published artifacts:"
	@for platform in $(subst _,/,$(PLATFORMS)); do \
		arch=$$(echo $$platform | cut -d/ -f2); \
		echo "  Controller: docker.io/ravan/provider-orchard-$$arch:$(VERSION)"; \
	done
	@echo "  Package:    docker.io/ravan/provider-orchard:$(VERSION)"
	@echo ""
	@echo "Install with:"
	@echo "  kubectl crossplane install provider docker.io/ravan/provider-orchard:$(VERSION)"

# Dry run - show what would be published without actually doing it
dockerhub-dryrun:
	@echo "Would publish the following to Docker Hub:"
	@echo "  Container: docker.io/ravan/provider-orchard-$(shell uname -m):$(VERSION)"
	@echo "  Package:   docker.io/ravan/provider-orchard:$(VERSION)"
	@echo ""
	@echo "Current VERSION: $(VERSION)"
	@echo "Run 'make dockerhub-publish VERSION=vX.Y.Z' to publish"
