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

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

type observed struct {
	upToDate bool
	repos    []v1alpha1.VariableSelectedRepoObservation
}

// observeBoth observes cr through the cluster-scoped client and a copy of it
// through the namespaced variant served by the scope bridge.
func observeBoth(t *testing.T, e external, cr *v1alpha1.OrganizationVariable) map[string]observed {
	t.Helper()
	out := map[string]observed{}

	ns := ddtest.Convert(t, cr.DeepCopy(), &namespacedv1alpha1.OrganizationVariable{})
	bridged := scopebridge.New[*namespacedv1alpha1.OrganizationVariable](&e, func() *v1alpha1.OrganizationVariable { return &v1alpha1.OrganizationVariable{} })
	nsGot, err := bridged.Observe(context.Background(), ns)
	if err != nil {
		t.Fatalf("namespaced Observe: %v", err)
	}
	nsRepos := make([]v1alpha1.VariableSelectedRepoObservation, 0, len(ns.Status.AtProvider.SelectedRepositories))
	for _, r := range ns.Status.AtProvider.SelectedRepositories {
		nsRepos = append(nsRepos, v1alpha1.VariableSelectedRepoObservation{Repo: r.Repo})
	}
	out["Namespaced"] = observed{nsGot.ResourceUpToDate, nsRepos}

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("cluster Observe: %v", err)
	}
	out["Cluster"] = observed{got.ResourceUpToDate, cr.Status.AtProvider.SelectedRepositories}
	return out
}

func selectedVariable(value string) external {
	return newExternalWithActions(&fake.MockActionsClient{
		MockGetOrgVariable:                  variableActions(value, "selected").MockGetOrgVariable,
		MockListSelectedReposForOrgVariable: namedRepos(map[string]int64{testRepoB: testRepoBID, testRepoA: testRepoAID}),
	}, &fake.MockRepositoriesClient{MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID})})
}

// A value that differs from the spec does not stop the repositories that can
// read the variable from being read and mirrored: the variable is reported out
// of date and status.atProvider holds the list GitHub has.
func TestObserveMirrorsSelectedRepositoriesWhenTheValueDrifts(t *testing.T) {
	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA))
	want := []v1alpha1.VariableSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}}
	for scope, got := range observeBoth(t, selectedVariable("other"), cr) {
		t.Run(scope, func(t *testing.T) {
			if got.upToDate {
				t.Error("Observe reported the variable up to date, want out of date")
			}
			if diff := cmp.Diff(want, got.repos); diff != "" {
				t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
			}
		})
	}
}

// An Observe-only import that omits visibility mirrors the repositories GitHub
// reports for a variable whose visibility is selected.
func TestObserveMirrorsSelectedRepositoriesWithoutDeclaredVisibility(t *testing.T) {
	cr := newCR(withVisibility(""))
	want := []v1alpha1.VariableSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}}
	for scope, got := range observeBoth(t, selectedVariable(testValue), cr) {
		t.Run(scope, func(t *testing.T) {
			if !got.upToDate {
				t.Error("Observe reported the variable out of date, want up to date")
			}
			if diff := cmp.Diff(want, got.repos); diff != "" {
				t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
			}
		})
	}
}
