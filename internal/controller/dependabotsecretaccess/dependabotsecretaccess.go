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

package dependabotsecretaccess

import (
	"context"
	"strings"

	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/controller/secretaccess"
)

const (
	errNotDependabotSecretAccess = "managed resource is not a DependabotSecretAccess custom resource"
	errTrackPCUsage              = "cannot track ProviderConfig usage"
	errGetPC                     = "cannot get ProviderConfig"
	errNewClient                 = "cannot create new Service"
)

type external struct {
	github *ghclient.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.DependabotSecretAccess)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotDependabotSecretAccess)
	}

	// Nothing to delete on GitHub, so report the resource gone to let the finalizer be removed.
	if meta.WasDeleted(cr) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	name := meta.GetExternalName(cr)
	org := cr.Spec.ForProvider.Org
	visibility := cr.Spec.ForProvider.Visibility

	s, _, err := c.github.Dependabot.GetOrgSecret(ctx, org, name)
	if ghclient.Is404(err) {
		cr.SetConditions(secretaccess.SecretNotFound(name))
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.Status.AtProvider.ID = name
	cr.Status.AtProvider.Org = org
	cr.Status.AtProvider.Visibility = s.GetVisibility()
	cr.Status.AtProvider.SelectedRepositories = nil

	// The list exists on GitHub's side while GitHub's visibility is selected,
	// whatever the spec says; it is read and mirrored before any difference is
	// reported.
	// An omitted visibility (an Observe-only import) is not compared.
	mismatch := visibility != "" && s.Visibility != visibility
	if s.GetVisibility() == secretaccess.VisibilitySelected {
		ghNames, err := secretaccess.ListRepoNames(ctx, c.github.Dependabot, org, name)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
		cr.Status.AtProvider.SelectedRepositories = secretaccess.RepoObservations(ghNames)
		if !mismatch && visibility == secretaccess.VisibilitySelected && !secretaccess.SameRepos(cr.Spec.ForProvider.SelectedRepositories, ghNames) {
			cr.SetConditions(xpv2.Unavailable())
			return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, nil
		}
	}

	if mismatch {
		cr.SetConditions(secretaccess.VisibilityMismatch(s.Visibility, visibility))
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}

	cr.SetConditions(xpv2.Available())
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

func (c *external) Create(_ context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.DependabotSecretAccess)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotDependabotSecretAccess)
	}
	return managed.ExternalCreation{}, secretaccess.SecretNotFoundError(meta.GetExternalName(cr))
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.DependabotSecretAccess)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotDependabotSecretAccess)
	}

	org := cr.Spec.ForProvider.Org
	name := meta.GetExternalName(cr)

	// IDs of repositories already selected come from the list; only newly
	// added names cost a Repositories.Get. Update cost must scale with the
	// repositories added, not the list size, or a large secret cannot
	// converge within the reconcile timeout. known is keyed by lowercased
	// name; Repositories.Get keeps the spec spelling (GitHub accepts any case).
	known, err := secretaccess.ListRepoIDs(ctx, c.github.Dependabot, org, name)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	names := secretaccess.RepoNames(cr.Spec.ForProvider.SelectedRepositories)
	var added []string
	for _, n := range names {
		if _, ok := known[strings.ToLower(n)]; !ok {
			added = append(added, n)
		}
	}
	addedIDs, err := ghclient.NewRepoIDResolver(c.github, org).BatchGetIDs(ctx, added)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	if err := secretaccess.RenamedRepoError(known, added, addedIDs); err != nil {
		return managed.ExternalUpdate{}, err
	}
	for i, n := range added {
		known[strings.ToLower(n)] = addedIDs[i]
	}
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		ids = append(ids, known[strings.ToLower(n)])
	}

	_, err = c.github.Dependabot.SetSelectedReposForOrgSecret(ctx, org, name, ids)
	return managed.ExternalUpdate{}, err
}

// Delete leaves the secret alone: the provider does not own it.
func (c *external) Delete(_ context.Context, _ resource.Managed) (managed.ExternalDelete, error) {
	return managed.ExternalDelete{}, nil
}

// Disconnect is a no-op: the GitHub client is cached and shared across
// reconciles, so there is nothing to release per connection.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
