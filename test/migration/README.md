# Migration validation

A one-off validation of moving an existing installation from the last upstream
release to this tree. It answers three questions against a real GitHub
organization:

1. **In-place upgrade.** Take a cluster running the baseline provider with a full
   set of managed resources, point the Provider at this tree's build. Is anything
   recreated or written?
2. **Observe-only adoption, cluster-scoped kinds.** Can this tree's cluster-scoped
   managed resources adopt existing GitHub objects with `managementPolicies:
   [Observe]`, fill `status.atProvider` from GitHub, and write nothing?
3. **Observe-only adoption, namespaced kinds.** The same for the namespaced kinds
   in `organizations.github.m.crossplane.io`.

It is **not part of the end-to-end suite**. No `e2e` target, `UPTEST_MANIFESTS_*`
variable, CI workflow or file under `examples/` reaches it, and nothing gates on
it. `check-separation.sh` proves that (below).

| | Version | Built from |
|---|---|---|
| Baseline ("v1") | upstream tag `v0.22.0`; its CRDs are identical to commit `13ff658` and it has all nine kinds | source, fetched and built during the run |
| Candidate ("v2") | this tree | source, built during the run |

Both builds are loaded into the cluster locally; no registry is involved.

## Running it

```sh
# offline checks: no cluster, no GitHub call
make validate.migration.fixtures

# the scenarios: GitHub API + a cluster
make validate.migration.upgrade
make validate.migration.adopt-cluster
make validate.migration.adopt-namespaced
make validate.migration              # all three, in turn
```

The scenarios fail at once, naming the input, when something is missing:

| Variable | Meaning |
|---|---|
| `PROVIDER_GITHUB_APP_ID` | GitHub App ID |
| `PROVIDER_GITHUB_APP_INSTALLATION_ID` | installation ID of the App on the test organization |
| `PROVIDER_GITHUB_APP_PRIVATE_KEY_B64` | the App's PEM private key, base64-encoded (decoded before use) |
| `KIND_CLUSTER_NAME`, `KUBECONFIG` | the cluster to use. **The harness never creates a cluster under a name of its own** and never runs `kind create cluster`; whatever launches it provides the cluster name and kubeconfig, and tears the cluster down. The control plane is installed with the Makefile's `controlplane.up` target, which reuses a cluster of that name. |
| `MIGRATION_ORG` | test organization, default `pgh-test` (set in the Makefile, overridable) |

Optional: `MIGRATION_MEMBER` (an existing organization member the fixtures use;
default: the first user member found), `MIGRATION_FORK_SOURCE` (public `owner/repo`
to fork, default `octocat/Hello-World`), `MIGRATION_WORKDIR` (evidence and scratch,
default `$TMPDIR/provider-github-migration`), `MIGRATION_POLL` (provider `--poll`,
default `15s`), `MIGRATION_READY_TIMEOUT`, `MIGRATION_BASELINE_REF`,
`MIGRATION_BASELINE_REPO`, `MIGRATION_BASELINE_DIR` (reuse a checkout of the
baseline), `MIGRATION_REQUIRE_BASELINE_READY=1` (fail rather than warn when a
fixture is not Ready on the baseline) and `MIGRATION_KEEP_V1_FLAGS=1` (upgrade
without replacing the baseline's runtime flags, to record what a user who leaves
their runtime configuration alone sees). The full list is in `lib.sh`.

Required tools: `curl openssl jq yq base64 git make docker go` and `kubeconform`
for the offline fixture check. `kubectl`, `kind`, `helm` and the Crossplane CLI are
installed through the Makefile.

Run one scenario at a time, and not while another test is using the same
organization: the harness reads and restores organization-wide settings (see
"Safety").

Each scenario writes its evidence under `$MIGRATION_WORKDIR/<scenario>/`:
`results.tsv` and `report.md` (every assertion), the snapshots, the diffs, the
managed-resource state, the provider logs and the rendered fixtures.

