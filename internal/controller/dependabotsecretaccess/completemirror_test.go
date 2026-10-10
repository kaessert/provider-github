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

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// observeBoth observes cr through the cluster-scoped client and a copy of it
// through the namespaced variant served by the scope bridge, and returns the
// observation, the Ready reason and the mirrored repositories of each.
func observeBoth(t *testing.T, e *external, cr *v1alpha1.DependabotSecretAccess) map[string]struct {
	obs    managed.ExternalObservation
	reason string
	repos  []v1alpha1.SecretSelectedRepoObservation
} {
	t.Helper()
	type result = struct {
		obs    managed.ExternalObservation
		reason string
		repos  []v1alpha1.SecretSelectedRepoObservation
	}
	out := map[string]result{}

	ns := ddtest.Convert(t, cr.DeepCopy(), &namespacedv1alpha1.DependabotSecretAccess{})
	bridged := scopebridge.New[*namespacedv1alpha1.DependabotSecretAccess](e, func() *v1alpha1.DependabotSecretAccess { return &v1alpha1.DependabotSecretAccess{} })
	nsObs, err := bridged.Observe(context.Background(), ns)
	if err != nil {
		t.Fatalf("namespaced Observe: %v", err)
	}
	nsRepos := make([]v1alpha1.SecretSelectedRepoObservation, 0, len(ns.Status.AtProvider.SelectedRepositories))
	for _, r := range ns.Status.AtProvider.SelectedRepositories {
		nsRepos = append(nsRepos, v1alpha1.SecretSelectedRepoObservation{Repo: r.Repo})
	}
	out["Namespaced"] = result{nsObs, string(ns.GetCondition("Ready").Reason), nsRepos}

	obs, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("cluster Observe: %v", err)
	}
	out["Cluster"] = result{obs, string(cr.GetCondition("Ready").Reason), cr.Status.AtProvider.SelectedRepositories}
	return out
}

// A visibility the spec declares and GitHub does not have is reported in Ready,
// and the repositories GitHub gives the secret access to are still mirrored
// when GitHub's visibility is selected.
func TestObserveMirrorsRepositoriesWhenTheVisibilityMismatches(t *testing.T) {
	e := newExternal(&fake.MockDependabotClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos(testRepoB, testRepoA),
	}, nil)

	want := []v1alpha1.SecretSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}}
	for scope, got := range observeBoth(t, e, newCR(visibilityAll)) {
		t.Run(scope, func(t *testing.T) {
			if diff := cmp.Diff(upToDate, got.obs); diff != "" {
				t.Errorf("Observe: -want, +got:\n%s", diff)
			}
			if got.reason != "VisibilityMismatch" {
				t.Errorf("Ready reason = %q, want VisibilityMismatch", got.reason)
			}
			if diff := cmp.Diff(want, got.repos); diff != "" {
				t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
			}
		})
	}
}

// An Observe-only import that omits visibility mirrors the repositories GitHub
// reports for a secret whose visibility is selected, and is up to date.
func TestObserveMirrorsRepositoriesWithoutDeclaredVisibility(t *testing.T) {
	e := newExternal(&fake.MockDependabotClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos(testRepoB, testRepoA),
	}, nil)

	want := []v1alpha1.SecretSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}}
	for scope, got := range observeBoth(t, e, newCR("")) {
		t.Run(scope, func(t *testing.T) {
			if diff := cmp.Diff(upToDate, got.obs); diff != "" {
				t.Errorf("Observe: -want, +got:\n%s", diff)
			}
			if got.reason != "Available" {
				t.Errorf("Ready reason = %q, want Available", got.reason)
			}
			if diff := cmp.Diff(want, got.repos); diff != "" {
				t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
			}
		})
	}
}
