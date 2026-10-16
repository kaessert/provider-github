# GitHub API Rate Limit Tracking

This document describes the simplified rate limit tracking implementation for the GitHub Crossplane provider.

## Overview

The rate limit tracking system provides essential monitoring of GitHub API rate limits, focusing only on the most important metrics needed to track remaining API requests and detect rate limit violations.

## Metrics

The following Prometheus metrics are available:

### Rate Limiting Metrics

All metrics carry three labels:

- `organization` — the GitHub org the call targeted.
- `app_id` — the GitHub App ID (identifies the App definition; shared across all installations of the same App).
- `app_installation_id` — the GitHub App Installation ID (identifies one specific installation in one org/account).

Together `(app_id, app_installation_id)` uniquely identifies a credential. With a multi-app `ProviderConfig` each credential appears as its own time series.

- `github_rate_limit_remaining` (Gauge)
  - Labels: `organization`, `app_id`, `app_installation_id`
  - Description: Number of API requests remaining in the current rate limit window

- `github_rate_limit_reset_time` (Gauge)
  - Labels: `organization`, `app_id`, `app_installation_id`
  - Description: Unix timestamp when the rate limit will reset

- `github_rate_limit_limit` (Gauge)
  - Labels: `organization`, `app_id`, `app_installation_id`
  - Description: Total number of API requests allowed in the current rate limit window

- `github_rate_limit_exceeded_total` (Counter)
  - Labels: `organization`, `app_id`, `app_installation_id`
  - Description: Total number of times GitHub rate limit was exceeded. No series appears until at least one 429 is observed.

- `github_app_unhealthy_total` (Counter)
  - Labels: `organization`, `app_id`, `app_installation_id`
  - Description: Total number of failed GitHub API calls where no HTTP response was received — typically a 401 from `/app/installations/<id>/access_tokens` (bad/stale credentials) or a network error. Distinct from `github_rate_limit_exceeded_total` because the remediation is different: rotate or fix the credential, not wait for reset. The provider also puts the credential into a 60-second cooldown so the picker steers away from it.

- `github_api_calls_total` (Counter)
  - Labels: `organization`, `app_id`, `app_installation_id`, `method`
  - Description: Total number of GitHub API calls attempted, labeled by the wrapped client method (`Organizations.Get`, `Repositories.Edit`, etc.). Counts every attempt including failures. Use to attribute traffic volume to specific endpoints per credential.

- `github_app_picker_picks_total` (Counter)
  - Labels: `organization`, `app_id`, `app_installation_id`, `reason`
  - Description: Total number of times the credential picker selected each `(org, app, installation)`. The `reason` label takes one of: `highest_remaining` (one strict winner among the available candidates), `random_tiebreak` (N ≥ 2 candidates were tied at the top), or `only_candidate` (every other credential was in cooldown).

### Repository State Metrics

- `github_repository_unreconcilable` (Gauge)
  - Labels: `organization`, `repository`, `dimension`
  - Description: `1` while the repository has declared state that stays unapplied, `0` otherwise. `dimension` is one of `collaborators` (pending invitations or a role GitHub enforces for an org owner), `branch_protection` (rules for missing branches or settings and actors GitHub did not apply), `archived` (the repository is archived, so its settings, branch protection, rulesets and webhooks are frozen), `settings` (repository settings GitHub did not apply on the last push because they are not available on the repository's plan or type), `rulesets_forbidden` (GitHub answers the ruleset list with 403, for example on a private repository whose plan has no rulesets, while the Repository declares at least one ruleset, so the declared `repositoryRules` are left as they are; with `repositoryRules: []` the value is `0`, because the Repository declares no ruleset), `ruleset_parameters` (a ruleset the Repository manages carries a rule type GitHub returns and the provider does not model, so that ruleset is left as it is while the other rulesets reconcile, or a parameter GitHub returns and the provider does not model, which the next update of that ruleset resets) or `ruleset_spec` (a ruleset in the Repository's spec lists the same ref pattern or bypass actor twice; the declared `repositoryRules` are left as they are until the spec is fixed). Each value mirrors the Repository's `CollaboratorPartial`, `BranchProtectionPartial`, `ArchivedConfigFrozen`, `SettingsPartial` or `RulesetsPartial` condition; `rulesets_forbidden`, `ruleset_parameters` and `ruleset_spec` share `RulesetsPartial`: reason `RulesetsForbidden` carries GitHub's message, reason `UnmanagedRuleTypes` a message naming each ruleset left as it is and its rule types, followed by any unmanaged parameters, reason `UnmanagedParameters` a message naming the ruleset, rule and parameters, reason `InvalidRulesets` a message naming the ruleset and the duplicate. A repository's series are removed when the resource's finalizer is removed, which covers both deletion policies, and when the provider finds the repository gone on GitHub.

