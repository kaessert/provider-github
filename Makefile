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

# Source .env credentials for E2E tests if present (optional fallback).
# Primary credential source: environment variables / CI secrets.
# .env format is KEY=VALUE (no `export` prefix) -- valid GNU Make syntax.
# Note: ROOT_DIR is not yet set (common.mk sets it), so we use CURDIR.
ifneq (,$(wildcard $(CURDIR)/../.env))
  include $(CURDIR)/../.env
  export
endif
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
# Registration
#
# Regenerates apis/zz_generated_register.go (scheme registration) and
# internal/controller/zz_generated_register.go (controller registration and the
# CRD-gated variant) from the directory structure. Both files are committed.

generate-registration: ## Regenerate the scheme and controller registration files
	@$(INFO) generating registration files
	@go run hack/generate-registration.go $(PROJECT_REPO) || $(FAIL)
	@$(OK) generating registration files

# Wire generate-registration into the generate chain so it runs alongside
# go.generate (controller-gen deepcopy + CRD generation).
generate.run: generate-registration

.PHONY: generate-registration

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

reviewable: check-breaking-changes

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
-include build/makelib/local.xpkg.mk
-include build/makelib/controlplane.mk

# NOTE(hasheddan): we force image building to happen prior to xpkg build so that
# we ensure image is present in daemon.
xpkg.build.provider-github: do.build.images

# ====================================================================================
# End to End Testing

# Setup Uptest
#
# uptest fork -- carries --post-assert-script (upstream crossplane/uptest#65,
# unreleased) plus two merged-but-unreleased upstream fixes: `crossplane beta
# trace -n <namespace>` for dual-scope managed resources (#52) and
# --skip-webhook-check, which drops two remote fetches and two hard 10s sleeps
# per invocation for a provider with no conversion webhooks (#60).
#
# k8s_tools.mk's own $(TOOLS_HOST_DIR)/uptest-$(UPTEST_VERSION) recipe stays
# untouched -- overriding UPTEST_VERSION alone 404s, because its download rule
# hardcodes the upstream org. This block instead points UPTEST at a NEW filename
# under the same $(TOOLS_HOST_DIR) and supplies its own download recipe for that
# filename, so the two never collide on one make target. $(TOOLS_HOST_DIR) can be
# hardlinked from a shared cross-worktree tool cache, so reusing the stock
# uptest-$(UPTEST_VERSION) path here would swap the binary under another
# worktree's concurrently-running E2E.
#
# Retirement: delete this block and revert to plain UPTEST_VERSION the moment
# upstream ships a release containing #52, #60 and #65.
UPTEST_FORK_REPO := kaessert/uptest
UPTEST_FORK_REF  := v2.3.0-fork.e21896e
UPTEST           := $(TOOLS_HOST_DIR)/uptest-fork-$(UPTEST_FORK_REF)

$(UPTEST):
	@$(INFO) installing uptest fork $(UPTEST_FORK_REF)
	@mkdir -p $(TOOLS_HOST_DIR)
	@curl -fsSLo $(UPTEST) https://github.com/$(UPTEST_FORK_REPO)/releases/download/$(UPTEST_FORK_REF)/uptest_$(SAFEHOSTPLATFORM) || $(FAIL)
	@chmod +x $(UPTEST)
	@$(OK) installing uptest fork $(UPTEST_FORK_REF)

UPTEST_LOCAL_DEPLOY_TARGET = local-deploy
# uptest's built-in default process budget is 1200s, which is shorter than a
# single multi-resource apply stage once post-assert update-test hooks run.
UPTEST_DEFAULT_TIMEOUT ?= 3600s

# The GitHub organization every example manages. It is a recorded fact about the
# test environment, not something derived from the credentials. The examples
# name this organization literally, so changing it means changing them too;
# e2e-preflight fails when the two disagree.
E2E_ORG ?= pgh-test
export E2E_ORG

