# ====================================================================================
# Setup Project

# Force -mod=readonly into the exported GOFLAGS so no invocation in this
# Makefile's pipeline (including `go tool` targets, which load the full module
# graph) can rewrite go.sum. -mod=readonly is the Go default; forcing it changes
# nothing about a correct environment. The filter-out preserves every unrelated
# ambient flag.
export GOFLAGS := -mod=readonly $(filter-out -mod=%,$(GOFLAGS))
PROJECT_NAME := provider-github
# UP_ORG is the registry org this project is published under -- the org of
# XPKG_REG_ORGS below. It is a recorded fact, not derived from PROJECT_NAME.
UP_ORG ?= atarax
ifeq ($(strip $(UP_ORG)),)
UP_ORG := atarax
endif
PROJECT_REPO := github.com/crossplane/$(PROJECT_NAME)

PLATFORMS ?= linux_amd64 linux_arm64
GOLANGCILINT_VERSION ?= 2.12.2
# VERSION guard: reachability, not just existence, of git tags.
#
# build/makelib/common.mk derives VERSION by testing whether any tag exists in
# the repository at all. A tag that exists but is not an ancestor of HEAD (for
# example a patch tag on a release branch) makes `git describe --tags` fall back
# to a bare commit SHA instead of a semver string, and the xpkg is then cached
# under a name the package manager never looks up.
#
# This guard runs BEFORE common.mk is included and tests reachability instead.
# When a reachable tag is present it sets nothing and common.mk's normal
# derivation proceeds unchanged.
ifeq ($(origin VERSION), undefined)
ifeq ($(shell git describe --tags --abbrev=0 2>/dev/null),)
VERSION := $(shell echo "v0.0.0-$$(git rev-list HEAD --count)-g$$(git describe --dirty --always)" | sed 's/-/./2' | sed 's/-/./2' | sed 's/-/./2')
endif
endif

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
-include build/makelib/golang.mk

# ====================================================================================
# Setup Kubernetes tools

-include build/makelib/k8s_tools.mk

# ====================================================================================
# Setup Images

IMAGES = provider-github
-include build/makelib/imagelight.mk

# ====================================================================================
# Setup XPKG

XPKG_REG_ORGS ?= docker.io/$(UP_ORG)
# NOTE(hasheddan): skip promoting on xpkg.upbound.io as channel tags are
# inferred.
XPKG_REG_ORGS_NO_PROMOTE ?= docker.io/$(UP_ORG)
XPKGS = provider-github
-include build/makelib/xpkg.mk

# ====================================================================================
# Publishing
#
# The crossplane/build xpkg machinery publishes via `crossplane xpkg push`,
# which authenticates through the Docker keychain (~/.docker/config.json). We
# publish with the `up` CLI instead so CI can authenticate with an Upbound API
# token -- the same UP_API_TOKEN / UP_ORG wiring the Upbound configuration
# packages use. Do NOT pass `--create`: it hits the api.upbound.io
# repositories endpoint, and robot tokens 401 there. Registry repositories
# are created out-of-band.
#
# NOTE: the `up login` performed by upbound/action-up is NOT what authenticates
# the push. UP_API_TOKEN is a robot token, and up's RegistryKeychain falls back
# to the Docker keychain for robot tokens ("robot tokens cannot be used for
# registry login 401 Unauthorized"), so CI must additionally `docker login`
# to xpkg.upbound.io with UP_ROBOT_ID as the username. See the "Login to xpkg
# with robot" step in .github/workflows/release.yaml.
UP ?= up

XPKG_PLATFORM_FILES := $(foreach p,$(XPKG_LINUX_PLATFORMS),$(XPKG_OUTPUT_DIR)/$(p)/$(PROJECT_NAME)-$(VERSION).xpkg)

# Converges every built platform's io.crossplane.xpkg:base layer to one
# shared blob. A no-op, byte-for-byte, when every platform already agrees --
# see hack/normalize-xpkg-base-layer.go for why they sometimes don't.
#
# Deliberately NOT declared to depend on `build.all`: the release workflow
# already runs `make build.all` as its own step before `make xpkg.push.up`
# (release.yaml's "Build Artifacts" step), and a Make-level dependency here
# would re-run the entire multi-platform build a second time inside this
# one, undoing that separation for no benefit.
xpkg.normalize.base-layer:
	@$(INFO) Normalizing xpkg base layer across $(words $(XPKG_PLATFORM_FILES)) platform packages
	@go run hack/normalize-xpkg-base-layer.go $(XPKG_PLATFORM_FILES) || $(FAIL)
	@$(OK) xpkg base layer normalized