### A note on shared credentials across ProviderConfigs

The internal quota pool is process-wide and keyed by the credential bytes (not by `ProviderConfig`). If two `ProviderConfig`s reference the same credential (same `appId,installationId,privateKey` value), they share **one** pool entry — which is correct, because GitHub also enforces the rate limit at the installation level, not per-`ProviderConfig`. A 429 or auth failure observed via one `ProviderConfig` correctly steers the picker away from the credential when the other `ProviderConfig` reconciles next.

The Prometheus output reflects this differently though: gauge and counter series are split by the `organization` label, which is taken from the *resource* being reconciled. So a single installation used across two orgs will appear as two series — `{organization=X, app_id=A, app_installation_id=B}` and `{organization=Y, app_id=A, app_installation_id=B}` — with **identical values** (both reflect the same underlying installation state). When aggregating across orgs, use `max by (app_id, app_installation_id) (...)` rather than `sum` to avoid double-counting.

## Implementation

### Rate Limit Package (`internal/telemetry/rate_limit.go`)

- **RateLimitMetrics**: Core metrics structure with only essential Prometheus metrics
- **NewRateLimitMetrics()**: Creates and registers rate limit metrics
- **RecordRateLimitInfo()**: Records rate limit information from GitHub responses
- **IsRateLimited()**: Checks if an error is due to rate limiting
- **GetRateLimitInfo()**: Extracts rate limit information from responses
- **IsRateLimitExceeded()**: Checks if response indicates rate limit exceeded
- **GetRateLimitUsagePercentage()**: Calculates percentage of rate limit used

### Client (`internal/clients/client.go`)

- **Client**: Wraps the standard GitHub client with rate limit tracking
- **NewClient()**: Creates a new rate limit tracking client
- **WithRateLimitTracking()**: Returns a client configured for a specific organization

All GitHub API calls automatically record rate limit information from response headers.

## Usage

The rate limit tracking is automatically enabled for all GitHub API calls. The metrics are exposed on the `/metrics` endpoint and can be scraped by Prometheus.

### Example Prometheus Queries

```promql
# Current remaining requests across all installations used for an organization
sum by (app_installation_id) (github_rate_limit_remaining{organization="my-org"})

# Rate limit exceeded events per installation
rate(github_rate_limit_exceeded_total[5m])

# Rate limit usage percentage per installation for an organization
(1 - github_rate_limit_remaining{organization="my-org"} / github_rate_limit_limit{organization="my-org"}) * 100

# Identify the least-healthy installation in a multi-app ProviderConfig
bottomk(1, github_rate_limit_remaining{organization="my-org"})

# All installations of a specific App across all orgs
github_rate_limit_remaining{app_id="397599"}

# Identify a credential that's failing auth or hitting network issues
rate(github_app_unhealthy_total[5m]) > 0

# Top API methods by call volume, per credential
topk(5, sum by (method, app_installation_id) (rate(github_api_calls_total[5m])))

# Fraction of picks that hit the fallback "only_candidate" branch
# (high values mean most credentials are in cooldown)
sum(rate(github_app_picker_picks_total{reason="only_candidate"}[5m]))
  /
sum(rate(github_app_picker_picks_total[5m]))
```

### Example Alert Rules

```yaml
groups:
- name: github-rate-limits
  rules:
  - alert: GitHubRateLimitLow
    expr: github_rate_limit_remaining{organization="my-org"} < 100
    for: 1m
    labels:
      severity: warning
    annotations:
      summary: "GitHub API rate limit is low"
      description: "Only {{ $value }} requests remaining for organization {{ $labels.organization }}"

  - alert: GitHubRateLimitExceeded
    expr: rate(github_rate_limit_exceeded_total[5m]) > 0
    for: 0m
    labels:
      severity: critical
    annotations:
      summary: "GitHub API rate limit exceeded"
      description: "Rate limit exceeded for organization {{ $labels.organization }}"

  - alert: GitHubAppUnhealthy
    expr: rate(github_app_unhealthy_total[5m]) > 0
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "GitHub App credential failing auth or network"
      description: "App {{ $labels.app_id }} installation {{ $labels.app_installation_id }} in org {{ $labels.organization }} is failing to mint tokens or reach GitHub. Rotate the credential or check connectivity."
```

The three rules above work in both single-app and multi-app setups — they fire per `(app_id, app_installation_id)` series, which is the right granularity in either case.

### Multi-app alert rules