# ----------------------------------------------------------------------------------
# E2E poll interval -- single source of truth
#
# The post-assert converge/run checks wait a multiple of the provider's --poll
# interval to prove the controller has stopped reconciling, not just that it has
# not ticked yet. At the production default (--poll=1m) those waits dominate E2E
# wall-clock time. This variable lowers BOTH sides of that wait -- the E2E
# controlplane's --poll flag AND the update-tester wait windows -- from one
# place, so neither can change without the other following. Lowering only the
# tester side would shrink its drift-detection sleep below a real reconcile cycle
# and silently stop catching the reconcile-loop bug class it exists to catch.
#
# `?=` keeps this overridable per invocation (e.g. `make e2e.repository
# E2E_POLL_INTERVAL=30s`).
E2E_POLL_INTERVAL ?= 10s

# Rendered E2E-only DeploymentRuntimeConfig. test/e2e/deployment-runtime-config.yaml
# is the checked-in template (placeholder: __E2E_POLL_INTERVAL__); the e2e-drc
# target below substitutes the current E2E_POLL_INTERVAL and writes the result
# here. build/makelib/local.xpkg.mk's local.xpkg.deploy.provider.% rule applies
# $(DRC_FILE) instead of its own built-in default, but only on the
# controlplane.up path (local-deploy, below), which `make e2e` and
# `make e2e.<resource>` reach. It never touches production packaging.
DRC_FILE := $(CACHE_DIR)/e2e-deployment-runtime-config.yaml

# Reaches update-tester's `converge` (drift-detection sleep = 1.5x this value)
# and `run` (slow-observe bar = 0.5x this value) steps via
# test/hooks/run-update-tester.sh and test/hooks/converge-barrier.sh, both of
# which read it from the environment.
#
# E2E_POLL_INTERVAL is the ONLY supported knob for this pairing: `override`
# makes a command-line or environment attempt to set UPDATE_TESTER_POLL_INTERVAL
# directly be ignored, which would otherwise unpair the two. The assignment MUST
# stay recursive (`=`, not `:=`) so a target-specific E2E_POLL_INTERVAL reaches
# both knobs.
export override UPDATE_TESTER_POLL_INTERVAL = $(E2E_POLL_INTERVAL)

# Bounds the update-tester's per-field poll and its converge pre-check. At file
# scope so the default `make e2e` goal, which has no per-resource target in
# scope, gets it too. `?=` keeps a caller's own value winning.
UPDATE_TESTER_TIMEOUT ?= 300
export UPDATE_TESTER_TIMEOUT

# Renders test/e2e/deployment-runtime-config.yaml's placeholder into $(DRC_FILE).
#
# This cannot be wired as a prerequisite of local.xpkg.deploy.provider.%: GNU
# Make does not merge a second pattern rule's prerequisites onto a stem that
# already matched a pattern rule with a recipe. It is listed FIRST in the same
# rule statement as local.xpkg.deploy.provider.$(PROJECT_NAME) on the
# `local-deploy:` line below instead; in serial (non -j) mode one rule's
# prerequisites build strictly left to right, so the render finishes before the
# deploy recipe reads $(DRC_FILE). E2E is not invoked with -j.
#
# $(YQ) is an explicit prerequisite because the deploy recipe reads it but, as a
# pattern rule, cannot pick up a prerequisite that downloads it.
.PHONY: e2e-drc
e2e-drc: $(YQ)
	@mkdir -p $(CACHE_DIR)
	@sed -e 's/--poll=__E2E_POLL_INTERVAL__/--poll=$(E2E_POLL_INTERVAL)/' \
	     test/e2e/deployment-runtime-config.yaml > $(DRC_FILE)

# local-deploy: load the provider image into the kind cluster and deploy the
# xpkg. Called by `make e2e` via UPTEST_LOCAL_DEPLOY_TARGET and by CI
# (`make controlplane.up local-deploy`). e2e-drc MUST stay first in this list.
.PHONY: local-deploy
local-deploy: e2e-drc local.xpkg.deploy.provider.$(PROJECT_NAME)

# Helper variables for comma-separated manifest lists (uptest CLI convention).
# uptest expects a single comma-separated string, not space-separated.
comma := ,
empty :=
space := $(empty) $(empty)

