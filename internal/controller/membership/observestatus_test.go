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
	corev1 "k8s.io/api/core/v1"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func membershipExternal() *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGetOrgMembership: func(context.Context, string, string) (*github.Membership, *github.Response, error) {
				return githubMembership(), nil, nil
			},
		},
	}}}
}

// atProvider.id mirrors the external name, the user's login.
func TestObserveStatusSetsAtProviderID(t *testing.T) {
	cr := membership()
	if _, err := membershipExternal().Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := cr.Status.AtProvider.ID, meta.GetExternalName(cr); got != want || got == "" {
		t.Errorf("atProvider.id = %q, want %q", got, want)
	}
}

// A role that differs from GitHub is drift Update corrects, and Ready is
// False meanwhile.
func TestObserveStatusRoleDriftSetsUnavailable(t *testing.T) {
	cr := membership(withAdminRole())
	got, err := membershipExternal().Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	c := cr.GetCondition(xpv2.TypeReady)
	if c.Status != corev1.ConditionFalse || c.Reason != xpv2.ReasonUnavailable {
		t.Errorf("Ready = %s/%s, want False/%s", c.Status, c.Reason, xpv2.ReasonUnavailable)
	}
}

// An Observe-only import omits role: nothing is compared.
func TestObserveStatusOmittedRoleNoDrift(t *testing.T) {
	cr := membership(func(m *v1alpha1.Membership) { m.Spec.ForProvider.Role = "" })
	cr.Spec.ManagementPolicies = xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
	got, err := membershipExternal().Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonAvailable {
		t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.ReasonAvailable)
	}
}
