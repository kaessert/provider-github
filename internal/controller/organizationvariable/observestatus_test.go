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

package organizationvariable

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func assertReady(t *testing.T, cr *v1alpha1.OrganizationVariable, status corev1.ConditionStatus, reason xpv2.ConditionReason) {
	t.Helper()
	c := cr.GetCondition(xpv2.TypeReady)
	if c.Status != status || c.Reason != reason {
		t.Errorf("Ready = %s/%s, want %s/%s", c.Status, c.Reason, status, reason)
	}
}

func variableActions(value, visibility string) *fake.MockActionsClient {
	return &fake.MockActionsClient{
		MockGetOrgVariable: func(context.Context, string, string) (*github.ActionsVariable, *github.Response, error) {
			return ghVariable(value, visibility), fake.GenerateEmptyResponse(), nil
		},
		MockListSelectedReposForOrgVariable: func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			return &github.SelectedReposList{Repositories: []*github.Repository{
				{ID: github.Ptr(testRepoAID)},
				{ID: github.Ptr(testRepoBID)},
			}}, fake.GenerateEmptyResponse(), nil
		},
	}
}

// atProvider.id mirrors the external name, the variable name.
func TestObserveStatusSetsAtProviderID(t *testing.T) {
	e := newExternalWithActions(variableActions(testValue, "all"), nil)

	cr := newCR()
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := cr.Status.AtProvider.ID, meta.GetExternalName(cr); got != want || got == "" {
		t.Errorf("atProvider.id = %q, want %q", got, want)
	}
}

// Every drift path reports Ready=False while Update corrects it.
func TestObserveStatusDriftSetsUnavailable(t *testing.T) {
	repos := &fake.MockRepositoriesClient{MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID})}
	cases := map[string]struct {
		cr *v1alpha1.OrganizationVariable
		gh *fake.MockActionsClient
	}{
		"Value":      {cr: newCR(), gh: variableActions("other", "all")},
		"Visibility": {cr: newCR(), gh: variableActions(testValue, "private")},
		"SelectedRepositories": {
			cr: newCR(withVisibility("selected"), withSelectedRepos(testRepoA)),
			gh: variableActions(testValue, "selected"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := tc.cr
			e := newExternalWithActions(tc.gh, repos)
			got, err := e.Observe(context.Background(), cr)
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

// An Observe-only import omits value and visibility: neither is compared.
func TestObserveStatusOmittedFieldsNoDrift(t *testing.T) {
	e := newExternalWithActions(variableActions("anything", "private"), nil)

	cr := newCR(withValue(""), withVisibility(""))
	cr.Spec.ManagementPolicies = xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
}

// Under a policy that writes, an empty value is a declared one: it is
// compared, and drift from a non-empty value on GitHub is corrected.
func TestObserveStatusEmptyValueWithWritesIsDrift(t *testing.T) {
	e := newExternalWithActions(variableActions("not-empty", "all"), nil)

	cr := newCR(withValue(""))
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || got.ResourceUpToDate {
		t.Errorf("Observe = %+v, want exists and not up to date", got)
	}
	assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
}
