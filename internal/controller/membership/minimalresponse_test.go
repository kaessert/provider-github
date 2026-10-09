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

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// membershipAnswering returns an external client whose membership lookup
// answers with m and no error.
func membershipAnswering(m *github.Membership) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGetOrgMembership: func(context.Context, string, string) (*github.Membership, *github.Response, error) {
				return m, fake.GenerateEmptyResponse(), nil
			},
		},
	}}}
}

// observeOnlyMembership is a membership whose role is omitted, as in an
// Observe-only import.
func observeOnlyMembership() *clusterv1alpha1.Membership {
	return membership(func(m *clusterv1alpha1.Membership) { m.Spec.ForProvider.Role = "" })
}

// minimalObserveCases are GitHub responses stripped of every optional field.
// A role the spec omits is never compared, so the missing role on the GitHub
// side must not be dereferenced.
var minimalObserveCases = map[string]struct {
	reason string
	gh     *github.Membership
	want   managed.ExternalObservation
}{
	"EmptyMembership": {
		reason: "A membership carrying no fields must be reported as existing and up to date.",
		gh:     &github.Membership{},
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
	},
	"NilMembership": {
		reason: "A nil membership with no error must be reported as existing and up to date.",
		gh:     nil,
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
	},
}

func TestObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyMembership()
			got, err := membershipAnswering(tc.gh).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonAvailable {
				t.Errorf("\n%s\nReady reason = %q, want %q", tc.reason, c.Reason, xpv2.ReasonAvailable)
			}
		})
	}
}

func TestNamespacedObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			e := scopebridge.New[*namespacedv1alpha1.Membership](membershipAnswering(tc.gh),
				func() *clusterv1alpha1.Membership { return &clusterv1alpha1.Membership{} })
			cr := ddtest.Convert(t, observeOnlyMembership(), &namespacedv1alpha1.Membership{})
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonAvailable {
				t.Errorf("\n%s\nReady reason = %q, want %q", tc.reason, c.Reason, xpv2.ReasonAvailable)
			}
		})
	}
}