# Fails the build on divergence instead of letting `up xpkg push` burn an
# immutable tag on a registry rejection. Runs after the normalizer so the
# only way to see FAIL here is the normalizer itself refusing (a real
# content difference between platforms, not the key-order bug).
xpkg.check.base-layer: xpkg.normalize.base-layer
	@$(INFO) Verifying xpkg base layer consistency across platforms
	@hack/check-xpkg-base-consistency.sh local $(XPKG_PLATFORM_FILES) || $(FAIL)
	@$(OK) xpkg base layer consistent

xpkg.push.up: xpkg.check.base-layer ## Push the built xpkg to xpkg.upbound.io using the up CLI.
	@$(INFO) Pushing package $(XPKG_REG_ORGS)/$(PROJECT_NAME):$(VERSION)
	@$(UP) xpkg push \
		$(foreach p,$(XPKG_LINUX_PLATFORMS),-f $(XPKG_OUTPUT_DIR)/$(p)/$(PROJECT_NAME)-$(VERSION).xpkg) \
		$(XPKG_REG_ORGS)/$(PROJECT_NAME):$(VERSION) || $(FAIL)
	@$(OK) Pushed package $(XPKG_REG_ORGS)/$(PROJECT_NAME):$(VERSION)

.PHONY: xpkg.push.up xpkg.check.base-layer xpkg.normalize.base-layer

# ====================================================================================
# Convention checks

check-conventions: ## Detect convention violations (test names, error wrapping, kubectl usage)
	@# PascalCase test names: no underscores after "Test"
	@! grep -rn 'func Test.*_' --include='*_test.go' . 2>/dev/null || \
	  (echo "FAIL: test names must use PascalCase (no underscores)" && exit 1)
	@# KUBECTL env var: test scripts must use $${KUBECTL:-kubectl}, never a bare
	@# `kubectl` invocation. Three passes over each test script keep this to
	@# genuine invocations: comment lines are blanked (not deleted, so line
	@# numbers stay accurate), double-quoted string literals are stripped (so
	@# prose inside a quoted message like `assert_true "... kubectl" "$$rc"`
	@# can't match), and a word boundary is required before `kubectl` (so
	@# `_kubectl` inside a longer identifier, e.g. `__stub_kubectl_empty`,
	@# doesn't match). A `KUBECTL:-kubectl` backstop catches the one shape
	@# quote-stripping and the word boundary both miss: an unquoted
	@# `KUBECTL=$${KUBECTL:-kubectl}` assignment.
	@bad=0; \
	for f in $$(find test/ -name '*.sh' 2>/dev/null); do \
	  m=$$(sed -E 's/^[[:space:]]*#.*$$//' "$$f" | sed -E 's/"[^"]*"//g' \
	    | grep -nE '(^|[^[:alnum:]_$$])kubectl' | grep -v 'KUBECTL:-kubectl'); \
	  [ -z "$$m" ] && continue; \
	  for ln in $$(echo "$$m" | cut -d: -f1); do \
	    echo "$$f:$$ln:$$(sed -n "$${ln}p" "$$f")"; \
	    bad=1; \
	  done; \
	done; \
	if [ "$$bad" -eq 1 ]; then echo "FAIL: use \$${KUBECTL:-kubectl} instead of bare kubectl"; exit 1; fi
	@# Error wrapping: no fmt.Errorf in production code. Comment lines are
	@# filtered out so the controllers' own "never fmt.Errorf" doc comments
	@# do not trip the check that documents them.
	@! grep -rn 'fmt\.Errorf' internal/ --include='*.go' 2>/dev/null \
	    | grep -v '_test.go' \
	    | grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' || \
	  (echo "FAIL: use errors.Wrap/Errorf from crossplane-runtime, not fmt.Errorf" && exit 1)
	@# No bare stdlib "errors" import (match import lines only, not JSON struct tags).
	@! grep -rn '^\s*"errors"\s*$$' internal/ --include='*.go' 2>/dev/null | grep -v '_test.go' || \
	  (echo "FAIL: use crossplane-runtime/v2/pkg/errors, not stdlib \"errors\"" && exit 1)
	@echo "check-conventions: all checks passed"

