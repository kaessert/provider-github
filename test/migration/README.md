# Migration validation

A one-off validation of moving an existing installation from the last upstream
release to this tree. It answers three questions against a real GitHub
organization:

1. **In-place upgrade.** Take a cluster running the baseline provider with a full
   set of managed resources, point the Provider at this tree's build. Is anything
   recreated or written?
2. **Adoption of what the baseline created, cluster-scoped kinds.** The baseline
   creates every fixture object and is removed, its objects orphaned. Can this
   tree's cluster-scoped managed resources adopt them with `managementPolicies:
   [Observe]`, fill `status.atProvider` from GitHub (nested sub-objects included),
   and write nothing? Can the same objects then be switched to full management
   with zero writes, and changed once per kind with exactly that change reaching
   GitHub?
3. **Adoption into the namespaced kinds.** The same flow as 2, into the namespaced kinds
   in `organizations.github.m.crossplane.io`: through a namespaced `ProviderConfig` in
   one namespace and a `ClusterProviderConfig` in another, with references between
   objects of one namespace, and with a record of what a cluster-scoped and a namespaced
   object do when both manage one GitHub object.

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
to fork, default `actions/hello-world-docker-action`, chosen because it carries `.github/workflows/ci.yml`, which the runner-group workflow fixture names), `MIGRATION_WORKDIR` (evidence and scratch,
default `$TMPDIR/provider-github-migration` for the scenarios; the offline steps `fixtures`, `coverage`, `separation` and `selftest`, which `make validate.migration.fixtures` runs, use a private `mktemp -d` directory when it is unset, so concurrent runs from several worktrees do not share results files; it is removed when the step passes and kept, with its path printed, when it fails; an explicit value is used as given), `MIGRATION_POLL` (provider `--poll`,
default `60s`: at 15s a run exhausted the GitHub App installation's 5000 requests an hour), `MIGRATION_READY_TIMEOUT`, `MIGRATION_BASELINE_REF`,
`MIGRATION_MIN_RATE_BUDGET` (GitHub requests that must be left in the hour for `adopt-cluster` and `adopt-namespaced` to start,
default `3500`, `0` disables the check), `MIGRATION_BASELINE_REPO`, `MIGRATION_BASELINE_DIR` (reuse a checkout of the
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
| `fixtures/adopt/cluster/`, `fixtures/adopt/namespaced/` | hand-written Observe-only adoption fixtures, one set per scope. No scenario applies them since the full flow derives its manifests from `fixtures/v1/`; they are still validated against the candidate CRDs |
| `coverage.tsv`, `check-coverage.sh` | the kind x sub-object coverage table and its checker |
| `validate-fixtures.sh` | fixtures against the CRDs, offline |
| `snapshot.sh` | GitHub state snapshot and diff |
| `oob.sh` | out-of-band setup, seeding and cleanup through the GitHub API |
| `cluster.sh` | builds, control plane, local package and image loading, managed-resource state |
| `upgrade-compare.sh` | the upgrade scenario's comparisons: which objects the GitHub comparison narrows, the Kubernetes regression check with its re-read, and the attribution of `updated_at` moves |
| `scenario.sh`, `scenario-upgrade.sh`, `scenario-adopt-cluster.sh`, `scenario-adopt-namespaced.sh`, `adopt-common.sh` | the three drivers and what they share (`adopt-common.sh` holds the full adoption flow, `run_adopt_full <cluster\|namespaced>`) |
| `adopt-unsynced.sh` | which baseline objects are adopted (what GitHub holds, not Synced alone), the one completing update of an object the baseline never finished, and the "GitHub holds none" rows |
| `adopt-derive.sh` | derives the adoption manifests from the v1 fixtures (both scopes), the reference twins, the cluster-scoped twins and the both-scopes Teams; the evaluation helpers of the expectation tables and the both-scopes verdicts |
| `expect-adopt-nested.tsv` | what `status.atProvider` must report for every nested sub-object of `coverage.tsv` and for the fields an Observe-only object omits, against the GitHub snapshot (scenarios (b) and (c)) |
| `expect-adopt-change.tsv` | the one deliberate change per kind of scenarios (b) and (c), and what it may change on GitHub |
| `expect-adopt-mirror.tsv` | what `status.atProvider` must report for the objects the old API-seeded adoption created; unused since the full flow |
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
  namespaced scope and nothing else;
* the manifests the adoption scenarios derive, in both scopes, against the candidate
  CRDs and their CEL rules: the namespaced ones sit in two namespaces, one per kind of
  ProviderConfig, read their secrets from their own namespace and carry no
  `deletionPolicy` (a namespaced kind has none); the reference twins hold `*Ref` and
  `*Selector` fields in place of plain strings, the cross-namespace twin names an object of
  the other namespace; there is one cluster-scoped twin per kind; the two Teams of the
  both-scopes probe name one GitHub team.

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
* **adopt** (no scenario uses it any more; scenarios (b) and (c) use the `upgrade` setup,
  because the baseline provider creates its own objects): seeds one object of every
  adoptable kind: a repository, a parent and a child team (with the member) and a team
  that will drift, a variable, an organization webhook, a runner group and two secrets,
  and records the webhook's numeric ID.

## Snapshot tool

```sh
test/migration/snapshot.sh dump out.json
test/migration/snapshot.sh diff before.json after.json [--ignore-timestamps]
test/migration/snapshot.sh ids out.json
```

`dump` issues GET requests only and records, for everything under the prefixes above,
the numeric ID and every managed setting: repository settings, topics, fork and
template origin, collaborators, team access, webhooks (GitHub masks the secret, so its
presence shows), branch protection and rulesets; environments are recorded as
`unreadable` (the App can create an environment but GitHub refuses to list them with
403, so no environment is compared, and no error is logged); teams with parent and
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
     timestamps (**no recreate, no write**). Two kinds of object are narrowed first,
     because the candidate completes work the baseline never did, and each is reported as
     INFO: an organization variable that is not Synced on the baseline (it never existed on
     GitHub, so it is left out), and a repository listed as not settled on the baseline
     (its numeric IDs are still compared; its settings, branch protection and own
     timestamp are reported as a delta in `unreconciled-<repository>-delta.txt`). An
     object that was Synced and Ready on the baseline is never narrowed;
   * no managed resource was deleted, recreated, renamed, or lost Ready/Synced. An object
     that was Ready and is read as `Ready=False`, `Synced=True` with a Ready message starting
     `drift:` is read again after one poll period and fails only if it is still not Ready;
   * `status.atProvider.id` is populated on the seven kinds that gain it, and the
     numeric ID of `OrganizationWebhook` and `RunnerGroup` is still GitHub's;
   * the webhook's `writeConnectionSecretToRef` secret is byte-identical;
   * the candidate's logs contain no panic.
5. `report.md` lists every `updated_at` that moved between the two snapshots and says whether
   the candidate's log has a create or update line for the managed resource that owns it. A
   move on an object with no such line was made by something else; the strict timestamp
   assertion is left as it is.
6. Record, without asserting: what happens to `spec.publishConnectionDetailsTo`, the
   deprecated `spec.providerRef`, the externally published secret, and the objects
   with non-default management policies and `Orphan` deletion.

### (b) `adopt-cluster`

The full adoption flow: everything the baseline created is adopted, then fully managed.

1. Build the baseline tag and this tree; the `upgrade` setup (above).
2. **The baseline creates.** Install the baseline Provider, apply the `v1` fixtures,
   wait for Synced and Ready, let it run two poll cycles, snapshot GitHub (`v1`) and the
   managed resources. What is adopted is decided by what GitHub holds, not by Synced alone
   (`adopt-unsynced.sh`): a fixture that is not Synced and that the `v1` snapshot does not
   hold (for example a spec the baseline rejects) was never created; it is reported and left
   out of the adoption (`MIGRATION_REQUIRE_BASELINE_READY=1` fails instead). A fixture that is
   Synced but not Ready exists on GitHub: it is adopted like the others and its baseline state
   is shown in the per-object table, so a fix for the baseline's problem with it is proven on
   the adopted object. A fixture that is NOT Synced but that the snapshot holds is adopted too,
   listed with the message the baseline gave (`v1-unsynced-adopted.txt`) and shown with it in
   the per-object table's "On the baseline" column. `Repository/pgh-mig-repo-main` is the case
   that matters: the baseline creates the repository, its webhook and its ruleset, then reports
   Synced=False (`observe failed: branch is not protected`) because the default branch is
   protected by the ruleset and not by classic branch protection, and the repository holds most
   of the nested sub-objects, so leaving it out would leave them unevaluated. The key of an
   object in the snapshot is its external name (a webhook's URL); a webhook the baseline never
   recorded takes the hook ID GitHub holds.
3. **Orphan.** Give every v1 managed resource `deletionPolicy: Orphan` (the run stops
   before any delete if one lacks it), delete them, snapshot (`orphaned`): GitHub must be
   identical to `v1`. Then leave the baseline the way a user must: delete the baseline's
   `ProviderConfig` objects (the credentials Secret stays) while its Provider still runs,
   uninstall the Provider, and wait until every CRD of the baseline package is gone; one
   that remains fails the run and is named. A `ProviderConfig` left in place when the
   Provider is uninstalled keeps its in-use finalizer, so the CRD of its kind stays, the old
   package revision keeps control of it and the next Provider never becomes Healthy
   (`cannot establish control of object: providerconfigs.github.crossplane.io is already
   controlled by ProviderRevision ...`). That ordering is a migration note for a user moving
   from the baseline to another build. Then install the candidate.
4. **Observe-only adoption.** For every v1 object, one NEW cluster-scoped managed resource
   is derived from its fixture (`adopt-derive.sh`):
   * `crossplane.io/external-name` from the live baseline object, which is the object's
     name for every kind but `Organization` (the login) and `OrganizationWebhook` (the
     numeric hook ID, set by the baseline when it created the hook). A `RunnerGroup` is
     found by name; its numeric ID is `status.atProvider.id`;
   * `managementPolicies: [Observe]`;
   * the ten create-time fields the candidate lets an Observe-only object omit are left
     out (Organization `description`, Membership `role`, variable `value` and
     `visibility`, webhook `url`, `contentType` and `events`, runner group and
     SecretAccess `visibility`; `validate-fixtures.sh` checks the list against the CEL
     rules of the candidate CRDs); everything else, nested sub-objects included, is the v1
     `forProvider`;
   * the fields the move to crossplane-runtime v2 removes (`publishConnectionDetailsTo`,
     `providerRef`) are dropped.

   Before the manifests are applied, the connection secrets the baseline wrote (the
   `*-conn` Secrets its objects named in `writeConnectionSecretToRef`, kept before the
   objects were deleted) are applied again for every manifest that names one, in the
   namespace it names. The scenario thereby models a user who keeps the applied-secret
   records (see the migration note below); scenario (c) does the same into the namespace
   of each object.

   A drift probe is added: a second Observe-only `Team` over the parent team, whose
   declared description differs from GitHub's. Assert: every object Synced and Ready (the
   probe Synced and not Ready); UIDs new; `status.atProvider.id` equals the external name
   (the webhook's is GitHub's hook ID, the runner group's GitHub's group ID);
   `status.atProvider` against the snapshot for every row of `expect-adopt-nested.tsv`;
   the provider log shows each object reconciled and no create or update; the GitHub
   snapshot (`adopted`) is identical to `orphaned`, timestamps included. A sub-object that
   GitHub holds none of on the baseline (the classic branch protection of
   `pgh-mig-repo-main`), and that `status.atProvider` reports none of either, is "GitHub
   holds none" (not a mismatch); `status.atProvider` reporting something GitHub lacks is still
   a failure.
   The observation fills a selected-repositories list only while the spec declares
   `visibility: selected`, so those four rows are reported as not mirrored here (they are
   asserted in step 5) rather than as failures.
5. **Full management.** Apply the v1 `forProvider` of the same objects (the removed fields
   dropped) under the default management policies. Wait for Synced and Ready, let the
   provider run `MIGRATION_SETTLE_POLLS` cycles, snapshot (`full`). Assert: GitHub is
   identical to `adopted` (the switch and the cycles after it wrote nothing), the provider
   log shows no create or update, the same per-object and per-row checks as in step 4 now
   with every list mirrored. An object adopted although the baseline did not report it Synced
   may issue exactly ONE update (INFO, named, attributed to the baseline never finishing it)
   and no create: the candidate finishes what the baseline could not. That update's writes
   are left out of the snapshot comparison (the sub-objects the `v1` snapshot held none of,
   and the repository's `updated_at`; IDs and every other value stay compared) and the
   sub-objects the manifest declares must exist afterwards, else the row fails. A second
   update, a create, an update under Observe and the same update of any other object are
   failures.
6. **One deliberate change per kind** (`expect-adopt-change.tsv`): a description, a
   variable value, a webhook event, a repository list. Wait until `status.atProvider`
   shows it and every object is Synced and Ready again, snapshot (`changed`). Assert: the
   changed value is on GitHub; every other value that differs between `full` and `changed`
   is allowed by the row of its kind (nothing else changed; a timestamp that moved on an
   object whose values did not change is reported as a rewrite); each changed object issued
   an update and no other object issued a create or an update. `Membership` is the one
   kind with no deliberate change: its only mutable field is the user's role in the
   organization.
7. Cleanup deletes the managed resources (the default policies delete the GitHub objects;
   the Membership keeps `Orphan`), removes the Provider and sweeps.

**Migration note: keep the connection secrets of webhooks that use `secretKeyRef`.** A
webhook's secret cannot be read back from GitHub (it is masked), so the controller compares
the secret in the spec against the applied value recorded in the object's
`writeConnectionSecretToRef` Secret. That Secret is owned by the managed resource and is
garbage-collected when the old object is deleted. Without it the adopting object reports
Ready=False under Observe for the organization webhooks and for a Repository whose
`webhooks[]` use `secretKeyRef` (Synced stays True), and the first full-management reconcile
re-applies the hook once, rewriting it with the same values and recording the secret again. A run of this scenario that did not restore the
Secrets measured exactly that: each of the three objects issued one Update and nothing else
changed. Keep the Secrets and the objects are Ready from the first reconcile.

`report.md` ends with the tables: per managed resource (phase, Ready, Synced,
`status.atProvider.id`, external name, whether they agree, creates and updates in the
provider log and the reconciles that make zero meaningful), per nested sub-object
(phase, result, the two values when they differ), per deliberate change, and one row for every
object that is not Ready with its Ready reason and message and its Synced message. A run takes
about an hour and polls dozens of objects, which is why it checks the request budget
first. (c) adds the reference and both-scopes steps to that, so it needs the budget as well.

### (c) `adopt-namespaced`

The full adoption flow of (b), into the namespaced kinds, plus three probes that only the
namespaced scope has. Steps 1 to 3 are (b)'s: the baseline creates, the objects are
orphaned, the candidate is installed.

4. **Two ways to reach GitHub.** Two namespaces, each with the credentials its path reads:
   * `pgh-mig-ns-a` holds a namespaced `ProviderConfig` (`github.m.crossplane.io`,
     `pgh-mig-ns-pc`) whose Secret is in `pgh-mig-ns-a`: a namespaced ProviderConfig reads its Secret
     from its own namespace;
   * `pgh-mig-ns-b` holds no ProviderConfig and no credentials Secret; its objects name a
     `ClusterProviderConfig` (`pgh-mig-ns-cpc`) whose Secret is in `crossplane-system`.

   Every v1 object is adopted by one NEW namespaced managed resource, derived as in (b) and
   moved into one of the two namespaces (`adopt_target` in `adopt-derive.sh`): the organization,
   the membership, the three teams and two repositories go to `pgh-mig-ns-a`, everything else to
   `pgh-mig-ns-b`. A namespaced kind has no `deletionPolicy` and reads the webhook and connection
   secrets from its own namespace, so the derivation drops the field (an object the baseline
   kept with `Orphan`, a Membership above all, leaves `Delete` out of its management policies
   instead), reduces `writeConnectionSecretToRef` to its name, and the scenario copies the
   connection secrets the baseline wrote (the applied webhook secrets) into the namespaces. Assert
   what (b) asserts (Synced, Ready, `atProvider.id`, `status.atProvider` per nested sub-object,
   zero writes, the GitHub snapshot unchanged), and in addition that every object is on the path
   it was given and that every object reaching GitHub through each path is Synced and Ready.
5. **References.** Five Observe-only twins over objects of `pgh-mig-ns-a` (and one of
   `pgh-mig-ns-b`), each holding its references as `*Ref` and `*Selector` fields in place of the
   plain strings of the object it twins:

   | Twin | Fields |
   |---|---|
   | `pgh-mig-ref-team` (Team) | `orgRef`, `parentRef`, `members[].userRef` |
   | `pgh-mig-ref-membership` (Membership) | `orgRef` |
   | `pgh-mig-ref-org` (Organization) | `repoRef` in the Actions and the secrets repository lists |
   | `pgh-mig-ref-repo` (Repository) | `orgSelector`, `permissions.teams[].teamSelector`, `permissions.users[].userSelector` |
   | `pgh-mig-ref-xns-var` (OrganizationVariable, in `pgh-mig-ns-b`) | `orgRef` naming the Organization of `pgh-mig-ns-a` |

   Assert: the four twins in the namespace of their targets are Synced and Ready and the resolver
   has filled `spec.forProvider.<field>` with the value the plain-string object declares; the
   cross-namespace twin is Synced=False and its field stays unset (a reference resolves inside
   the namespace of the object that holds it); no twin writes. The twins are deleted afterwards.
6. **Both scopes, Observe-only.** The cluster-scoped Observe-only twin of one object per kind
   (same external name, named `pgh-mig-cl-<object>`) is applied while the namespaced object is
   Ready. Recorded, not asserted: Ready, Synced and message of each, what each controller did,
   whether GitHub changed (snapshot `twins` against `adopted`, timestamps included).
7. **Full management and one deliberate change per kind**, as in (b) (`Membership` is waived).
8. **Both scopes, full management.** Guarded: one disposable Team (`pgh-mig-both-team`, removed by
   the sweep) is created by a namespaced object, then adopted by a cluster-scoped one that declares
   another description. Both have full management of the description, neither deletes the team.
   Recorded after `MIGRATION_SETTLE_POLLS` poll cycles: the updates each controller issued, the
   description on GitHub and in each status. One GitHub object is managed by exactly one managed
   resource, in exactly one scope; naming it from two scopes is an invalid configuration (Crossplane
   has never guarded it for two cluster-scoped resources either), so the predicted outcome is that
   both controllers write and overwrite each other. The step asserts that: both wrote is a PASS, and
   the numbers are still reported. A run in which only one side wrote, or in which neither wrote and
   a side is not `Synced=True` (a guard refused the second scope, and the holder sees no drift), is a
   WARN (verdicts `ONE SCOPE WROTE` and `GUARDED`), because it means cross-scope behaviour exists that
   is not documented; one in which neither wrote and both are `Synced=True` is recorded as
   inconclusive. `MIGRATION_BOTH_SCOPES_WRITE=0` skips this step.
9. Cleanup deletes the managed resources of both groups, the namespaces and the
   ClusterProviderConfig, removes the Provider and sweeps.

`report.md` carries the tables of (b) with the ProviderConfig path added to the per-object and
per-sub-object tables (one row per `coverage.tsv` row and path), a per-path summary, the reference
table (declared value against the value the resolver filled in, per field), the both-scopes table
and a plain statement whether the provider has a guard that stops two scopes managing one object.

## Safety

* `Membership` fixtures use `deletionPolicy: Orphan` (the derived fully managed Membership
  keeps it, and Observe-only adoption never deletes): deleting a Membership with the
  default policy removes the user from the organization.
* The upgrade scenario changes organization-wide settings and restores them from the
  baseline snapshot: the description, and the Actions policy and enabled-repository
  list. A run that is killed before cleanup leaves them changed; `oob.sh sweep`
  restores them from `$MIGRATION_WORKDIR/baseline.json`, and the next `prepare` removes
  leftover `pgh-mig-` objects but does not restore settings.
* Let a running scenario finish. Stopping it (`kill`, Ctrl-C of the make
  target's process group) ends the driver WITHOUT running its cleanup: the GitHub
  objects stay and the organization settings are not restored. After any stop, run
  `oob.sh sweep`.
* The scenarios, a `snapshot.sh dump` and every `make e2e*` run share the
  installation's 5000 requests per hour on the `pgh-test` organization. A dump reads
  dozens of objects and a Repository observe is dozens of requests, which is why the
  default poll is 60s. Do not run two of them at once; once the limit is exhausted
  (`403 API rate limit of 5000 exceeded`) every call fails until the hour rolls over.
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
without a cluster or a GitHub call. They check the manifests `adopt-cluster` and `adopt-namespaced` derive
against the candidate CRDs and their CEL rules, and the expectation tables against the
fixtures and the stand-in API; what GitHub itself reports for a nested sub-object (the
shape of a branch protection or a ruleset) is known only from a live run, so the first run
of `adopt-cluster` or `adopt-namespaced` may fail a row whose expectation is mis-stated rather than the provider.
`adopt-namespaced` also depends on what the resolver and the controllers log: the write counts read the
provider's debug lines for the namespaced controllers (`managed/<kind>.organizations.github.m.crossplane.io`,
the request carrying the namespace), which the self-test checks against the format of the cluster-scoped ones.
The first run of the other scenarios may surface GitHub-side validation differences (for
example the runner-group workflow restriction, which not every organization plan
accepts, or branch-protection actor lists) and build details of the baseline tag. Those
surface as a fixture that never becomes Ready, which the upgrade scenario reports and
excludes from the Ready-after-upgrade check unless `MIGRATION_REQUIRE_BASELINE_READY=1`.
