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

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func namedRepos(ids map[string]int64) func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
		list := &github.SelectedReposList{}
		for name, id := range ids {
			list.Repositories = append(list.Repositories, &github.Repository{ID: github.Ptr(id), Name: github.Ptr(name)})
		}
		return list, fake.GenerateEmptyResponse(), nil
	}
}

// Observe mirrors the variable GitHub reports, with the repositories that can
// read it, into status.atProvider.
func TestObserveMirrorsVariable(t *testing.T) {
	repos := &fake.MockRepositoriesClient{MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID})}
	e := newExternalWithActions(&fake.MockActionsClient{
		MockGetOrgVariable:                  variableActions(testValue, "selected").MockGetOrgVariable,
		MockListSelectedReposForOrgVariable: namedRepos(map[string]int64{testRepoB: testRepoBID, testRepoA: testRepoAID}),
	}, repos)

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB))
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := v1alpha1.OrganizationVariableObservation{
		ID:                   testVariableName,
		Org:                  testOrg,
		Value:                testValue,
		Visibility:           "selected",
		SelectedRepositories: []v1alpha1.VariableSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}},
	}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}

// The mirror shows what GitHub has even when it differs from the spec, and the
// repository list is dropped while the spec does not ask for it.
func TestObserveMirrorsDriftedVariable(t *testing.T) {
	e := newExternalWithActions(variableActions("other", "private"), nil)

	cr := newCR()
	cr.Status.AtProvider.SelectedRepositories = []v1alpha1.VariableSelectedRepoObservation{{Repo: testRepoA}}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ap := cr.Status.AtProvider
	if ap.Value != "other" || ap.Visibility != "private" || len(ap.SelectedRepositories) != 0 {
		t.Errorf("atProvider = %+v, want value other, visibility private and no repositories", ap)
	}
}
