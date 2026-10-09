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

package team

import (
	"context"
	"testing"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// teamClient returns a factory for an external client whose team lookup finds a
// team matching team(), or answers 404 when notFound is set.
func teamClient(notFound bool) func() managed.ExternalClient {
	return func() managed.ExternalClient {
		return &external{github: &ghclient.Client{Services: &ghclient.Services{
			Teams: &fake.MockTeamsClient{
				MockGetTeamBySlug: func(_ context.Context, _, _ string) (*github.Team, *github.Response, error) {
					if notFound {
						return nil, nil, fake.Generate404Response()
					}
					return &github.Team{Privacy: &teamPrivacy, Description: &teamDescription}, nil, nil
				},
				MockListTeamMembersBySlug: func(_ context.Context, _, _ string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
					return githubTeam(opts.Role), fake.GenerateEmptyResponse(), nil
				},
				MockListPendingTeamInvitationsBySlug: func(_ context.Context, _, _ string, _ *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
					return nil, fake.GenerateEmptyResponse(), nil
				},
				MockListChildTeamsByParentSlug: func(_ context.Context, _, _ string, _ *github.ListOptions) ([]*github.Team, *github.Response, error) {
					return nil, fake.GenerateEmptyResponse(), nil
				},
			},
		}}}
	}
}

// A resource with no spec.driftDetection block must reconcile exactly as it did
// before the block existed: the decorator wired at both of this kind's
// reconciler sites has to be indistinguishable from the bare client, in the
// cluster-scoped variant and in the namespaced variant served through the
// scope bridge.
func TestDriftDetectionAbsentIsPassThrough(t *testing.T) {
	type scenario struct {
		client func() managed.ExternalClient
		cr     func() *clusterv1alpha1.Team
	}
	scenarios := map[string]scenario{
		"UpToDate": {
			client: teamClient(false),
			cr:     func() *clusterv1alpha1.Team { return team() },
		},
		"Drift": {
			client: teamClient(false),
			cr: func() *clusterv1alpha1.Team {
				return team(func(cr *clusterv1alpha1.Team) { cr.Spec.ForProvider.Description = "changed" })
			},
		},
		"NotFound": {
			client: teamClient(true),
			cr:     func() *clusterv1alpha1.Team { return team() },
		},
	}

	for name, tc := range scenarios {
		t.Run("Cluster/"+name, func(t *testing.T) {
			ddtest.RequirePassThrough(t, tc.client, func() resource.Managed { return tc.cr() })
		})
		t.Run("Namespaced/"+name, func(t *testing.T) {
			ddtest.RequirePassThrough(t,
				func() managed.ExternalClient {
					return scopebridge.New[*namespacedv1alpha1.Team](tc.client(),
						func() *clusterv1alpha1.Team { return &clusterv1alpha1.Team{} })
				},
				func() resource.Managed {
					return ddtest.Convert(t, tc.cr(), &namespacedv1alpha1.Team{})
				})
		})
	}
}
