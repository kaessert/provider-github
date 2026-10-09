# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Working rules

These rules override default behavior. Follow them on every change in this repo.

**Rule 1 — Think before coding.** State assumptions explicitly. If uncertain, ask rather than guess. Present multiple interpretations when ambiguity exists. Push back when a simpler approach exists. Stop when confused; name what's unclear.

**Rule 2 — Simplicity first.** Minimum code that solves the problem. Nothing speculative. No features beyond what was asked. No abstractions for single-use code. Test: would a senior engineer say this is overcomplicated? If yes, simplify.

**Rule 3 — Surgical changes.** Touch only what you must. Clean up only your own mess. Don't "improve" adjacent code, comments, or formatting. Don't refactor what isn't broken. Match existing style.

**Rule 4 — Goal-driven execution.** Define success criteria. Loop until verified. Don't follow steps — define success and iterate. Strong success criteria let you loop independently.

**Rule 5 — Use the model only for judgment calls.** Use me for: classification, drafting, summarization, extraction. Do NOT use me for: routing, retries, deterministic transforms. If code can answer, code answers.

**Rule 6 — Surface conflicts, don't average them.** If two patterns contradict, pick one (more recent / more tested). Explain why. Flag the other for cleanup. Don't blend conflicting patterns.

**Rule 7 — Read before you write.** Before adding code, read exports, immediate callers, shared utilities. "Looks orthogonal" is dangerous. If unsure why code is structured a way, ask.

**Rule 8 — Tests verify intent, not just behavior.** Tests must encode WHY behavior matters, not just WHAT it does. A test that can't fail when business logic changes is wrong.

**Rule 9 — Checkpoint after every significant step.** Summarize what was done, what's verified, what's left. Don't continue from a state you can't describe back. If you lose track, stop and restate.

**Rule 10 — Match the codebase's conventions, even if you disagree.** Conformance > taste inside the codebase. If you genuinely think a convention is harmful, surface it. Don't fork silently.

**Rule 11 — Fail loud.** "Completed" is wrong if anything was skipped silently. "Tests pass" is wrong if any were skipped. Default to surfacing uncertainty, not hiding it.

**Rule 12 — Preserve reversibility.** Every change should be revertible by a single `git revert` or `git checkout`. Don't half-finish — commit the working slice or stash the rest. Don't bundle unrelated edits into one commit. Before destructive operations (force push, hard reset, dependency downgrade, deleting generated files), name the rollback path out loud.

## Project