## Layout

| Path | Purpose |
|---|---|
| `fixtures/v1/` | the baseline-schema fixtures: all nine kinds, every nested sub-object |
| `fixtures/adopt/cluster/`, `fixtures/adopt/namespaced/` | Observe-only adoption fixtures, one set per scope |
| `coverage.tsv`, `check-coverage.sh` | the kind x sub-object coverage table and its checker |
| `validate-fixtures.sh` | fixtures against the CRDs, offline |
| `snapshot.sh` | GitHub state snapshot and diff |
| `oob.sh` | out-of-band setup, seeding and cleanup through the GitHub API |
| `cluster.sh` | builds, control plane, local package and image loading, managed-resource state |
| `scenario.sh`, `scenario-upgrade.sh`, `scenario-adopt-cluster.sh`, `scenario-adopt-namespaced.sh`, `adopt-common.sh` | the three drivers and what they share |
| `expect-adopt-mirror.tsv` | what `status.atProvider` must report for the seeded objects |
| `check-separation.sh` | proof the harness is unreachable from the end-to-end suite |
| `selftest.sh`, `testdata/` | offline tests of the harness against a read-only local stand-in for the GitHub API |
| `run.sh` | entry point used by the Makefile |

## Fixtures

Every GitHub object a fixture creates is named `pgh-mig-*` (repositories, teams,
runner groups, webhook URLs under `https://example.com/pgh-mig`) or `PGH_MIG_*`
(variables, secrets). The snapshot and the cleanup key on those prefixes and on
nothing else.

`fixtures/v1/` (22 objects):

| File | Objects |
|---|---|
| `00-prerequisites.yaml` | the Secret the webhook fixtures read |
| `10-teams.yaml` | parent, child (members, parent), and one with `deletionPolicy: Orphan` |
| `20-memberships.yaml` | the Membership of an existing member, `deletionPolicy: Orphan` |
| `30-repositories.yaml` | main (template, topics, merge strategies, collaborator, team permission, webhook with secret, branch protection with checks, reviews, restrictions and signed commits, a ruleset), rules (rulesets, required deployments), fork, archive, template |
| `40-organization.yaml` | description, Actions-enabled repositories, the deprecated nested secrets access |
| `50-variables.yaml` | every visibility, one with non-default `managementPolicies`, one with `spec.providerRef` |
| `60-webhooks.yaml` | `writeConnectionSecretToRef`; `publishConnectionDetailsTo`; one without a secret |
| `70-runner-groups.yaml` | selected repositories; selected workflows |
| `80-secret-access.yaml` | ActionsSecretAccess and DependabotSecretAccess |

The fixtures carry `publishConnectionDetailsTo` and `providerRef` on purpose: the
candidate's schema no longer has them (they go with the move to crossplane-runtime
v2), and the upgrade scenario **records** what happens to them.

### Fixture coverage

Every nested object and array of objects in the baseline `forProvider` schema, plus
the spec-level lifecycle fields, and the object that sets it. `check-coverage.sh`
derives the schema side from the baseline CRDs and fails on a sub-object with no
row, and on a row no fixture satisfies (36 sub-objects, 51 rows).

