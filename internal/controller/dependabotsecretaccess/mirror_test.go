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

package dependabotsecretaccess

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

// Observe mirrors the secret's visibility and the repositories that can use it,
// spelled as GitHub spells them, into status.atProvider.
func TestObserveMirrorsDependabotSecretAccess(t *testing.T) {
	e := newExternal(&fake.MockDependabotClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos("Repo-B", testRepoA),
	}, nil)

	cr := newCR(visibilitySelected, testRepoA, "Repo-B")
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := v1alpha1.DependabotSecretAccessObservation{
		ID:                   testSecretName,
		Org:                  testOrg,
		Visibility:           visibilitySelected,
		SelectedRepositories: []v1alpha1.SecretSelectedRepoObservation{{Repo: "Repo-B"}, {Repo: testRepoA}},
	}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}

// The repository list is read, and so mirrored, only while the spec asks for
// the selected visibility; a list mirrored earlier is not kept.
func TestObserveMirrorDropsRepositoriesWhenNotSelected(t *testing.T) {
	e := newExternal(&fake.MockDependabotClient{
		MockGetOrgSecret:                  getSecret(visibilityAll),
		MockListSelectedReposForOrgSecret: listMustNotBeCalled(t),
	}, nil)

	cr := newCR(visibilityAll)
	cr.Status.AtProvider.SelectedRepositories = []v1alpha1.SecretSelectedRepoObservation{{Repo: testRepoA}}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cr.Status.AtProvider.Visibility; got != visibilityAll {
		t.Errorf("visibility = %q, want %q", got, visibilityAll)
	}
	if got := cr.Status.AtProvider.SelectedRepositories; len(got) != 0 {
		t.Errorf("selectedRepositories = %v, want none", got)
	}
}
