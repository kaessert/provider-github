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

	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// A declared empty list and no entries on GitHub both mean "none", so they are
// not drift. A real difference still is.
func TestObserveEmptyEqualsAbsent(t *testing.T) {
	nilRepos := func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
		return &github.ListRepositories{}, fake.GenerateEmptyResponse(), nil
	}

	cases := map[string]struct {
		reason string
		cr     *clusterv1alpha1.RunnerGroup
		gh     *github.RunnerGroup
		access func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error)
		want   bool
	}{
		"SelectedWorkflowsDeclaredEmptyGitHubAbsent": {
			reason: "selectedWorkflows: [] and none on GitHub is in sync",
			cr: newCR(func(cr *clusterv1alpha1.RunnerGroup) {
				cr.Spec.ForProvider.SelectedWorkflows = []clusterv1alpha1.WorkflowRef{}
			}),
			gh:   ghGroup(func(g *github.RunnerGroup) { g.SelectedWorkflows = nil }),
			want: true,
		},
		"SelectedWorkflowsDeclaredNilGitHubEmpty": {
			reason: "no selectedWorkflows and an empty list on GitHub is in sync",
			cr:     newCR(),
			gh:     ghGroup(func(g *github.RunnerGroup) { g.SelectedWorkflows = []string{} }),
			want:   true,
		},
		"SelectedWorkflowsDeclaredEmptyGitHubHasWorkflow": {
			reason: "a workflow on GitHub that the spec leaves out is drift",
			cr: newCR(func(cr *clusterv1alpha1.RunnerGroup) {
				cr.Spec.ForProvider.SelectedWorkflows = []clusterv1alpha1.WorkflowRef{}
			}),
			gh: ghGroup(func(g *github.RunnerGroup) {
				g.RestrictedToWorkflows = github.Ptr(true)
				g.SelectedWorkflows = []string{testWorkflowA}
			}),
			want: false,
		},
		"SelectedRepositoriesDeclaredEmptyGitHubAbsent": {
			reason: "selectedRepositories: [] and no repository access on GitHub is in sync",
			cr: newCR(withVisibility("selected"), func(cr *clusterv1alpha1.RunnerGroup) {
				cr.Spec.ForProvider.SelectedRepositories = []clusterv1alpha1.RunnerGroupSelectedRepo{}
			}),
			gh:     ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr("selected") }),
			access: nilRepos,
			want:   true,
		},
		"SelectedRepositoriesDeclaredEmptyGitHubHasRepo": {
			reason: "repository access on GitHub that the spec leaves out is drift",
			cr: newCR(withVisibility("selected"), func(cr *clusterv1alpha1.RunnerGroup) {
				cr.Spec.ForProvider.SelectedRepositories = []clusterv1alpha1.RunnerGroupSelectedRepo{}
			}),
			gh:     ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr("selected") }),
			access: listRepoAccess(testRepoAID),
			want:   false,
		},
	}

	for name, tc := range cases {
		observe := func(t *testing.T, scope string) {
			actions := &fake.MockActionsClient{
				MockListOrganizationRunnerGroups:    listGroups(tc.gh),
				MockListRepositoryAccessRunnerGroup: tc.access,
			}
			e := newExternal(actions, knownRepos())
			cr := tc.cr.DeepCopy()

			var (
				mg  resource.Managed = cr
				got managed.ExternalObservation
				err error
			)
			if scope == "Namespaced" {
				ns := ddtest.Convert(t, cr, &namespacedv1alpha1.RunnerGroup{})
				mg = ns
				got, err = scopebridge.New[*namespacedv1alpha1.RunnerGroup](&e,
					func() *clusterv1alpha1.RunnerGroup { return &clusterv1alpha1.RunnerGroup{} }).Observe(context.Background(), ns)
			} else {
				got, err = e.Observe(context.Background(), cr)
			}
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.want {
				t.Errorf("%s\nResourceUpToDate = %v, want %v", tc.reason, got.ResourceUpToDate, tc.want)
			}
			if ready := mg.GetCondition(xpv2.TypeReady).Status == corev1.ConditionTrue; ready != tc.want {
				t.Errorf("%s\nReady = %v, want %v", tc.reason, ready, tc.want)
			}
		}
		t.Run("Cluster/"+name, func(t *testing.T) { observe(t, "Cluster") })
		t.Run("Namespaced/"+name, func(t *testing.T) { observe(t, "Namespaced") })
	}
}
