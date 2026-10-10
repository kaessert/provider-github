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
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
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
// while GitHub's visibility is selected, and dropped otherwise.
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

	// Once GitHub no longer holds a list, a stale one is not kept.
	e = newExternal(&fake.MockActionsClient{
		MockListOrganizationRunnerGroups: listGroups(ghGroup()),
	}, nil)
	cr.Spec.ForProvider.Visibility = "all"
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cr.Status.AtProvider.SelectedRepositories; len(got) != 0 {
		t.Errorf("selectedRepositories = %v, want none", got)
	}
}

// A group GitHub reports with no selected workflows mirrors an empty list, not
// an absent one, so an emptied list reads [] like selectedRepositories does.
// The namespaced variant runs the same external client through the scope bridge.
func TestObserveMirrorsNoWorkflowsAsEmptyList(t *testing.T) {
	cases := map[string]*github.RunnerGroup{
		"NilFromGitHub":   ghGroup(func(g *github.RunnerGroup) { g.SelectedWorkflows = nil }),
		"EmptyFromGitHub": ghGroup(func(g *github.RunnerGroup) { g.SelectedWorkflows = []string{} }),
	}
	for name, gh := range cases {
		e := newExternal(&fake.MockActionsClient{MockListOrganizationRunnerGroups: listGroups(gh)}, nil)

		t.Run("Cluster/"+name, func(t *testing.T) {
			cr := newCR(withWorkflows(testWorkflowA))
			if _, err := e.Observe(context.Background(), cr); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := cr.Status.AtProvider.SelectedWorkflows; got == nil || len(got) != 0 {
				t.Errorf("selectedWorkflows = %#v, want a non-nil empty list", got)
			}
		})

		t.Run("Namespaced/"+name, func(t *testing.T) {
			ns := ddtest.Convert(t, newCR(withWorkflows(testWorkflowA)), &namespacedv1alpha1.RunnerGroup{})
			b := scopebridge.New[*namespacedv1alpha1.RunnerGroup](&e,
				func() *v1alpha1.RunnerGroup { return &v1alpha1.RunnerGroup{} })
			if _, err := b.Observe(context.Background(), ns); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := ns.Status.AtProvider.SelectedWorkflows; got == nil || len(got) != 0 {
				t.Errorf("selectedWorkflows = %#v, want a non-nil empty list", got)
			}
		})
	}
}
