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

// namedRepoAccess lists the repositories with access, by name.
func namedRepoAccess(names ...string) func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
	return func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
		repos := make([]*github.Repository, 0, len(names))
		for _, n := range names {
			repos = append(repos, &github.Repository{Name: github.Ptr(n)})
		}
		return &github.ListRepositories{Repositories: repos}, fake.GenerateEmptyResponse(), nil
	}
}

// observeBoth observes cr through the cluster-scoped client and a copy of it
// through the namespaced variant served by the scope bridge, and returns what
// each reported and mirrored.
func observeBoth(t *testing.T, e external, cr *v1alpha1.RunnerGroup) map[string]struct {
	upToDate bool
	repos    []v1alpha1.RunnerGroupSelectedRepoObservation
} {
	t.Helper()
	type result = struct {
		upToDate bool
		repos    []v1alpha1.RunnerGroupSelectedRepoObservation
	}
	out := map[string]result{}

	ns := ddtest.Convert(t, cr.DeepCopy(), &namespacedv1alpha1.RunnerGroup{})
	bridged := scopebridge.New[*namespacedv1alpha1.RunnerGroup](&e, func() *v1alpha1.RunnerGroup { return &v1alpha1.RunnerGroup{} })
	nsGot, err := bridged.Observe(context.Background(), ns)
	if err != nil {
		t.Fatalf("namespaced Observe: %v", err)
	}
	nsRepos := make([]v1alpha1.RunnerGroupSelectedRepoObservation, 0, len(ns.Status.AtProvider.SelectedRepositories))
	for _, r := range ns.Status.AtProvider.SelectedRepositories {
		nsRepos = append(nsRepos, v1alpha1.RunnerGroupSelectedRepoObservation{Repo: r.Repo})
	}
	out["Namespaced"] = result{nsGot.ResourceUpToDate, nsRepos}

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("cluster Observe: %v", err)
	}
	out["Cluster"] = result{got.ResourceUpToDate, cr.Status.AtProvider.SelectedRepositories}
	return out
}

// A field that differs from the spec does not stop the repositories with
// access from being read and mirrored: the group is reported out of date and
// status.atProvider holds the list GitHub has.
func TestObserveMirrorsSelectedRepositoriesWhenAnEarlierFieldDrifts(t *testing.T) {
	gh := ghGroup(func(g *github.RunnerGroup) {
		g.Visibility = github.Ptr("selected")
		g.AllowsPublicRepositories = github.Ptr(true)
	})
	e := newExternal(&fake.MockActionsClient{
		MockListOrganizationRunnerGroups:    listGroups(gh),
		MockListRepositoryAccessRunnerGroup: namedRepoAccess(testRepoB, testRepoA),
	}, knownRepos())

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB))
	want := []v1alpha1.RunnerGroupSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}}
	for scope, got := range observeBoth(t, e, cr) {
		t.Run(scope, func(t *testing.T) {
			if got.upToDate {
				t.Error("Observe reported the group up to date, want out of date")
			}
			if diff := cmp.Diff(want, got.repos); diff != "" {
				t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
			}
		})
	}
}

// An Observe-only import that omits visibility mirrors the repositories GitHub
// reports for a group whose visibility is selected.
func TestObserveMirrorsSelectedRepositoriesWithoutDeclaredVisibility(t *testing.T) {
	gh := ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr("selected") })
	e := newExternal(&fake.MockActionsClient{
		MockListOrganizationRunnerGroups:    listGroups(gh),
		MockListRepositoryAccessRunnerGroup: namedRepoAccess(testRepoB, testRepoA),
	}, nil)

	cr := newCR(withVisibility(""))
	want := []v1alpha1.RunnerGroupSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}}
	for scope, got := range observeBoth(t, e, cr) {
		t.Run(scope, func(t *testing.T) {
			if !got.upToDate {
				t.Error("Observe reported the group out of date, want up to date")
			}
			if diff := cmp.Diff(want, got.repos); diff != "" {
				t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
			}
		})
	}
}