| Kind | Nested sub-object | Set by |
|---|---|---|
| ActionsSecretAccess | `forProvider.selectedRepositories[]` | pgh-mig-actions-secret-access |
| DependabotSecretAccess | `forProvider.selectedRepositories[]` | pgh-mig-dependabot-secret-access |
| Organization | `forProvider.actions` | pgh-mig-org |
| Organization | `forProvider.actions.enabledRepos[]` | pgh-mig-org |
| Organization | `forProvider.secrets` | pgh-mig-org |
| Organization | `forProvider.secrets.actionsSecrets[]` | pgh-mig-org |
| Organization | `forProvider.secrets.actionsSecrets[].repositoryAccessList[]` | pgh-mig-org |
| Organization | `forProvider.secrets.dependabotSecrets[]` | pgh-mig-org |
| Organization | `forProvider.secrets.dependabotSecrets[].repositoryAccessList[]` | pgh-mig-org |
| OrganizationVariable | `forProvider.selectedRepositories[]` | pgh-mig-var-selected |
| OrganizationWebhook | `forProvider.secretKeyRef` | pgh-mig-hook-ess, pgh-mig-hook-wcs |
| Repository | `forProvider.branchProtectionRules[]` | pgh-mig-repo-main |
| Repository | `forProvider.branchProtectionRules[].branchProtectionRestrictions` | pgh-mig-repo-main |
| Repository | `forProvider.branchProtectionRules[].requiredPullRequestReviews` | pgh-mig-repo-main |
| Repository | `forProvider.branchProtectionRules[].requiredPullRequestReviews.bypassPullRequestAllowances` | pgh-mig-repo-main |
| Repository | `forProvider.branchProtectionRules[].requiredPullRequestReviews.dismissalRestrictions` | pgh-mig-repo-main |
| Repository | `forProvider.branchProtectionRules[].requiredStatusChecks` | pgh-mig-repo-main |
| Repository | `forProvider.branchProtectionRules[].requiredStatusChecks.checks[]` | pgh-mig-repo-main |
| Repository | `forProvider.createFork` | pgh-mig-repo-fork |
| Repository | `forProvider.createFromTemplate` | pgh-mig-repo-archive, pgh-mig-repo-main, pgh-mig-repo-rules |
| Repository | `forProvider.permissions` | pgh-mig-repo-main |
| Repository | `forProvider.permissions.teams[]` | pgh-mig-repo-main |
| Repository | `forProvider.permissions.users[]` | pgh-mig-repo-main |
| Repository | `forProvider.repositoryRules[]` | pgh-mig-repo-main, pgh-mig-repo-rules |
| Repository | `forProvider.repositoryRules[].bypassActors[]` | pgh-mig-repo-main |
| Repository | `forProvider.repositoryRules[].conditions` | pgh-mig-repo-main, pgh-mig-repo-rules |
| Repository | `forProvider.repositoryRules[].conditions.refName` | pgh-mig-repo-main, pgh-mig-repo-rules |
| Repository | `forProvider.repositoryRules[].rules` | pgh-mig-repo-main, pgh-mig-repo-rules |
| Repository | `forProvider.repositoryRules[].rules.pullRequest` | pgh-mig-repo-main |
| Repository | `forProvider.repositoryRules[].rules.requiredDeployments` | pgh-mig-repo-rules |
| Repository | `forProvider.repositoryRules[].rules.requiredStatusChecks` | pgh-mig-repo-main |
| Repository | `forProvider.repositoryRules[].rules.requiredStatusChecks.requiredStatusChecks[]` | pgh-mig-repo-main |
| Repository | `forProvider.webhooks[]` | pgh-mig-repo-main |
| Repository | `forProvider.webhooks[].secretKeyRef` | pgh-mig-repo-main |
| RunnerGroup | `forProvider.selectedRepositories[]` | pgh-mig-rg-selected |
| RunnerGroup | `forProvider.selectedWorkflows` | pgh-mig-rg-workflows |
| Team | `forProvider.members[]` | pgh-mig-team-child |
| Team | `forProvider.parent` | pgh-mig-team-child |
| Membership | `forProvider.role` | pgh-mig-membership |
| Repository | `forProvider.archived` | pgh-mig-repo-archive |
| Repository | `forProvider.isTemplate` | pgh-mig-repo-template |
| Repository | `forProvider.topics` | pgh-mig-repo-main |
| Repository | `forProvider.allowMergeCommit` | pgh-mig-repo-main |
| OrganizationVariable | `forProvider.visibility` | pgh-mig-var-all, pgh-mig-var-policies, pgh-mig-var-private, pgh-mig-var-providerref, pgh-mig-var-selected |
| OrganizationWebhook | `writeConnectionSecretToRef` | pgh-mig-hook-ess, pgh-mig-hook-wcs |
| OrganizationWebhook | `publishConnectionDetailsTo` | pgh-mig-hook-ess |
| OrganizationWebhook | `publishConnectionDetailsTo.configRef` | pgh-mig-hook-ess |
| OrganizationWebhook | `publishConnectionDetailsTo.metadata` | pgh-mig-hook-ess |
| OrganizationVariable | `managementPolicies` | pgh-mig-var-policies |
| Team | `deletionPolicy` | pgh-mig-team-orphan |
| OrganizationVariable | `providerRef` | pgh-mig-var-providerref |

