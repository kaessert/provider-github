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

package membership

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func membershipReturning(m *github.Membership) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGetOrgMembership: func(context.Context, string, string) (*github.Membership, *github.Response, error) {
				return m, nil, nil
			},
		},
	}}}
}

// Observe mirrors the role and organization GitHub reports into
// status.atProvider.
func TestObserveMirrorsMembership(t *testing.T) {
	cases := map[string]struct {
		reason string
		gh     *github.Membership
		want   v1alpha1.MembershipObservation
	}{
		"ReportedOrganization": {
			reason: "The organization in the response is the one recorded, not the one queried.",
			gh:     &github.Membership{Role: &role, Organization: &github.Organization{Login: github.Ptr("reported")}},
			want:   v1alpha1.MembershipObservation{ID: userName, Role: role, Org: "reported"},
		},
		"QueriedOrganization": {
			reason: "A response without an organization falls back to the organization it was read under.",
			gh:     githubMembership(),
			want:   v1alpha1.MembershipObservation{ID: userName, Role: role, Org: org},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := membership()
			if _, err := membershipReturning(tc.gh).Observe(context.Background(), cr); err != nil {
				t.Fatalf("%s\nunexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, cr.Status.AtProvider); diff != "" {
				t.Errorf("%s\natProvider: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
