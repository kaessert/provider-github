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

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func descriptionOnlyExternal(ghDescription string) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGet: func(context.Context, string) (*github.Organization, *github.Response, error) {
				return &github.Organization{Name: &org, Description: &ghDescription}, nil, nil
			},
		},
	}}}
}

func descriptionOnlyCR(desc string, policies ...xpv2.ManagementAction) *v1alpha1.Organization {
	cr := &v1alpha1.Organization{}
	cr.Spec.ForProvider.Description = desc
	cr.Spec.ManagementPolicies = policies
	meta.SetExternalName(cr, org)
	return cr
}

// atProvider.id mirrors the external name.
func TestObserveStatusSetsAtProviderID(t *testing.T) {
	cr := descriptionOnlyCR(description)
	if _, err := descriptionOnlyExternal(description).Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cr.Status.AtProvider.ID; got != org {
		t.Errorf("atProvider.id = %q, want %q", got, org)
	}
}

// An Observe-only import omits description: nothing is compared.
func TestObserveStatusOmittedDescriptionObserveOnlyNoDrift(t *testing.T) {
	cr := descriptionOnlyCR("", xpv2.ManagementActionObserve)
	got, err := descriptionOnlyExternal(description).Observe(context.Background(), cr)
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

// Under a policy that writes, an empty description is a declared one: it is
// compared, and drift from a non-empty description on GitHub is corrected.
func TestObserveStatusEmptyDescriptionWithWritesIsDrift(t *testing.T) {
	for name, policies := range map[string][]xpv2.ManagementAction{
		"Default":      nil,
		"All":          {xpv2.ManagementActionAll},
		"ObserveWrite": {xpv2.ManagementActionObserve, xpv2.ManagementActionUpdate},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := descriptionOnlyExternal(description).Observe(context.Background(), descriptionOnlyCR("", policies...))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Observe: -want, +got:\n%s", diff)
			}
		})
	}
}
