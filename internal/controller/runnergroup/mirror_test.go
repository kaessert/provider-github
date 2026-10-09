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

package runnergroup

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

// Observe mirrors the group GitHub reports into status.atProvider.
func TestObserveMirrorsGroup(t *testing.T) {
	gh := ghGroup(func(g *github.RunnerGroup) {
		g.Visibility = github.Ptr("private")
		g.AllowsPublicRepositories = github.Ptr(true)
		g.RestrictedToWorkflows = github.Ptr(true)
		g.SelectedWorkflows = []string{testWorkflowB, testWorkflowA}
	})
	e := newExternal(&fake.MockActionsClient{MockListOrganizationRunnerGroups: listGroups(gh)}, nil)

	// The spec differs, so the group is reported out of date; the mirror
	// still shows what GitHub has.
	cr := newCR()
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := v1alpha1.RunnerGroupObservation{
		ID:                       testGroupID,
		Org:                      testOrg,
		Visibility:               "private",
		AllowsPublicRepositories: github.Ptr(true),
		SelectedWorkflows:        []v1alpha1.WorkflowRefObservation{testWorkflowA, testWorkflowB},
	}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}

// The repositories with access are mirrored from the list Observe already reads
// while the visibility is selected, and dropped otherwise.
func TestObserveMirrorsSelectedRepositories(t *testing.T) {
	access := func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
		return &github.ListRepositories{Repositories: []*github.Repository{
			{ID: github.Ptr(testRepoBID), Name: github.Ptr(testRepoB)},
			{ID: github.Ptr(testRepoAID), Name: github.Ptr(testRepoA)},
		}}, fake.GenerateEmptyResponse(), nil
	}
	e := newExternal(&fake.MockActionsClient{
		MockListOrganizationRunnerGroups:    listGroups(ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr("selected") })),
		MockListRepositoryAccessRunnerGroup: access,
	}, knownRepos())

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB))
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []v1alpha1.RunnerGroupSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}}
	if diff := cmp.Diff(want, cr.Status.AtProvider.SelectedRepositories); diff != "" {
		t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
	}

	// Once the spec no longer asks for the list, a stale one is not kept.
	cr.Spec.ForProvider.Visibility = ""
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cr.Status.AtProvider.SelectedRepositories; len(got) != 0 {
		t.Errorf("selectedRepositories = %v, want none", got)
	}
}