.PHONY: check-conventions

# check-conventions is not yet a prerequisite of reviewable: non-test code has no
# fmt.Errorf calls or stdlib "errors" imports left, but the test files still
# contain underscore test names (func Test...), which the check flags. Wire it in
# (reviewable: check-conventions) once those are renamed and it passes on a clean
# tree.

# ====================================================================================
# Breaking-change gate
#
# Compares every changed CRD against BASE_REF and fails on an incompatible
# schema change.

BASE_REF ?= origin/main

check-breaking-changes: ## Fail if a changed CRD reshapes a served schema incompatibly
	@BASE_REF=$(BASE_REF) ./hack/check-breaking-changes.sh

.PHONY: check-breaking-changes

# ====================================================================================
# Tool tests
#
# Standalone tool modules (their own go.mod, kept out of GO_SUBDIRS so `make
# test`/`make reviewable` never pull them into the provider's module graph)
# and shell guardrail tests under test/hooks/ and hack/ have no runner
# otherwise. Discovers modules rather than naming them, and skips package-less
# ones -- a stub module holding only go.mod/go.sum makes `go test ./...` exit 1
# ("no packages to test") even though there is nothing to test. Any failing
# iteration fails the target.
test.tools: ## Run unit tests for tools/*/ modules and the shell guardrail tests
	@rc=0; for d in $$(find tools -name go.mod -not -path '*/vendor/*' -exec dirname {} \; 2>/dev/null); do \
		[ -n "$$(cd $$d && go list ./... 2>/dev/null)" ] || continue; \
		$(INFO) go test $$d; \
		(cd $$d && go test ./... -count=1) || rc=1; \
	done; \
	for s in test/hooks/*_test.sh hack/*-test.sh; do \
		[ -e "$$s" ] || continue; \
		$(INFO) running $$s; \
		bash "$$s" || rc=1; \
	done; [ $$rc -eq 0 ] || $(FAIL)
	@$(OK) tool tests passed

reviewable: test.tools

.PHONY: test.tools

# ====================================================================================
# Control plane

CROSSPLANE_VERSION = 2.2.1
CROSSPLANE_CLI_VERSION = v2.2.1
-include build/makelib/controlplane.mk

# NOTE(hasheddan): we force image building to happen prior to xpkg build so that
# we ensure image is present in daemon.
xpkg.build.provider-github: do.build.images

fallthrough: submodules
	@echo Initial setup complete. Running make again . . .
	@make

# integration tests
e2e.run: test-integration

# Run integration tests.
test-integration: $(KIND) $(KUBECTL) $(HELM3)
	@$(INFO) running integration tests using kind $(KIND_VERSION)
	@KIND_NODE_IMAGE_TAG=${KIND_NODE_IMAGE_TAG} $(ROOT_DIR)/cluster/local/integration_tests.sh || $(FAIL)
	@$(OK) integration tests passed

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

# The xpkg build shells out to the Crossplane CLI, so make sure it is installed
# in the tool cache prior to build.
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
	@$(INFO) Installing Provider GitHub CRDs
	@$(KUBECTL) apply -R -f package/crds
	@$(INFO) Starting Provider GitHub controllers
	@$(GO) run cmd/provider/main.go --debug

dev-clean: $(KIND) $(KUBECTL)
	@$(INFO) Deleting kind cluster
	@$(KIND) delete cluster --name=$(PROJECT_NAME)-dev

.PHONY: submodules fallthrough test-integration run dev dev-clean

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
    submodules            Update the submodules, such as the common build scripts.
    run                   Run crossplane locally, out-of-cluster. Useful for development.

endef
# The reason CROSSPLANE_MAKE_HELP is used instead of CROSSPLANE_HELP is because the crossplane
# binary will try to use CROSSPLANE_HELP if it is set, and this is for something different.
export CROSSPLANE_MAKE_HELP

crossplane.help:
	@echo "$$CROSSPLANE_MAKE_HELP"

help-special: crossplane.help

.PHONY: crossplane.help help-special
