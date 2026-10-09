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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

// Observe mirrors the team GitHub reports into status.atProvider: its
// description, privacy and parent, and its direct members. A member the team
// only inherits from a child team is not listed.
func TestObserveMirrorsTeam(t *testing.T) {
	const (
		parentSlug = "parent-team"
		childSlug  = "child-team"
	)
	inherited := "inherited-user"
	members := map[string]map[string][]string{
		statusTeamName: {"maintainer": {member1}, "member": {member2, inherited}},
		childSlug:      {"member": {inherited}},
	}
	e := &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGetOrgMembership: func(context.Context, string, string) (*github.Membership, *github.Response, error) {
				return &github.Membership{State: github.Ptr("active"), Role: github.Ptr("member")}, fake.GenerateEmptyResponse(), nil
			},
		},
		Teams: &fake.MockTeamsClient{
			MockGetTeamBySlug: func(context.Context, string, string) (*github.Team, *github.Response, error) {
				return &github.Team{
					Description: github.Ptr("what GitHub has"),
					Privacy:     github.Ptr("closed"),
					Parent:      &github.Team{Name: github.Ptr("Parent Team"), Slug: github.Ptr(parentSlug)},
				}, nil, nil
			},
			MockListTeamMembersBySlug: func(_ context.Context, _, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
				logins := members[slug][opts.Role]
				users := make([]*github.User, 0, len(logins))
				for _, l := range logins {
					users = append(users, &github.User{Login: github.Ptr(l)})
				}
				return users, fake.GenerateEmptyResponse(), nil
			},
			MockListPendingTeamInvitationsBySlug: func(context.Context, string, string, *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
				return nil, fake.GenerateEmptyResponse(), nil
			},
			MockListChildTeamsByParentSlug: func(context.Context, string, string, *github.ListOptions) ([]*github.Team, *github.Response, error) {
				return []*github.Team{{Slug: github.Ptr(childSlug)}}, fake.GenerateEmptyResponse(), nil
			},
		},
	}}}

	cr := team()
	meta.SetExternalName(cr, statusTeamName)
	cr.Spec.ForProvider.Org = "acme"
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := v1alpha1.TeamObservation{
		ID:          statusTeamName,
		Description: "what GitHub has",
		Members: []v1alpha1.TeamMemberUserObservation{
			{User: member1, Role: "maintainer"},
			{User: member2, Role: "member"},
		},
		Org:     "acme",
		Parent:  github.Ptr("Parent Team"),
		Privacy: github.Ptr("closed"),
	}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}