`provider-github` is a [Crossplane](https://crossplane.io/) Provider that manages GitHub organizations, teams, repositories (including branch protection, webhooks, and rulesets), and memberships. Resources are reconciled via Kubernetes controllers built on `crossplane-runtime`.

The repository depends on the [Upbound build submodule](https://github.com/upbound/build) at `build/` — initialize it before doing anything else.

## Common commands

```sh
make submodules          # Initialize the build submodule (run once after clone)
make run                 # Build and run the provider locally, out-of-cluster (--debug)
make dev                 # Create a kind cluster, install CRDs, run the provider against it
make dev-clean           # Tear down the kind cluster from `make dev`
make reviewable          # Code generation, linters, and tests — run before opening a PR
make build               # Build the provider binary and OCI/xpkg artifacts
make test                # Unit tests
make test-integration    # Integration tests (requires kind + helm + up)
make generate            # Regenerate CRDs and zz_generated_*.go via controller-gen + angryjet
make lint                # golangci-lint (v2.4.0)
```

If your system Go is 1.26+ (the project targets `go 1.25.0`), `make reviewable` / `make lint` will panic with `file requires newer Go version go1.26 (application built with go1.25)`. Set `GOTOOLCHAIN=go1.25.0` to work around it locally — CI is unaffected because it uses Go 1.25.

Run a single test:

```sh
go test ./internal/controller/repository/... -run TestObserve -v
```

Adding a new managed resource type:

```sh
make provider.addtype provider=GitHub group=<group> kind=<Kind>
```

Then register the new API in `apis/github.go` (or the appropriate group aggregator) and call its `Setup` from `internal/controller/github.go`.

## Architecture

### Entry point and wiring

- `cmd/provider/main.go` — kingpin CLI; constructs the controller-runtime manager, starts a *second* HTTP server on `:8081` for custom Prometheus metrics (`/metrics`), and starts a 15-minute ticker that calls `ghclient.CleanupExpiredServices()` to evict expired GitHub App tokens. Two setup paths: `Setup` (no per-reconcile timeout) vs `SetupWithTimeout` (uses `--reconcile-timeout`, default `1m`).
- `internal/controller/github.go` — registers each resource's `Setup`/`SetupWithTimeout` with the manager. **Adding a new controller requires editing this file.**

### API types (CRDs)

- `apis/cluster/v1alpha1/` — `ProviderConfig`, `ProviderConfigUsage` (provider-level config).
- `apis/cluster/organizations/v1alpha1/` — managed resources: `Organization`, `Team`, `Repository`, `Membership`, `ActionsSecretAccess`, `DependabotSecretAccess`. Hand-written `*_types.go` plus generated `zz_generated_*.go` (deepcopy, managed, managedlist, resolvers).
- `apis/generate.go` drives codegen with `controller-gen` (CRDs → `package/crds/`) and `angryjet` (crossplane-runtime methodsets). Always run `make generate` after editing `*_types.go`.

### Controllers

Each managed resource lives in `internal/controller/<resource>/` with a `<resource>.go` (the Observe/Create/Update/Delete logic), a `cluster.go` (Setup, connector and ProviderConfig resolution for the cluster-scoped kind) and `<resource>_test.go`. They follow the standard `crossplane-runtime/pkg/reconciler/managed` pattern: a `connector` builds the GitHub client from `ProviderConfig` credentials and produces an `external` that implements `Observe`/`Create`/`Update`/`Delete`.

Important: in `Connect`, controllers call `ghclient.ResolveAndConnect(ctx, kube, pc, metrics, orgName)` to get a ready-to-use `*Client`. That helper resolves every credential entry on the `ProviderConfig` (the primary `credentials` plus any `additionalCredentials`), consults the global per-app quota pool to pick the credential with the most remaining rate-limit headroom, builds a cached client via `NewCachedServices`, and wraps it with rate-limit tracking — so every controller automatically benefits from token caching, per-app quota selection, and 429-aware cooldowns. **Don't re-implement the inline secret extraction + client wrapping pattern in a new controller; call `ResolveAndConnect`.**

`config.Setup` (provider config controller) is set up unconditionally; resource controllers vary based on `--reconcile-timeout`.

### GitHub client layer (`internal/clients/`)

- `services.go` — defines narrow per-service interfaces (`ActionsClient`, `OrganizationsClient`, `TeamsClient`, `RepositoriesClient`, etc.) over `google/go-github/v90`. `Services` is a struct of those interfaces — the bag of service handles bound to one credential. `Is404(err)` is the canonical way to detect "not found" GitHub errors.
- `cached_services.go` — `NewCachedServices` parses creds in the format `appId,installationId,privateKeyPEM` (using `bradleyfalzon/ghinstallation/v2` for App auth) and keeps a process-wide map of `*Services` instances keyed by `GenerateCacheKey(creds)` (8-byte SHA-256 prefix), with a 50-minute TTL (GitHub App tokens expire at 60). `CleanupExpiredServices` is the periodic eviction routine called from `main`.
- `client.go` — defines `Client`, the outer wrapper controllers use. Each per-service wrapper (`actionsClient`, `organizationsClient`, etc.) embeds the underlying service interface and records every response into `telemetry.RateLimitMetrics` (Prometheus) and the per-credential cooldown pool. `metrics` is nil-safe so unit tests can skip telemetry setup.
- `pool.go` — process-wide `globalPool` of per-credential `AppQuota` snapshots (`Remaining` + `CooldownUntil` + `ConsecutiveFailures`). `recordResponse(cacheKey, resp, err)` handles three cases:
    - Normal response (incl. 429): updates `Remaining` from the rate-limit headers; on 429 sets `CooldownUntil` to `X-RateLimit-Reset` (30s floor when that header is missing or in the past); resets `ConsecutiveFailures` to zero.
    - `(nil, err)` — typically a ghinstallation token-mint 401 or a network blip: bumps `ConsecutiveFailures` and extends cooldown with exponential backoff (60s → 120s → 240s … capped at 15min).
    - `(nil, nil)` — no-op.
  Cooldown updates use `max()` semantics so a transient blip can't shorten a longer 429 cooldown. `pick` chooses the credential with the most remaining quota among those not in cooldown; unseen creds sort to the top so they get tried first. Returns `(cacheKey, reason, error)` — `reason` is one of `"highest_remaining"`, `"random_tiebreak"`, `"only_candidate"`. When every credential is in cooldown, returns a `*CooldownError` wrapping `ErrAllAppsInCooldown` (use `errors.As` for the `RetryAt` hint, `errors.Is` for the kind).
- `connect.go` — `ResolveAndConnect` is the single entry point controllers use (described above).
- `resolve.go` — `resolveAllCredentials` errors with `"credentials at index N: secret X/Y key Z is empty or missing"` when a referenced Secret key returns empty bytes — operators don't have to chase the failure deeper. If `NewCachedServices` fails to construct (non-numeric IDs, malformed PEM), the failure is also recorded into the pool + `github_app_unhealthy_total` before propagating, so the picker steers away on the next attempt.
- `fake/client.go` — mock implementations used by controller unit tests (each test wires only the methods it needs).

### Telemetry (`internal/telemetry/rate_limit.go`)

Owns seven Prometheus metrics. Six carry `(organization, app_id, app_installation_id)` so each credential is its own time series:

- `github_rate_limit_remaining`, `github_rate_limit_reset_time`, `github_rate_limit_limit` — gauges, updated from response headers.
- `github_rate_limit_exceeded_total` — counter, incremented on 429.
- `github_app_unhealthy_total` — counter, incremented when a wrapped call returns no HTTP response but an error (token-mint 401, network blip, malformed PEM caught at construction time).
- `github_api_calls_total` — counter with an extra `method` label (e.g. `"Organizations.Get"`); incremented on every wrapped call regardless of outcome.
- `github_app_picker_picks_total` — counter with an extra `reason` label (`"highest_remaining"` | `"random_tiebreak"` | `"only_candidate"`); incremented on every successful picker selection.

The seventh, `github_repository_unreconcilable{organization, repository, dimension}`, is a gauge set by the repository controller: 1 while the `CollaboratorPartial` / `BranchProtectionPartial` / `ArchivedConfigFrozen` / `SettingsPartial` condition (dimension `collaborators` / `branch_protection` / `archived` / `settings`) is True, else 0. Its series are removed when the resource's finalizer is removed, which covers both deletion policies, and when the provider finds the repository gone on GitHub.

`app_id` and `app_installation_id` hold the real GitHub App ID / Installation ID parsed via `ExtractAppIDs` — not the cache-key hash. Metrics are initialized once in `main`, registered with the default Prometheus registry, and exposed on `:8081`/metrics. `newRateLimitMetrics()` (lowercase) and `NewForTest()` (exported) build the struct without registering — the former for in-package tests, the latter for cross-package wiring tests. See `RATE_LIMIT_TRACKING.md` for the full metric/alert reference.

### Authentication

`ProviderConfig.Spec.Credentials` points at a `Secret` whose value is the literal string `appId,installationId,privateKey` (PEM, with newlines). `ProviderConfig.Spec.AdditionalCredentials` (optional) is a list of extra credentials of the same shape — when set, the provider treats them as a pool with the primary credentials and picks the app with the most remaining rate-limit quota on every reconcile. See `examples/provider/config.yaml` for both shapes.

## Code conventions

- Go 1.25, modules. Module path: `github.com/crossplane/provider-github`.
- Imports follow `goimports` with local prefix `github.com/my/project` (per `.golangci.yml` — yes, that prefix string is a quirk of the config).
- Linters enabled: `govet`, `gocyclo` (max 30), `gocritic`, `goconst`, `prealloc`, `unconvert`, `misspell`, `nakedret`. The `repository` controller has high complexity by design — `gocyclo:ignore`/`//nolint:gocyclo` is used judiciously.
- Generated files (`zz_generated_*.go`, `package/crds/`) are committed; never hand-edit them.
- All new managed resources must obtain their GitHub client via `ghclient.ResolveAndConnect`, never by calling `NewCachedServices` / `NewClient` directly — that's how they participate in the multi-app credential pool and quota-aware picking.

## Where to find more

- `README.md` — a short overview of the implemented resources.
- `RATE_LIMIT_TRACKING.md` — Prometheus metrics, example queries, and alert rules.
- `PROVIDER_CHECKLIST.md` — Crossplane community checklist for providers (mostly governance, not day-to-day dev).
- `examples/organizations/` — sample manifests for each managed resource.