Fields no fixture sets are listed by `check-coverage.sh` as information:
`apps` in the branch protection actor lists, `appId` of a status check and
`integrationId` of a ruleset status check (they need IDs of installed GitHub Apps).

### Validation

`validate-fixtures.sh` checks, with no cluster and no GitHub call:

* the `v1` fixtures against the baseline CRDs, and the adoption fixtures against the
  candidate CRDs of their scope, with unknown fields rejected (an API server would
  silently prune them);
* the CEL rules the CRDs carry, evaluated from the rule text;
* that the `v1` fixtures fail the candidate schema only on `publishConnectionDetailsTo`
  and `providerRef`;
* that each namespaced adoption fixture is its cluster counterpart moved to the
  namespaced scope and nothing else.

The baseline CRDs are read from `MIGRATION_BASELINE_CRDS`, else from
`MIGRATION_BASELINE_DIR/package/crds`, else from this repository's history at
`13ff658`. The upgrade scenario also asserts that the fetched tag's CRDs are
identical to that commit's.

## Out-of-band setup

The test organization pre-exists. `oob.sh prepare` first removes leftovers of earlier
runs (by prefix), picks the organization member the fixtures use, and **takes the
baseline snapshot before changing anything**. Then, through the GitHub API:

* creates the template repository `pgh-mig-template`;
* **upgrade:** sets the organization Actions policy to `selected` if it is not (the
  Organization fixture manages the enabled-repository list, which GitHub accepts only
  then), and creates four organization secrets, two Actions and two Dependabot, of
  visibility `selected` (the provider never creates a secret; sealed with
  `test/sealedbox`);
* **adopt:** seeds one object of every adoptable kind: a repository, a parent and a
  child team (with the member) and a team that will drift, a variable, an organization
  webhook, a runner group and two secrets, and records the webhook's numeric ID.

## Snapshot tool

```sh
test/migration/snapshot.sh dump out.json
test/migration/snapshot.sh diff before.json after.json [--ignore-timestamps]
test/migration/snapshot.sh ids out.json
```

`dump` issues GET requests only and records, for everything under the prefixes above,
the numeric ID and every managed setting: repository settings, topics, fork and
template origin, collaborators, team access, webhooks (GitHub masks the secret, so its
presence shows), branch protection, rulesets and environments; teams with parent and
members; variables and secrets (metadata only) with their selected repositories;
organization webhooks; runner groups. It also records the organization's description,
Actions policy and enabled-repository list, and the member's role. Timestamps are
filed apart, because a changed `updated_at` on an object whose settings did not change
is itself evidence of a write.

`diff` exits non-zero on any difference and reports changed numeric IDs (an object was
created, deleted or recreated) separately from changed settings.

## Scenarios

Each driver sets up, asserts and cleans up on every exit path. Cleanup deletes the
managed resources, removes the Provider (so nothing reconciles during the sweep),
deletes everything under the prefixes through the GitHub API, restores the
organization description and Actions settings from the baseline snapshot, and then
compares a fresh snapshot with the baseline (timestamps ignored): the organization must
be back where it started.

### (a) `upgrade`