# Per-resource manifest variables. A resource whose cluster and namespaced MRs
# address distinct external objects lists BOTH scope variants as a comma pair, so
# one uptest pass gates cluster and namespaced together. Every example uses its
# own external name so the two scopes can run at once.
#
# Sorted alphabetically by resource name so a new resource's addition lands at a
# distinct diff hunk instead of colliding with a concurrent addition.
#
# ActionsSecretAccess manages access to an organization Actions secret that must
# already exist; test/setup.sh creates a disposable one (see E2E_ORG_SECRETS below).
UPTEST_MANIFESTS_ACTIONS_SECRET_ACCESS := examples/actions-secret-access/actions-secret-access.yaml,examples/actions-secret-access/actions-secret-access-namespaced.yaml
# DependabotSecretAccess manages access to an organization Dependabot secret that
# must already exist; test/setup.sh creates a disposable one (see E2E_ORG_SECRETS below).
UPTEST_MANIFESTS_DEPENDABOT_SECRET_ACCESS := examples/dependabot-secret-access/dependabot-secret-access.yaml,examples/dependabot-secret-access/dependabot-secret-access-namespaced.yaml
# GATED: Membership invites a real GitHub user to the organization, which the
# credentials alone cannot do for a throwaway account.
UPTEST_MANIFESTS_MEMBERSHIP := examples/membership/membership.yaml,examples/membership/membership-namespaced.yaml
# GATED: Organization adopts the shared test organization, which the provider can
# neither create nor delete. Both scopes manage that one organization, so they run
# as two sequential passes (the _NS variable) rather than a comma pair.
UPTEST_MANIFESTS_ORGANIZATION := examples/organization/organization.yaml
UPTEST_MANIFESTS_ORGANIZATION_NS := examples/organization/organization-namespaced.yaml
UPTEST_MANIFESTS_ORGANIZATION_VARIABLE := examples/organization-variable/organization-variable.yaml,examples/organization-variable/organization-variable-namespaced.yaml
UPTEST_MANIFESTS_ORGANIZATION_WEBHOOK := examples/organization-webhook/organization-webhook.yaml,examples/organization-webhook/organization-webhook-namespaced.yaml
UPTEST_MANIFESTS_REPOSITORY := examples/repository/repository.yaml,examples/repository/repository-namespaced.yaml
UPTEST_MANIFESTS_RUNNER_GROUP := examples/runner-group/runner-group.yaml,examples/runner-group/runner-group-namespaced.yaml
UPTEST_MANIFESTS_TEAM := examples/team/team.yaml,examples/team/team-namespaced.yaml

# E2E manifest tiers
# CORE: resources that need nothing beyond the GitHub App credentials and the test organization.
UPTEST_MANIFESTS_CORE = $(UPTEST_MANIFESTS_ACTIONS_SECRET_ACCESS),$(UPTEST_MANIFESTS_DEPENDABOT_SECRET_ACCESS),$(UPTEST_MANIFESTS_ORGANIZATION_VARIABLE),$(UPTEST_MANIFESTS_ORGANIZATION_WEBHOOK),$(UPTEST_MANIFESTS_REPOSITORY),$(UPTEST_MANIFESTS_RUNNER_GROUP),$(UPTEST_MANIFESTS_TEAM)
# GATED: resources that need an out-of-band prerequisite (an inviteable user, the
# shared organization itself).
UPTEST_MANIFESTS_GATED = $(UPTEST_MANIFESTS_MEMBERSHIP),$(UPTEST_MANIFESTS_ORGANIZATION),$(UPTEST_MANIFESTS_ORGANIZATION_NS)

