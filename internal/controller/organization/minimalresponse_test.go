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

package organization

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// bareOrganization declares nothing beyond the organization it points at: no
// description, no enabled repositories, no secrets.
func bareOrganization(policies ...xpv2.ManagementAction) *clusterv1alpha1.Organization {
	cr := &clusterv1alpha1.Organization{}
	cr.Spec.ManagementPolicies = policies
	meta.SetExternalName(cr, org)
	return cr
}

func organizationAnswering(o *github.Organization) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGet: func(context.Context, string) (*github.Organization, *github.Response, error) {
				return o, fake.GenerateEmptyResponse(), nil
			},
		},
	}}}
}

// minimalObserveCases are GitHub responses stripped of every optional field.
// Observe must not dereference what GitHub left out.
var minimalObserveCases = map[string]struct {
	reason   string
	gh       *github.Organization
	policies []xpv2.ManagementAction
	want     managed.ExternalObservation
}{
	"EmptyOrganizationDefaultPolicies": {
		reason: "An organization without a description matches an omitted description.",
		gh:     &github.Organization{},
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
	},
	"EmptyOrganizationObserveOnly": {
		reason:   "An empty organization is not compared under an Observe-only policy.",
		gh:       &github.Organization{},
		policies: []xpv2.ManagementAction{xpv2.ManagementActionObserve},
		want:     managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
	},
	"NilOrganizationObserveOnly": {
		reason:   "A nil organization with no error is not compared under an Observe-only policy.",
		gh:       nil,
		policies: []xpv2.ManagementAction{xpv2.ManagementActionObserve},
		want:     managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
	},
}

func TestObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			cr := bareOrganization(tc.policies...)
			got, err := organizationAnswering(tc.gh).Observe(context.Background(), cr)
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
			e := scopebridge.New[*namespacedv1alpha1.Organization](organizationAnswering(tc.gh),
				func() *clusterv1alpha1.Organization { return &clusterv1alpha1.Organization{} })
			cr := ddtest.Convert(t, bareOrganization(tc.policies...), &namespacedv1alpha1.Organization{})
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