1. Build the baseline tag and this tree; set up the organization (above).
2. Install the baseline Provider with the runtime flags it needs
   (`--enable-management-policies`, `--enable-external-secret-stores`) and apply the
   `v1` fixtures. Wait for every object to be Synced and Ready, let the provider run a
   few poll cycles, and snapshot GitHub and the managed resources.
3. Point the Provider at the candidate build and its own runtime configuration, and
   wait for it to be Healthy and the objects to settle. Let the candidate run a few
   poll cycles, snapshot again.
4. Assert:
   * the two GitHub snapshots are identical: same IDs, same settings, same
     timestamps (**no recreate, no write**);
   * no managed resource was deleted, recreated, renamed, or lost Ready/Synced;
   * `status.atProvider.id` is populated on the seven kinds that gain it, and the
     numeric ID of `OrganizationWebhook` and `RunnerGroup` is still GitHub's;
   * the webhook's `writeConnectionSecretToRef` secret is byte-identical;
   * the candidate's logs contain no panic.
5. Record, without asserting: what happens to `spec.publishConnectionDetailsTo`, the
   deprecated `spec.providerRef`, the externally published secret, and the objects
   with non-default management policies and `Orphan` deletion.

### (b) `adopt-cluster` and (c) `adopt-namespaced`

1. Build this tree; seed the adoption targets; install the candidate Provider and the
   ProviderConfigs; snapshot GitHub (`seeded`).
2. Apply the Observe-only fixtures of the scope. Several omit fields that are required
   for a writing object (Organization `description`, Membership `role`, variable
   `value`/`visibility`, webhook `url`/`contentType`/`events`, runner group and
   SecretAccess `visibility`), which the relaxed schema allows. One Team declares a
   description that differs from GitHub.
3. Assert: every object is Synced, and Ready unless it is the drift probe;
   `status.atProvider.id` is filled; the webhook and runner group IDs are GitHub's;
   `status.atProvider` reports GitHub's values for the seeded objects
   (`expect-adopt-mirror.tsv`), including, for the drift probe, GitHub's description
   rather than the declared one; the GitHub snapshot after adoption equals `seeded`.
4. Delete the managed resources and assert GitHub still equals `seeded`.

## Safety

* `Membership` fixtures use `deletionPolicy: Orphan` (and adoption is Observe-only):
  deleting a Membership with the default policy removes the user from the
  organization.
* The upgrade scenario changes organization-wide settings and restores them from the
  baseline snapshot: the description, and the Actions policy and enabled-repository
  list. A run that is killed before cleanup leaves them changed; `oob.sh sweep`
  restores them from `$MIGRATION_WORKDIR/baseline.json`, and the next `prepare` removes
  leftover `pgh-mig-` objects but does not restore settings.
* Deleting is by prefix. A repository, team, variable, secret, runner group or
  webhook that starts with `pgh-mig-` / `PGH_MIG_` or `https://example.com/pgh-mig`
  in the test organization belongs to this harness.
* Nothing here is meant for an organization that holds anything that matters.

## Separation from the end-to-end suite

`check-separation.sh` (run by `make validate.migration.fixtures`) asserts that the
`make -n` output of `e2e` and every `e2e.<resource>` target never mentions the harness;
that no harness target begins with `e2e` or is a prerequisite of an `e2e`, `build` or
`uptest` target; that no `UPTEST_*` variable, CI workflow or file under `examples/`
refers to it; that no harness script reads `examples/`; and that nothing outside this
directory and the Makefile's `Migration Validation` block refers to it.

## Status of this harness

The offline parts (fixture validation, coverage, separation and the self-test) run
without a cluster or a GitHub call. The scenarios have not been run against the live
organization yet: the first run may surface GitHub-side validation differences (for
example the runner-group workflow restriction, which not every organization plan
accepts, or branch-protection actor lists) and build details of the baseline tag. Those
surface as a fixture that never becomes Ready, which the upgrade scenario reports and
excludes from the Ready-after-upgrade check unless `MIGRATION_REQUIRE_BASELINE_READY=1`.
