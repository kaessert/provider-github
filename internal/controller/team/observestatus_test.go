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
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

const statusTeamName = "platform"

func teamExternal(ghDescription string) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGetOrgMembership: func(context.Context, string, string) (*github.Membership, *github.Response, error) {
				active := "active"
				return &github.Membership{State: &active}, fake.GenerateEmptyResponse(), nil
			},
		},
		Teams: &fake.MockTeamsClient{
			MockGetTeamBySlug: func(context.Context, string, string) (*github.Team, *github.Response, error) {
				return &github.Team{Privacy: &teamPrivacy, Description: &ghDescription}, nil, nil
			},
			MockListTeamMembersBySlug: func(_ context.Context, _, _ string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
				return githubTeam(opts.Role), fake.GenerateEmptyResponse(), nil
			},
			MockListPendingTeamInvitationsBySlug: func(context.Context, string, string, *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
				return nil, fake.GenerateEmptyResponse(), nil
			},
			MockListChildTeamsByParentSlug: func(context.Context, string, string, *github.ListOptions) ([]*github.Team, *github.Response, error) {
				return nil, fake.GenerateEmptyResponse(), nil
			},
		},
	}}}
}

func namedTeam(m ...teamModifier) *v1alpha1.Team {
	cr := team(m...)
	meta.SetExternalName(cr, statusTeamName)
	return cr
}

func assertReady(t *testing.T, cr *v1alpha1.Team, status corev1.ConditionStatus, reason xpv2.ConditionReason) {
	t.Helper()
	c := cr.GetCondition(xpv2.TypeReady)
	if c.Status != status || c.Reason != reason {
		t.Errorf("Ready = %s/%s, want %s/%s", c.Status, c.Reason, status, reason)
	}
}

// atProvider.id mirrors the external name; the placeholder field is untouched.
func TestObserveStatusSetsAtProviderID(t *testing.T) {
	cr := namedTeam()
	if _, err := teamExternal(teamDescription).Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cr.Status.AtProvider.ID; got != statusTeamName {
		t.Errorf("atProvider.id = %q, want %q", got, statusTeamName)
	}
	assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
}

// Structural drift and member drift both report Ready=False while Update
// corrects them.
func TestObserveStatusDriftSetsUnavailable(t *testing.T) {
	cases := map[string]struct {
		ghDescription string
		mods          []teamModifier
	}{
		"Structural": {ghDescription: "other", mods: nil},
		"Member":     {ghDescription: teamDescription, mods: []teamModifier{withExtraMember(member3, member3Role)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := namedTeam(tc.mods...)
			got, err := teamExternal(tc.ghDescription).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Errorf("Observe = %+v, want exists and not up to date", got)
			}
			assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
		})
	}
}
