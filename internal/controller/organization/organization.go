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
	"reflect"
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

	// To use this function, the organization permission policy for enabled_repositories must be configured to selected, otherwise you get error 409 Conflict
	if cr.Spec.ForProvider.Actions.EnabledRepos != nil {
		repos, err := listEnabledReposInOrg(ctx, c.github, name)
		if err != nil {
			return managed.ExternalObservation{}, err
		}

		crARepos := getSortedEnabledReposFromCr(cr.Spec.ForProvider.Actions.EnabledRepos)
		aRepos := getSortedRepoNames(repos)

		if !reflect.DeepEqual(aRepos, crARepos) {
			return drifted(cr), nil
		}
	}

	if cr.Spec.ForProvider.Secrets != nil {
		if cr.Spec.ForProvider.Secrets.ActionsSecrets != nil {
			crActionsSecretsToConfig, err := getOrgSecretsMapFromCr(ctx, c.github, name, cr.Spec.ForProvider.Secrets.ActionsSecrets)
			if err != nil {
				return managed.ExternalObservation{}, err
			}
			ghActionsSecretsToConfig, err := getOrgSecretsWithConfig(ctx, c.github.Actions, name, cr.Spec.ForProvider.Secrets.ActionsSecrets)
			if err != nil {
				return managed.ExternalObservation{}, err
			}
			if !cmp.Equal(crActionsSecretsToConfig, ghActionsSecretsToConfig) {
				return drifted(cr), nil
			}
		}
		if cr.Spec.ForProvider.Secrets.DependabotSecrets != nil {
			crDependabotSecretsToConfig, err := getOrgSecretsMapFromCr(ctx, c.github, name, cr.Spec.ForProvider.Secrets.DependabotSecrets)
			if err != nil {
				return managed.ExternalObservation{}, err
			}
			ghDependabotSecretsToConfig, err := getOrgSecretsWithConfig(ctx, c.github.Dependabot, name, cr.Spec.ForProvider.Secrets.DependabotSecrets)
			if err != nil {
				return managed.ExternalObservation{}, err
			}
			if !cmp.Equal(crDependabotSecretsToConfig, ghDependabotSecretsToConfig) {
				return drifted(cr), nil
			}
		}
	}

	// An omitted description (an Observe-only import) is not compared; under a
	// policy that writes, an empty description is a declared one.
	desc := cr.Spec.ForProvider.Description
	if (desc != "" || mgmtpolicy.WritesDeclared(cr.GetManagementPolicies())) && desc != pointer.Deref(org.Description, "") {
		return drifted(cr), nil
	}

	cr.SetConditions(xpv2.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// drifted reports an existing Organization that differs from its spec. Ready is
// False until a later poll finds it in sync; the update that corrects the drift
// runs in the meantime.
func drifted(cr *v1alpha1.Organization) managed.ExternalObservation {
	cr.SetConditions(xpv2.Unavailable())
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
	req := &github.Organization{
		Description: &cr.Spec.ForProvider.Description,
	}

	_, _, err := gh.Organizations.Edit(ctx, name, req)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	if cr.Spec.ForProvider.Actions.EnabledRepos != nil {
		if err := setEnabledReposForActions(ctx, gh, name, cr); err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	secrets := cr.Spec.ForProvider.Secrets
	if secrets != nil {
		if secrets.ActionsSecrets != nil {
			err = updateOrgSecrets(ctx, gh, name, cr.Spec.ForProvider.Secrets.ActionsSecrets, &ActionsSecretSetter{gh: gh})
			if err != nil {
				return managed.ExternalUpdate{}, err
			}
		}
		if secrets.DependabotSecrets != nil {
			err = updateOrgSecrets(ctx, gh, name, cr.Spec.ForProvider.Secrets.DependabotSecrets, &DependabotSecretSetter{gh: gh})
			if err != nil {
				return managed.ExternalUpdate{}, err
			}
		}
	}

	return managed.ExternalUpdate{}, nil
}

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
	if reflect.DeepEqual(crARepos, getSortedRepoNames(repos)) {
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

func getOrgSecretsWithConfig(ctx context.Context, c OrgSecretGetter, owner string, secrets []v1alpha1.OrgSecret) (map[string][]int64, error) {
	orgSecretsToConfig := make(map[string][]int64, len(secrets))
	for _, secret := range secrets {
		// Check for context timeout before processing each secret
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		ghSecret, _, err := c.GetOrgSecret(ctx, owner, secret.Name)
		if err != nil {
			return nil, err
		}
		repoIds := make([]int64, 0)
		if ghSecret != nil && ghSecret.Visibility == "selected" {
			opts := &github.ListOptions{PerPage: 100}
			for {
				// Check for context timeout in pagination loop
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}

				ghRepo, resp, err := c.ListSelectedReposForOrgSecret(ctx, owner, secret.Name, opts)
				if err != nil {
					return nil, err
				}
				for _, selectedRepo := range ghRepo.Repositories {
					repoIds = append(repoIds, selectedRepo.GetID())
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
	}
	return orgSecretsToConfig, nil
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

func updateOrgSecrets(ctx context.Context, gh *ghclient.Client, owner string, secrets []v1alpha1.OrgSecret, setter OrgSecretSetter) error {
	for _, secret := range secrets {
		repoIds := make([]int64, 0, len(secret.RepositoryAccessList))
		for _, repo := range secret.RepositoryAccessList {
			ghRepo, _, err := gh.Repositories.Get(ctx, owner, repo.Repo)
			if err != nil {
				return err
			}
			repoIds = append(repoIds, ghRepo.GetID())
		}
		err := setter.SetSelectedReposForOrgSecret(ctx, owner, secret.Name, repoIds)
		if err != nil {
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
