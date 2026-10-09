# provider-github

`provider-github` is a [Crossplane](https://crossplane.io/) Provider
that is meant to be used to manage github organizations.

The project is in a prototyping phase but it's already functional and
implements the following resources with partial functionality:

* **Organization** — manages an existing organization (creation and deletion are not supported)
  * description
  * Actions enabled repositories
  * Actions secrets — repository access (secret values are set out-of-band)
  * Dependabot secrets — repository access (secret values are set out-of-band)
* **OrganizationVariable** — Actions variables at the organization level
  * value
  * visibility (`all`, `private`, `selected`)
  * selected repositories
* **OrganizationWebhook** — organization-level webhooks
  * url, content type, events, active flag, SSL verification, secret (from a Kubernetes Secret)
* **ActionsSecretAccess** — repository access to an existing Actions organization secret (the secret and its value are managed out-of-band)
  * deleting the resource leaves the secret and its repository access unchanged on GitHub, whatever `deletionPolicy` is set to
* **DependabotSecretAccess** — repository access to an existing Dependabot organization secret (the secret and its value are managed out-of-band)
  * deleting the resource leaves the secret and its repository access unchanged on GitHub, whatever `deletionPolicy` is set to
* **RunnerGroup** — Actions self-hosted runner groups at the organization level (runners themselves are not managed)
  * visibility (`all`, `selected`, `private`), selected repositories, public repository access, workflow restrictions
* **Repository**
  * description, visibility (private/public), topics, template flag
  * default branch
  * feature toggles — Issues, Projects, Wiki, Discussions
  * merge strategies — merge / squash / rebase / auto-merge / update-branch / delete-branch-on-merge
  * merge commit format (title and body, for both merge and squash-merge)
  * creation from a template repository or as a fork
  * user (collaborator) permissions
  * team permissions
  * webhooks
  * branch protection rules (including required status checks, required reviews, restrictions, signed commits)
  * repository rulesets
  * archive-on-delete safeguard
* **Team**
  * description
  * visibility (`secret`, `closed`)
  * members
  * parent team
* **Membership** — organization membership
  * role

## GitHub App permissions

See [PERMISSIONS.md](./PERMISSIONS.md) for the GitHub App permissions
the provider requires, including a per-resource breakdown for
least-privilege setups.

## Operational considerations

### `--reconcile-timeout`

The provider takes a `--reconcile-timeout` flag that bounds the time any
single managed-resource reconcile (Observe + Create/Update/Delete) is
allowed to run. The runtime cancels the reconcile's context when the
deadline is reached.

The `Organization` CR's `spec.forProvider.actions.enabledRepos` field
lists the repositories that are allowed to run GitHub Actions at the
organization level (this maps to GitHub's `selected_repository_ids`
list under `Actions › General › Allow select repositories` in the org
settings, and to the `actions/permissions/repositories` REST API).

The field is **tri-state** — omitting it leaves the org's enabled list
unmanaged, setting it to `[]` explicitly wipes the list (disabling
Actions for every repo in the org), and a non-empty list reconciles
the list to exactly those repositories. See the field's CRD docstring
(`kubectl explain organization.spec.forProvider.actions.enabledRepos`)
for the full contract.

The first two timeout-sensitive scenarios below involve reconciling
that list.

**Default: `1m`.** Suitable for steady-state reconciliation and small
spec changes. The default is **not** enough for three operational
scenarios:

1. **Cold bootstrap of an `Organization` CR with a large Actions
   enabled-repos list.** On first reconcile the provider issues one
   sequential `Repositories.Get` per repo in `actions.enabledRepos`
   that is not already enabled in GitHub, to resolve names to numeric
   IDs. At ~0.5s per call through the rate-limit-aware client this is
   roughly `N × 0.5s`. An org bootstrapping with 200 enabled repos
   takes ~100s — past the 60s budget — so the reconcile will time out
   and retry on the next cycle. The system is **self-healing** across
   cycles (each retry resolves a smaller remaining diff) but doesn't
   converge in one shot.

2. **Bulk spec edits to the Actions enabled-repos list.** Adding or
   replacing tens of repos in an existing `Organization`'s
   `actions.enabledRepos` triggers the same per-new-repo
   `Repositories.Get` burst. Removals do not — repo IDs for
   already-enabled repos come back free from the paginated
   `ListEnabledReposInOrg` walk that the controller does anyway.

3. **Adding many repositories at once to an `ActionsSecretAccess` or
   `DependabotSecretAccess`.** Each repository in
   `selectedRepositories` that the secret does not already have costs
   one sequential `Repositories.Get`; repositories already selected
   cost none. The list is written in a single call after all IDs are
   resolved, so a reconcile that times out makes no progress and the
   next one starts over. Adding more than roughly 100 new
   repositories to one secret in a single change needs a larger
   `--reconcile-timeout` (for example `--reconcile-timeout=3m`).

Steady-state reconciles (no spec drift, or drift confined to
description/secrets) cost a handful of API calls regardless of org
size and fit comfortably in the default.

**Raising it.** Pass `--reconcile-timeout=3m` (or whatever fits your
worst-case bootstrap) on the provider deployment. A 3-minute budget
covers ~300 sequential ID resolutions plus the usual overhead. Pick the
smallest value that contains your operational worst case — larger
timeouts let stuck reconciles hold a worker for longer before the
runtime cancels them, which reduces overall throughput across all
controllers.

**Detecting it.** A reconcile that hits the deadline surfaces as
`Synced=False` with `reason=ReconcileError, message="... context
deadline exceeded"` on the CR's status, and a `Warning` event of type
`CannotUpdateExternalResource`. If you see this on an `Organization`
right after a large change to its Actions enabled-repos list, the
timeout is the most likely cause; let the controller retry a couple
of cycles before raising the flag.

### Repository `description` is always managed

The provider keeps a repository's GitHub description in sync with
`spec.forProvider.description`, including when that field is empty.
There is no "unmanaged" state — a `Repository` CR that omits or
empties `description` resets the repository's description on GitHub
**to empty**.

Earlier releases enforced this only intermittently; it now applies
on every reconcile. Before upgrading, make sure each `Repository`
CR declares the description you want — otherwise the next reconcile
will clear it.


### crossplane-runtime v2: removed features and always-on management policies

The provider is built on crossplane-runtime v2. Compared with earlier releases:

* **External Secret Stores are gone.** The `StoreConfig` kind, the
  `--enable-external-secret-stores` flag and every managed resource's
  `spec.publishConnectionDetailsTo` field are removed. Connection details are
  published only through `spec.writeConnectionSecretToRef`. Remove
  `publishConnectionDetailsTo` from existing manifests and delete any
  `StoreConfig` objects before upgrading.
* **Management policies are always on.** `spec.managementPolicies` is honored
  on every managed resource, and the `--enable-management-policies` flag (and
  its `ENABLE_MANAGEMENT_POLICIES` environment variable) no longer exists.
  Remove the flag from any `DeploymentRuntimeConfig` or Deployment arguments,
  because an unknown flag stops the provider from starting. The same holds for
  `--enable-external-secret-stores` and `--namespace`.
* **The deprecated `spec.providerRef` field is gone** from every managed
  resource. Use `spec.providerConfigRef`.
* Managed resources report `status.observedGeneration` and
  `status.lastHandledReconcileAt`, and `status.conditions` entries report
  `observedGeneration`.
* `ProviderConfig` carries the `crossplane`, `provider` and `github`
  categories, so `kubectl get crossplane` also lists it.
* The Go API packages moved to `apis/cluster/...`
  (`github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1`
  and `.../apis/cluster/v1alpha1`) and four exported fields were renamed to Go
  initialism style: `Url` to `URL`, `InsecureSsl` to `InsecureSSL`, `ActorId`
  to `ActorID` and `IntegrationId` to `IntegrationID`. Their JSON names are
  unchanged.


## Developing

To add a new resource follow these steps:
1. Run `make submodules` to initialize the "build" Make submodule we use for CI/CD.
2. Add your new type by running the following command:
```shell
  export group=sample # lower case e.g. core, cache, database, storage, etc.
  export type=MyType # Camel casee.g. Bucket, Database, CacheCluster, etc.
  make provider.addtype provider=GitHub group=${group} kind=${type}
```
3. Call the `Setup` function of your controller here `internal/controller/github.go`
4. Run `make run` to run locally
5. Run `make reviewable` to run code generation, linters, and tests.
6. Run `make build` to build the provider.

Refer to Crossplane's [CONTRIBUTING.md] file for more information on how the
Crossplane community prefers to work. The [Provider Development][provider-dev]
guide may also be of use.

[CONTRIBUTING.md]: https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md
[provider-dev]: https://github.com/crossplane/crossplane/blob/master/contributing/guide-provider-development.md
