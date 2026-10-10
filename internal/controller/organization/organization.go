/*
Copyright 2022 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package organization

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/pkg/errors"
	pointer "k8s.io/utils/ptr"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/controller/driftcmp"
	"github.com/crossplane/provider-github/internal/controller/mgmtpolicy"

	"github.com/google/go-github/v90/github"
)

// repositoryCache provides a per-reconciliation cache for repository ID lookups
// to avoid repeated API calls for the same repositories
type repositoryCache struct {
	mu    sync.RWMutex
	cache map[string]int64 // repo name -> repo ID
	gh    *ghclient.Client
	org   string
}

// newRepositoryCache creates a new repository cache for the given organization
func newRepositoryCache(gh *ghclient.Client, org string) *repositoryCache {
	return &repositoryCache{
		cache: make(map[string]int64),
		gh:    gh,
		org:   org,
	}
}

// getRepositoryID gets a repository ID, using cache if available
func (rc *repositoryCache) getRepositoryID(ctx context.Context, repoName string) (int64, error) {
	// Check cache first (read lock)
	rc.mu.RLock()
	if id, exists := rc.cache[repoName]; exists {
		rc.mu.RUnlock()
		return id, nil
	}
	rc.mu.RUnlock()

	// Not in cache, fetch from API (write lock)
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Double-check in case another goroutine added it
	if id, exists := rc.cache[repoName]; exists {
		return id, nil
	}

	// Check for context timeout
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	// Fetch from GitHub API
	repo, _, err := rc.gh.Repositories.Get(ctx, rc.org, repoName)
	if err != nil {
		return 0, err
	}

	id := repo.GetID()
	rc.cache[repoName] = id
	return id, nil
}

// batchGetRepositoryIDs gets multiple repository IDs efficiently
func (rc *repositoryCache) batchGetRepositoryIDs(ctx context.Context, repoNames []string) ([]int64, error) {
	if len(repoNames) == 0 {
		return []int64{}, nil
	}

	repoIDs := make([]int64, 0, len(repoNames))
	for _, repoName := range repoNames {
		// Check for context timeout
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		id, err := rc.getRepositoryID(ctx, repoName)
		if err != nil {
			return nil, err
		}
		repoIDs = append(repoIDs, id)
	}
	return repoIDs, nil
}

const (
	errNotOrganization = "managed resource is not a Organization custom resource"
	errTrackPCUsage    = "cannot track ProviderConfig usage"
	errGetPC           = "cannot get ProviderConfig"

	errNewClient = "cannot create new Service"

	// visibilitySelected is the visibility of an organization secret shared with
	// a list of repositories.
	visibilitySelected = "selected"
)

type external struct {
	// A 'client' used to connect to the external resource API. In practice this
	// would be something like an AWS SDK client.
	github *ghclient.Client
}

//nolint:gocyclo
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotOrganization)
	}

	// A GitHub organization cannot be deleted through this provider, so
	// deleting the resource only removes the Kubernetes object. Reporting the
	// organization as absent once a deletion is requested lets the finalizer
	// clear; no GitHub call is made.
	if meta.WasDeleted(cr) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	name := meta.GetExternalName(cr)

	org, _, err := c.github.Organizations.Get(ctx, name)

	if ghclient.Is404(err) {
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.Status.AtProvider.ID = name
	cr.Status.AtProvider.Description = org.GetDescription()
	cr.Status.AtProvider.Actions.EnabledRepos = nil
	cr.Status.AtProvider.Secrets = nil

	// Every section is read and mirrored before a difference is reported, so
	// status.atProvider shows what GitHub holds for the sections after the first
	// one that differs. drift keeps the first difference, which is the message
	// Ready carries.
	var drift string
	differs := func(why string) {
		if drift == "" {
			drift = why
		}
	}

	// To use this function, the organization permission policy for enabled_repositories must be configured to selected, otherwise you get error 409 Conflict
	if cr.Spec.ForProvider.Actions.EnabledRepos != nil {
		repos, err := listEnabledReposInOrg(ctx, c.github, name)
		if err != nil {
			return managed.ExternalObservation{}, err
		}

		crARepos := getSortedEnabledReposFromCr(cr.Spec.ForProvider.Actions.EnabledRepos)
		aRepos := getSortedRepoNames(repos)
		cr.Status.AtProvider.Actions.EnabledRepos = enabledRepoObservations(aRepos)

		if !driftcmp.Equal(aRepos, crARepos) {
			differs(fmt.Sprintf("actions.enabledRepos: GitHub has %v, spec declares %v", aRepos, crARepos))
		}
	}

	if cr.Spec.ForProvider.Secrets != nil {
		cr.Status.AtProvider.Secrets = &v1alpha1.SecretConfigurationObservation{}
		if cr.Spec.ForProvider.Secrets.ActionsSecrets != nil {
			observed, why, isDrift, err := c.observeOrgSecrets(ctx, name, cr.Spec.ForProvider.Secrets.ActionsSecrets, c.github.Actions, drift != "")
			if err != nil {
				return managed.ExternalObservation{}, err
			}
			cr.Status.AtProvider.Secrets.ActionsSecrets = observed
			if isDrift {
				differs("secrets.actionsSecrets selected repository IDs (-spec +GitHub): " + why)
			}
		}
		if cr.Spec.ForProvider.Secrets.DependabotSecrets != nil {
			observed, why, isDrift, err := c.observeOrgSecrets(ctx, name, cr.Spec.ForProvider.Secrets.DependabotSecrets, c.github.Dependabot, drift != "")
			if err != nil {
				return managed.ExternalObservation{}, err
			}
			cr.Status.AtProvider.Secrets.DependabotSecrets = observed
			if isDrift {
				differs("secrets.dependabotSecrets selected repository IDs (-spec +GitHub): " + why)
			}
		}
	}

	// An omitted description (an Observe-only import) is not compared; under a
	// policy that writes, an empty description is a declared one.
	desc := cr.Spec.ForProvider.Description
	if (desc != "" || mgmtpolicy.WritesDeclared(cr.GetManagementPolicies())) && desc != pointer.Deref(org.Description, "") {
		differs(fmt.Sprintf("description: GitHub has %q, spec declares %q", pointer.Deref(org.Description, ""), desc))
	}

	if drift != "" {
		return drifted(cr, drift), nil
	}

	cr.SetConditions(xpv2.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// observeOrgSecrets reads the repositories GitHub gives each declared secret
// access to and returns them as the mirror. When the declared lists are not
// already known to differ (known is false), it also resolves the declared
// repositories to IDs and reports whether they differ from GitHub's, with the
// diff of the two.
func (c *external) observeOrgSecrets(ctx context.Context, org string, declared []v1alpha1.OrgSecret, getter OrgSecretGetter, known bool) (observed []v1alpha1.OrgSecretObservation, diff string, differs bool, err error) {
	gh, repos, err := getOrgSecretsWithConfig(ctx, getter, org, declared)
	if err != nil {
		return nil, "", false, err
	}
	observed = orgSecretObservations(declared, repos)
	if known {
		return observed, "", false, nil
	}
	spec, err := getOrgSecretsMapFromCr(ctx, c.github, org, declared)
	if err != nil {
		return nil, "", false, err
	}
	if driftcmp.Equal(spec, gh) {
		return observed, "", false, nil
	}
	return observed, cmp.Diff(spec, gh), true, nil
}

// drifted reports an existing Organization that differs from its spec. Ready is
// False until a later poll finds it in sync; the update that corrects the drift
// runs in the meantime. The Ready condition message names the field that
// differed and what each side held.
func drifted(cr *v1alpha1.Organization, why string) managed.ExternalObservation {
	cr.SetConditions(xpv2.Unavailable().WithMessage("drift: " + why))
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	_, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotOrganization)
	}

	return managed.ExternalCreation{}, errors.New("Creation of organizations not supported!")
}

//nolint:gocyclo
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotOrganization)
	}

	name := meta.GetExternalName(cr)
	gh := c.github

	// Observe's reads can differ from GitHub's state for a moment after an
	// earlier write. Every write below is therefore made only after reading
	// the current state again and finding it different, so a drift that is
	// already gone costs reads and no write: a write moves the updated_at of
	// the organization and of the secret it touches even when it changes
	// nothing.
	current, _, err := gh.Organizations.Get(ctx, name)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	if current == nil || pointer.Deref(current.Description, "") != cr.Spec.ForProvider.Description {
		req := &github.Organization{
			Description: &cr.Spec.ForProvider.Description,
		}
		if _, _, err := gh.Organizations.Edit(ctx, name, req); err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	if cr.Spec.ForProvider.Actions.EnabledRepos != nil {
		if err := setEnabledReposForActions(ctx, gh, name, cr); err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	secrets := cr.Spec.ForProvider.Secrets
	if secrets != nil {
		if secrets.ActionsSecrets != nil {
			err = updateOrgSecrets(ctx, gh, name, secrets.ActionsSecrets, gh.Actions, &ActionsSecretSetter{gh: gh})
			if err != nil {
				return managed.ExternalUpdate{}, err
			}
		}
		if secrets.DependabotSecrets != nil {
			err = updateOrgSecrets(ctx, gh, name, secrets.DependabotSecrets, gh.Dependabot, &DependabotSecretSetter{gh: gh})
			if err != nil {
				return managed.ExternalUpdate{}, err
			}
		}
	}

	return managed.ExternalUpdate{}, nil
}

// Delete issues no GitHub call: the organization is left untouched. Observe
// reports the resource as absent once a deletion is requested, which is what
// lets the finalizer clear.
func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.Organization)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotOrganization)
	}
	cr.Status.SetConditions(xpv2.Deleting())

	return managed.ExternalDelete{}, nil
}

func getSortedEnabledReposFromCr(repos []v1alpha1.ActionEnabledRepo) []string {
	crAEnabledRepos := make([]string, 0, len(repos))
	for _, repo := range repos {
		crAEnabledRepos = append(crAEnabledRepos, repo.Repo)
	}
	slices.Sort(crAEnabledRepos)
	return crAEnabledRepos
}

func getSortedRepoNames(repos []*github.Repository) []string {
	repoNames := make([]string, 0, len(repos))
	for _, repo := range repos {
		repoNames = append(repoNames, repo.GetName())
	}
	slices.Sort(repoNames)
	return repoNames
}

// listEnabledReposInOrg returns every repository enabled for Actions in
// the given organization, iterating through all pages. The single-page
// shape of github.Actions.ListEnabledReposInOrg silently truncates the
// result to one page, which produces a phantom diff against the CR's
// enabled-repos list and triggers idempotent re-adds on every reconcile
// when the org has more than 100 enabled repos.
func listEnabledReposInOrg(ctx context.Context, gh *ghclient.Client, org string) ([]*github.Repository, error) {
	opts := &github.ListOptions{PerPage: 100}
	var all []*github.Repository
	for {
		resp, httpResp, err := gh.Actions.ListEnabledReposInOrg(ctx, org, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Repositories...)
		if httpResp.NextPage == 0 {
			break
		}
		opts.Page = httpResp.NextPage
	}
	return all, nil
}

// setEnabledReposForActions reconciles the org's Actions-enabled repo list
// to match the CR spec using a single PUT call instead of per-repo
// Add/Remove. To avoid an unnecessary write when the diff Observe saw is
// unrelated to enabledRepos (e.g. only description or secrets drifted),
// it first lists the current state and skips the Set when names match.
func setEnabledReposForActions(ctx context.Context, gh *ghclient.Client, name string, cr *v1alpha1.Organization) error {
	crARepos := getSortedEnabledReposFromCr(cr.Spec.ForProvider.Actions.EnabledRepos)

	// To use this function, the organization permission policy for enabled_repositories must be configured to selected, otherwise you get error 409 Conflict
	repos, err := listEnabledReposInOrg(ctx, gh, name)
	if err != nil {
		return err
	}
	if driftcmp.Equal(crARepos, getSortedRepoNames(repos)) {
		return nil
	}

	repoCache := newRepositoryCache(gh, name)
	// Seed the cache from the list we already have. Without this, every
	// Update would re-fetch IDs for repos that are already enabled — a
	// burst of N Repositories.Get calls on an org with many enabled repos.
	for _, r := range repos {
		repoCache.cache[r.GetName()] = r.GetID()
	}
	ids, err := repoCache.batchGetRepositoryIDs(ctx, crARepos)
	if err != nil {
		return err
	}
	_, err = gh.Actions.SetEnabledReposInOrg(ctx, name, ids)
	return err
}

func getOrgSecretsMapFromCr(ctx context.Context, gh *ghclient.Client, org string, secrets []v1alpha1.OrgSecret) (map[string][]int64, error) {
	crOrgSecretsToConfig := make(map[string][]int64, len(secrets))

	// Create repository cache for this function to avoid repeated lookups
	repoCache := newRepositoryCache(gh, org)

	for _, secret := range secrets {
		// Check for context timeout before processing each secret
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Collect repository names for batch lookup
		repoNames := make([]string, 0, len(secret.RepositoryAccessList))
		for _, selectedRepo := range secret.RepositoryAccessList {
			repoNames = append(repoNames, selectedRepo.Repo)
		}

		// Batch lookup repository IDs using cache
		repoIds, err := repoCache.batchGetRepositoryIDs(ctx, repoNames)
		if err != nil {
			return nil, err
		}

		sort.Slice(repoIds, func(i, j int) bool {
			return repoIds[i] < repoIds[j]
		})
		crOrgSecretsToConfig[secret.Name] = repoIds
	}
	return crOrgSecretsToConfig, nil
}

type OrgSecretGetter interface {
	GetOrgSecret(ctx context.Context, owner, secretName string) (*github.Secret, *github.Response, error)
	ListSelectedReposForOrgSecret(ctx context.Context, owner, secretName string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
}

func getOrgSecretsWithConfig(ctx context.Context, c OrgSecretGetter, owner string, secrets []v1alpha1.OrgSecret) (map[string][]int64, map[string][]string, error) {
	orgSecretsToConfig := make(map[string][]int64, len(secrets))
	orgSecretRepos := make(map[string][]string, len(secrets))
	for _, secret := range secrets {
		// Check for context timeout before processing each secret
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		ghSecret, _, err := c.GetOrgSecret(ctx, owner, secret.Name)
		if err != nil {
			return nil, nil, err
		}
		repoIds := make([]int64, 0)
		repoNames := make([]string, 0)
		if ghSecret != nil && ghSecret.Visibility == visibilitySelected {
			opts := &github.ListOptions{PerPage: 100}
			for {
				// Check for context timeout in pagination loop
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				default:
				}

				ghRepo, resp, err := c.ListSelectedReposForOrgSecret(ctx, owner, secret.Name, opts)
				if err != nil {
					return nil, nil, err
				}
				for _, selectedRepo := range ghRepo.Repositories {
					repoIds = append(repoIds, selectedRepo.GetID())
					repoNames = append(repoNames, selectedRepo.GetName())
				}
				if resp.NextPage == 0 {
					break
				}
				opts.Page = resp.NextPage
			}
			sort.Slice(repoIds, func(i, j int) bool {
				return repoIds[i] < repoIds[j]
			})
		}
		orgSecretsToConfig[secret.Name] = repoIds
		orgSecretRepos[secret.Name] = repoNames
	}
	return orgSecretsToConfig, orgSecretRepos, nil
}

// enabledRepoObservations lists the repositories named in names.
func enabledRepoObservations(names []string) []v1alpha1.ActionEnabledRepoObservation {
	out := make([]v1alpha1.ActionEnabledRepoObservation, 0, len(names))
	for _, n := range names {
		out = append(out, v1alpha1.ActionEnabledRepoObservation{Repo: n})
	}
	return out
}

// orgSecretObservations lists the declared secrets with the names of the
// repositories GitHub gives access to each, sorted.
func orgSecretObservations(declared []v1alpha1.OrgSecret, repos map[string][]string) []v1alpha1.OrgSecretObservation {
	out := make([]v1alpha1.OrgSecretObservation, 0, len(declared))
	for _, s := range declared {
		names := slices.Clone(repos[s.Name])
		slices.Sort(names)
		access := make([]v1alpha1.SecretSelectedRepoObservation, 0, len(names))
		for _, n := range names {
			access = append(access, v1alpha1.SecretSelectedRepoObservation{Repo: n})
		}
		out = append(out, v1alpha1.OrgSecretObservation{Name: s.Name, RepositoryAccessList: access})
	}
	return out
}

type OrgSecretSetter interface {
	SetSelectedReposForOrgSecret(ctx context.Context, org string, name string, ids []int64) error
}

type ActionsSecretSetter struct {
	gh *ghclient.Client
}

type DependabotSecretSetter struct {
	gh *ghclient.Client
}

func (a *ActionsSecretSetter) SetSelectedReposForOrgSecret(ctx context.Context, org string, name string, ids []int64) error {
	_, err := a.gh.Actions.SetSelectedReposForOrgSecret(ctx, org, name, ids)
	if err != nil {
		return err
	}
	return nil
}

func (d *DependabotSecretSetter) SetSelectedReposForOrgSecret(ctx context.Context, org string, name string, ids []int64) error {
	_, err := d.gh.Dependabot.SetSelectedReposForOrgSecret(ctx, org, name, ids)
	if err != nil {
		return err
	}
	return nil
}

// updateOrgSecrets sets the selected repositories of each declared secret to
// the declared list. A secret whose selected repositories on GitHub already
// are that list is left alone.
func updateOrgSecrets(ctx context.Context, gh *ghclient.Client, owner string, secrets []v1alpha1.OrgSecret, getter OrgSecretGetter, setter OrgSecretSetter) error {
	declared, err := getOrgSecretsMapFromCr(ctx, gh, owner, secrets)
	if err != nil {
		return err
	}
	current, _, err := getOrgSecretsWithConfig(ctx, getter, owner, secrets)
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		if driftcmp.Equal(declared[secret.Name], current[secret.Name]) {
			continue
		}
		// getOrgSecretsMapFromCr sorts; the request carries the declared IDs.
		if err := setter.SetSelectedReposForOrgSecret(ctx, owner, secret.Name, declared[secret.Name]); err != nil {
			return err
		}
	}
	return nil
}

// Disconnect is a no-op: the GitHub client is cached and shared across
// reconciles, so there is nothing to release per connection.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