# ALL: every resource example, discovered via wildcard (excludes examples/provider/).
# Diagnostic only, never a gate.
_UPTEST_MANIFESTS_ALL_RAW := $(filter-out examples/provider/%,$(wildcard examples/*/*.yaml))
UPTEST_MANIFESTS_ALL := $(subst $(space),$(comma),$(_UPTEST_MANIFESTS_ALL_RAW))

# Default e2e input: CORE manifests (can be overridden per-target below).
UPTEST_INPUT_MANIFESTS ?= $(UPTEST_MANIFESTS_CORE)

UPTEST_SETUP_SCRIPT ?= test/setup.sh

# The SecretAccess kinds manage access to an organization secret the provider
# never creates. When a run includes one, test/setup.sh creates the disposable
# secrets PGH_E2E_ACTIONS_SECRET and PGH_E2E_DEPENDABOT_SECRET in $(E2E_ORG) and
# test/teardown.sh removes them after the delete step, so a run that addresses
# no SecretAccess example never writes to the organization's secrets. The value
# stays recursive (`=`) so a per-resource target's UPTEST_INPUT_MANIFESTS is seen.
export E2E_ORG_SECRETS = $(if $(findstring secret-access,$(UPTEST_INPUT_MANIFESTS)),true)
UPTEST_ARGS += --teardown-script=$(abspath test/teardown.sh)

# Per-run uptest test-directory isolation (each concurrent E2E run gets its own
# staging directory) -- without this, concurrent E2E runs share /tmp/uptest-e2e
# and corrupt each other's staged chainsaw test files.
#
# CASE_DIR points the shared convergence barrier (test/hooks/converge-barrier.sh)
# at uptest's own rendered manifests for this run -- never examples/, whose
# sources still carry unsubstituted placeholders. It shares the same
# KIND_CLUSTER_NAME-derived root as --test-directory above, so concurrent runs
# cannot read each other's staged manifests. --post-assert-script runs ONE shared
# convergence barrier per test run, after every resource's own assertions pass.
ifdef KIND_CLUSTER_NAME
  UPTEST_ARGS += --test-directory=/tmp/uptest-e2e-$(KIND_CLUSTER_NAME)
  export CASE_DIR = /tmp/uptest-e2e-$(KIND_CLUSTER_NAME)/case
  UPTEST_ARGS += --post-assert-script=$(abspath test/hooks/converge-barrier.sh)
endif

-include build/makelib/uptest.mk

# Minimum free disk space (GB) required on / before running E2E. A full root
# filesystem does not announce itself -- it surfaces as a build or pod failure
# that never mentions disk, 30+ minutes into an uptest run, as a bogus PROVIDER
# failure. Override on the command line if needed.
E2E_MIN_FREE_GB ?= 15

# Pre-flight: validate free disk space and the GitHub App credentials before any E2E run.
e2e-preflight: ## Validate disk space and GitHub App credentials before E2E
	@FREE=$$(df -BG --output=avail / | tail -1 | tr -dc '0-9'); \
	if [ "$$FREE" -lt "$(E2E_MIN_FREE_GB)" ]; then \
	  echo "ERROR: only $${FREE}GB free on / -- E2E needs >= $(E2E_MIN_FREE_GB)GB." >&2; \
	  echo "  A full disk fails E2E as a BOGUS PROVIDER error 30+ min from now." >&2; \
	  echo "  Reclaim: docker image prune -f; rm -rf /tmp/tmp.* /tmp/go-build*;" >&2; \
	  echo "           delete unused kind clusters (kind delete cluster --name <name>);" >&2; \
	  echo "           trim \$$(go env GOCACHE) (oldest-first, keep it under ~30GB)." >&2; \
	  exit 1; \
	fi; \
	echo "e2e-preflight: disk OK ($${FREE}GB free, >= $(E2E_MIN_FREE_GB)GB required)"
	@./test/preflight.sh || exit 1

# `e2e: e2e-preflight` looks like a preflight but is not one: build/makelib/uptest.mk
# already declares `e2e: build controlplane.down controlplane.up ... uptest`, and a
# second rule for the same target only APPENDS to that prerequisite list. `e2e` has
# no recipe of its own, so a prerequisite added here lands at the END -- the guard
# would print its verdict after the tests it exists to gate have already run.
#
# Attach the guard instead to `build` -- the first prerequisite in uptest.mk's e2e
# chain, and one that HAS a recipe -- gated on the top-level goal so a plain
# `make build` never starts requiring GitHub credentials:
build: $(if $(filter e2e e2e.%,$(MAKECMDGOALS)),e2e-preflight,)

# The MAKECMDGOALS filter above is not optional polish. `build` is the documented
# public entry point of this repo, so an unconditional `build: e2e-preflight` would
# make compiling the binary require a live GitHub App. The filter must list every
# E2E entry point this Makefile defines that `e2e.%` cannot match (`-` is not `.`).
# This provider defines no `e2e-full` aggregate, so `e2e e2e.%` is complete today.
# A target whose recipe shells out via recursive `$(MAKE) e2e ...` needs no entry:
# the sub-make's own MAKECMDGOALS is `e2e`.

# Pin VERSION for the e2e path. local.xpkg.mk derives the package cache key by
# stripping the version off the xpkg filename and writes the Provider CR with the
# bare PROJECT_NAME. They agree only when VERSION matches -v<d>.<d>.<d>*.xpkg;
# otherwise the provider hangs at Installed=False reason=UnpackingPackage.
e2e e2e.%: VERSION := v0.0.0-e2e

# Per-resource E2E target blocks, sorted alphabetically by resource name.

e2e.actions-secret-access: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_ACTIONS_SECRET_ACCESS)
e2e.actions-secret-access: e2e

e2e.dependabot-secret-access: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_DEPENDABOT_SECRET_ACCESS)
e2e.dependabot-secret-access: e2e

# GATED: invites a real GitHub user; set the external name to a login that may be invited.
e2e.membership: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_MEMBERSHIP)
e2e.membership: e2e

e2e.organization-variable: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_ORGANIZATION_VARIABLE)
e2e.organization-variable: e2e

e2e.organization-webhook: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_ORGANIZATION_WEBHOOK)
e2e.organization-webhook: e2e

# Listed after organization-variable and organization-webhook because the target
# blocks are kept in byte order with the colon, and `-` sorts before `:`.
# GATED: adopts the shared test organization, which can be neither created nor
# deleted. Both scopes manage that one organization, so they run as two sequential
# passes instead of a comma pair.
e2e.organization: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_ORGANIZATION)
e2e.organization: e2e
	@$(MAKE) e2e UPTEST_INPUT_MANIFESTS=$(UPTEST_MANIFESTS_ORGANIZATION_NS)

e2e.repository: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_REPOSITORY)
e2e.repository: e2e

e2e.runner-group: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_RUNNER_GROUP)
e2e.runner-group: e2e

e2e.team: UPTEST_INPUT_MANIFESTS = $(UPTEST_MANIFESTS_TEAM)
e2e.team: e2e

# One dotted per-resource target per .PHONY line, sorted, so two concurrent waves
# adding different resources land on distinct lines instead of the same shared line.
.PHONY: e2e-preflight
.PHONY: e2e.actions-secret-access
.PHONY: e2e.dependabot-secret-access
.PHONY: e2e.membership
.PHONY: e2e.organization
.PHONY: e2e.organization-variable
.PHONY: e2e.organization-webhook
.PHONY: e2e.repository
.PHONY: e2e.runner-group
.PHONY: e2e.team

# ====================================================================================
# Update-Tester Targets
#
# The update tester is consumed as a pinned module from tools/update-tester (a stub
# module holding only go.mod/go.sum, no vendored source), so there is no build
# step: `go -C` runs it directly from the module cache. Every path handed across
# that boundary is absolute, because `go -C tools/update-tester` changes the child
# process's working directory.
#
# Per-field update tests run live inside the uptest flow, through the
# post-assert-hook symlinks in test/hooks/. update-test.validate is the offline
# half: a static coverage gate that reads the example YAML and the generated Go
# types and needs no cluster.
UPDATE_TESTER := go -C tools/update-tester tool crossplane-update-tester

# update-test.validate: annotation-coverage gate. Runs `validate` against every
# example manifest that carries a crossplane.io/update-test: annotation KEY, so a
# field silently dropped from an annotation is caught. The candidate list is the
# union of an annotation still inline and one that lives in a
# `<manifest>.yaml.uptest` sidecar (stripped back to the manifest path the tool
# reads); a *.yaml-only glob would go blind and validate nothing the moment
# annotations migrate to sidecars. The tool resolves each manifest's types file
# itself from --root, by the manifest's scope and Kind.
update-test.validate:
	@fail=0; \
	for f in $$( { grep -rl 'crossplane.io/update-test:' examples --include='*.yaml'; \
	  find examples -name '*.yaml.uptest' -exec grep -l 'crossplane.io/update-test:' {} \; \
	    | sed 's/\.uptest$$//'; } | sort -u); do \
	  echo "=== $$f ==="; \
	  $(UPDATE_TESTER) validate --root "$$PWD" "$$PWD/$$f" || fail=1; \
	done; \
	exit $$fail

.PHONY: update-test.validate

fallthrough: submodules
	@echo Initial setup complete. Running make again . . .
	@make

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

.PHONY: submodules fallthrough run dev dev-clean

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