These three rules are only meaningful when the `ProviderConfig` has `additionalCredentials` configured (i.e. there's a pool to be evaluated as a whole, not just one credential):

```yaml
groups:
- name: github-rate-limits-multi-app
  rules:
  - alert: GitHubAllCredentialsExhausted
    # Fires when *every* credential's remaining is below the threshold —
    # no healthy fallback is available. Different from GitHubRateLimitLow,
    # which fires per-credential and is fine to ignore as long as another
    # credential is still healthy.
    expr: min by (organization) (github_rate_limit_remaining) < 100
    for: 2m
    labels:
      severity: critical
    annotations:
      summary: "All GitHub Apps for org {{ $labels.organization }} are nearly exhausted"
      description: "Lowest remaining quota across all configured credentials is {{ $value }}. Reconciles will start failing soon."

  - alert: GitHubPickerCooldownStorm
    # Fires when more than half of picks are landing on only_candidate —
    # meaning most of the pool is in cooldown and the picker has no real
    # choice. Symptom of a partial outage (some apps down) or a 429 spike.
    expr: |
      sum by (organization) (rate(github_app_picker_picks_total{reason="only_candidate"}[5m]))
        /
      sum by (organization) (rate(github_app_picker_picks_total[5m]))
        > 0.5
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Picker for org {{ $labels.organization }} is stuck on the fallback credential"
      description: "More than 50% of picks in the last 5m landed on the only available credential — the rest of the pool is in cooldown."

  - alert: GitHubAppPoolDegraded
    # Fires when the number of credentials actively serving traffic drops
    # below the number configured. Suggests one or more credentials have
    # been silently rotated out or are stuck in long cooldowns.
    # Replace `expected_pool_size` with your configured count.
    expr: count by (organization) (rate(github_api_calls_total[10m]) > 0) < expected_pool_size
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "Active GitHub App pool for {{ $labels.organization }} is degraded"
      description: "Only {{ $value }} credentials served traffic in the last 10m. Expected the full pool size — check for rotated-out or persistently-failing credentials."
```

### Repository state alert rules

```yaml
groups:
- name: github-repository-state
  rules:
  - alert: GitHubRepositoryUnreconcilable
    expr: github_repository_unreconcilable == 1
    for: 1h
    labels:
      severity: warning
    annotations:
      summary: "{{ $labels.repository }} has declared state that stays unapplied"
      description: "Repository {{ $labels.repository }} in org {{ $labels.organization }} has had unapplied {{ $labels.dimension }} state for 1h. See the Repository's conditions for the details."
  - alert: GitHubRulesetUnmanagedParameters
    expr: github_repository_unreconcilable{dimension="ruleset_parameters"} == 1
    labels:
      severity: warning
    annotations:
      summary: "A ruleset of {{ $labels.repository }} ({{ $labels.organization }}) holds a rule type or parameter the provider does not model"
      description: |
        A ruleset holding an unmodelled rule type is left as it is; the next update of a ruleset holding an unmodelled parameter resets the parameter. The RulesetsPartial condition (reason UnmanagedRuleTypes or UnmanagedParameters) names them:
        kubectl get repositories -o custom-columns='NAME:.metadata.name,UNMANAGED:.status.conditions[?(@.type=="RulesetsPartial")].message'
        Then upgrade the provider, or open an issue naming the rule type or parameter.
  - alert: GitHubRulesetSpecInvalid
    expr: github_repository_unreconcilable{dimension="ruleset_spec"} == 1
    labels:
      severity: warning
    annotations:
      summary: "A ruleset in the spec of {{ $labels.repository }} ({{ $labels.organization }}) lists a ref pattern or bypass actor twice"
      description: |
        The declared repositoryRules are left as they are until the spec is fixed. The RulesetsPartial condition names the ruleset and the duplicate:
        kubectl get repositories -o custom-columns='NAME:.metadata.name,INVALID:.status.conditions[?(@.reason=="InvalidRulesets")].message'
        Then list the pattern or actor once in the Repository's spec.
```

## Benefits

1. **Simple and Focused**: Only tracks essential rate limit information
2. **Automatic Monitoring**: All GitHub API calls are automatically monitored
3. **Rate Limit Detection**: Specific detection and tracking of rate limiting events
4. **Alerting**: Essential alert rules for operational monitoring
5. **Minimal Overhead**: Reduced complexity and resource usage compared to full telemetry

## Files

### Core Files
- `internal/telemetry/rate_limit.go` - Rate limit metrics and utilities
- `internal/clients/client.go` - Rate limit tracking client wrapper

### Integration
- All controllers updated to use `Client` instead of `TelemetryClient`
- Main application initializes rate limit metrics
- Metrics exposed on `/metrics` endpoint
