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

package membership

import (
	"context"

	"github.com/pkg/errors"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
)

const (
	errNotMembership = "managed resource is not a Membership custom resource"
	errTrackPCUsage  = "cannot track ProviderConfig usage"
	errGetPC         = "cannot get ProviderConfig"

	errNewClient = "cannot create new Service"
)

// roleDirectMember is the GitHub API name of the plain organization member role.
const roleDirectMember = "direct_member"

type external struct {
	github *ghclient.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)
	m, _, err := c.github.Organizations.GetOrgMembership(ctx, name, cr.Spec.ForProvider.Org)

	if ghclient.Is404(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.Status.AtProvider.ID = name

	// An omitted role (an Observe-only import) is not compared.
	if cr.Spec.ForProvider.Role != "" && *m.Role != cr.Spec.ForProvider.Role {
		cr.SetConditions(xpv2.Unavailable())
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	cr.SetConditions(xpv2.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)

	user, _, err := c.github.Users.Get(ctx, name)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	role := cr.Spec.ForProvider.Role
	if role == roleDirectMember {
		return managed.ExternalCreation{}, errors.New("Don't use `direct_member`, use `member` instead!")
	}
	if role == "member" {
		role = roleDirectMember
	}

	inv := &github.CreateOrgInvitationOptions{
		InviteeID: user.ID,
		Role:      &role,
		TeamID:    []int64{},
	}

	_, _, err = c.github.Organizations.CreateOrgInvitation(ctx, cr.Spec.ForProvider.Org, inv)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	cr.SetConditions(xpv2.Available())

	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)
	u, _, err := c.github.Users.Get(ctx, name)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	m := &github.Membership{
		User: u,
		Role: &cr.Spec.ForProvider.Role,
	}

	_, _, err = c.github.Organizations.EditOrgMembership(ctx, name, cr.Spec.ForProvider.Org, m)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.Membership)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotMembership)
	}

	name := meta.GetExternalName(cr)
	role := cr.Spec.ForProvider.Role

	if role == "admin" {
		return managed.ExternalDelete{}, errors.New("You can only delete member users. Admin users cannot be deleted")
	}

	_, err := c.github.Organizations.RemoveOrgMembership(ctx, name, cr.Spec.ForProvider.Org)
	if err != nil {
		return managed.ExternalDelete{}, err
	}

	return managed.ExternalDelete{}, nil
}

// Disconnect is a no-op: the GitHub client is cached and shared across
// reconciles, so there is nothing to release per connection.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
