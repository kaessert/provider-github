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

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// A declared empty member list and no members on GitHub both mean "none", so
// they are not drift. A real difference still is.
func TestObserveEmptyEqualsAbsent(t *testing.T) {
	cases := map[string]struct {
		reason  string
		members []clusterv1alpha1.TeamMemberUser
		// github is what GitHub lists for every role.
		github []*github.User
		want   bool
	}{
		"MembersDeclaredEmptyGitHubAbsent": {
			reason:  "members: [] and no members on GitHub is in sync",
			members: []clusterv1alpha1.TeamMemberUser{},
			github:  nil,
			want:    true,
		},
		"MembersDeclaredNilGitHubEmpty": {
			reason:  "no members declared and an empty list on GitHub is in sync",
			members: nil,
			github:  []*github.User{},
			want:    true,
		},
		"MembersDeclaredEmptyGitHubHasMember": {
			reason:  "a member on GitHub that the spec leaves out is drift",
			members: []clusterv1alpha1.TeamMemberUser{},
			github:  []*github.User{{Login: &member1}},
			want:    false,
		},
	}

	for name, tc := range cases {
		observe := func(t *testing.T, scope string) {
			cr := team()
			cr.Spec.ForProvider.Members = tc.members
			c := &external{github: &ghclient.Client{Services: &ghclient.Services{
				Teams: &fake.MockTeamsClient{
					MockGetTeamBySlug: func(context.Context, string, string) (*github.Team, *github.Response, error) {
						return &github.Team{Privacy: &teamPrivacy, Description: &teamDescription}, nil, nil
					},
					MockListTeamMembersBySlug: func(context.Context, string, string, *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
						return tc.github, fake.GenerateEmptyResponse(), nil
					},
					MockListPendingTeamInvitationsBySlug: func(context.Context, string, string, *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
						return nil, fake.GenerateEmptyResponse(), nil
					},
					MockListChildTeamsByParentSlug: func(context.Context, string, string, *github.ListOptions) ([]*github.Team, *github.Response, error) {
						return nil, fake.GenerateEmptyResponse(), nil
					},
				},
				Organizations: &fake.MockOrganizationsClient{
					MockGetOrgMembership: func(context.Context, string, string) (*github.Membership, *github.Response, error) {
						return &github.Membership{State: github.Ptr("active")}, fake.GenerateEmptyResponse(), nil
					},
				},
			}}}

			var (
				mg  resource.Managed = cr
				got managed.ExternalObservation
				err error
			)
			if scope == "Namespaced" {
				ns := ddtest.Convert(t, cr, &namespacedv1alpha1.Team{})
				mg = ns
				got, err = scopebridge.New[*namespacedv1alpha1.Team](c,
					func() *clusterv1alpha1.Team { return &clusterv1alpha1.Team{} }).Observe(context.Background(), ns)
			} else {
				got, err = c.Observe(context.Background(), cr)
			}
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.want {
				t.Errorf("%s\nResourceUpToDate = %v, want %v", tc.reason, got.ResourceUpToDate, tc.want)
			}
			if ready := mg.GetCondition(xpv2.TypeReady).Status == corev1.ConditionTrue; ready != tc.want {
				t.Errorf("%s\nReady = %v, want %v", tc.reason, ready, tc.want)
			}
		}
		t.Run("Cluster/"+name, func(t *testing.T) { observe(t, "Cluster") })
		t.Run("Namespaced/"+name, func(t *testing.T) { observe(t, "Namespaced") })
	}
}
