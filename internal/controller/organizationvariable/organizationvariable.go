/*
Copyright 2026 The Crossplane Authors.

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

package organizationvariable

import (
	"context"
	"slices"

	"github.com/google/go-github/v90/github"
	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/controller/mgmtpolicy"
)

const (
	errNotOrganizationVariable = "managed resource is not an OrganizationVariable custom resource"
	errTrackPCUsage            = "cannot track ProviderConfig usage"
	errGetPC                   = "cannot get ProviderConfig"
	errNewClient               = "cannot create new Service"

	visibilitySelected = "selected"
)

type external struct {
	github *ghclient.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.OrganizationVariable)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotOrganizationVariable)
	}

	name := meta.GetExternalName(cr)
	org := cr.Spec.ForProvider.Org

	v, _, err := c.github.Actions.GetOrgVariable(ctx, org, name)
	if ghclient.Is404(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.Status.AtProvider.ID = name

	ghVisibility := ""
	if v.Visibility != nil {
		ghVisibility = *v.Visibility
	}
	// An omitted visibility, or an omitted value under an Observe-only policy,
	// is not compared.
	p := cr.Spec.ForProvider
	valueDrift := v.Value != p.Value && (p.Value != "" || mgmtpolicy.WritesDeclared(cr.GetManagementPolicies()))
	visibilityDrift := p.Visibility != "" && ghVisibility != p.Visibility
	if valueDrift || visibilityDrift {
		cr.SetConditions(xpv2.Unavailable())
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
	}

	if cr.Spec.ForProvider.Visibility == visibilitySelected {
		resolver := ghclient.NewRepoIDResolver(c.github, org)
		crIDs, err := resolver.BatchGetIDs(ctx, repoNamesFromCR(cr.Spec.ForProvider.SelectedRepositories))
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		ghIDs, err := listSelectedRepoIDs(ctx, c.github, org, name)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		ghclient.SortInt64(crIDs)
		ghclient.SortInt64(ghIDs)
		if !slices.Equal(crIDs, ghIDs) {
			cr.SetConditions(xpv2.Unavailable())
			return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
		}
	}

	cr.SetConditions(xpv2.Available())
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.OrganizationVariable)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotOrganizationVariable)
	}

	v, err := buildActionsVariable(ctx, c.github, cr)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	_, err = c.github.Actions.CreateOrgVariable(ctx, cr.Spec.ForProvider.Org, v)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.OrganizationVariable)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotOrganizationVariable)
	}

	v, err := buildActionsVariable(ctx, c.github, cr)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	_, err = c.github.Actions.UpdateOrgVariable(ctx, cr.Spec.ForProvider.Org, v.Name, github.ActionsUpdateOrgVariableRequest{
		Name:                  &v.Name,
		Value:                 &v.Value,
		Visibility:            &v.Visibility,
		SelectedRepositoryIDs: v.SelectedRepositoryIDs,
	})
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.OrganizationVariable)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotOrganizationVariable)
	}

	name := meta.GetExternalName(cr)
	_, err := c.github.Actions.DeleteOrgVariable(ctx, cr.Spec.ForProvider.Org, name)
	if err != nil && !ghclient.Is404(err) {
		return managed.ExternalDelete{}, err
	}
	return managed.ExternalDelete{}, nil
}

// buildActionsVariable produces the payload for CreateOrgVariable /
// UpdateOrgVariable. For visibility=selected, it resolves repository
// names to IDs so they're sent in the same request.
func buildActionsVariable(ctx context.Context, gh *ghclient.Client, cr *v1alpha1.OrganizationVariable) (github.ActionsCreateOrgVariableRequest, error) {
	visibility := cr.Spec.ForProvider.Visibility
	v := github.ActionsCreateOrgVariableRequest{
		Name:       meta.GetExternalName(cr),
		Value:      cr.Spec.ForProvider.Value,
		Visibility: visibility,
	}

	if visibility == visibilitySelected {
		resolver := ghclient.NewRepoIDResolver(gh, cr.Spec.ForProvider.Org)
		ids, err := resolver.BatchGetIDs(ctx, repoNamesFromCR(cr.Spec.ForProvider.SelectedRepositories))
		if err != nil {
			return github.ActionsCreateOrgVariableRequest{}, err
		}
		v.SelectedRepositoryIDs = ids
	}

	return v, nil
}

func repoNamesFromCR(refs []v1alpha1.VariableSelectedRepo) []string {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Repo)
	}
	return names
}

// listSelectedRepoIDs paginates through all repositories that have
// access to the variable on GitHub's side, returning their numeric IDs.
func listSelectedRepoIDs(ctx context.Context, gh *ghclient.Client, org, name string) ([]int64, error) {
	opts := &github.ListOptions{PerPage: 100}
	var ids []int64
	for {
		list, resp, err := gh.Actions.ListSelectedReposForOrgVariable(ctx, org, name, opts)
		if err != nil {
			return nil, err
		}
		for _, r := range list.Repositories {
			ids = append(ids, r.GetID())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return ids, nil
}

// Disconnect is a no-op: the GitHub client is cached and shared across
// reconciles, so there is nothing to release per connection.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
