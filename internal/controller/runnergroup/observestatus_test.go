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
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func assertReady(t *testing.T, cr *v1alpha1.RunnerGroup, status corev1.ConditionStatus, reason xpv2.ConditionReason) {
	t.Helper()
	c := cr.GetCondition(xpv2.TypeReady)
	if c.Status != status || c.Reason != reason {
		t.Errorf("Ready = %s/%s, want %s/%s", c.Status, c.Reason, status, reason)
	}
}

// Every drift path reports Ready=False while Update corrects it.
func TestObserveStatusDriftSetsUnavailable(t *testing.T) {
	cases := map[string]struct {
		cr     *v1alpha1.RunnerGroup
		gh     *github.RunnerGroup
		access func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error)
	}{
		"Visibility": {
			cr: newCR(),
			gh: ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr("private") }),
		},
		"AllowsPublicRepositories": {
			cr: newCR(),
			gh: ghGroup(func(g *github.RunnerGroup) { g.AllowsPublicRepositories = github.Ptr(true) }),
		},
		"SelectedWorkflows": {
			cr: newCR(withWorkflows(testWorkflowA)),
			gh: ghGroup(func(g *github.RunnerGroup) {
				g.RestrictedToWorkflows = github.Ptr(true)
				g.SelectedWorkflows = []string{testWorkflowA, testWorkflowB}
			}),
		},
		"RepositoryAccess": {
			cr:     newCR(withVisibility("selected"), withSelectedRepos(testRepoA)),
			gh:     ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr("selected") }),
			access: listRepoAccess(testRepoAID, testRepoBID),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			actions := &fake.MockActionsClient{
				MockListOrganizationRunnerGroups:    listGroups(tc.gh),
				MockListRepositoryAccessRunnerGroup: tc.access,
			}
			e := newExternal(actions, knownRepos())

			cr := tc.cr
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

// An Observe-only import omits visibility: it is not compared, whatever
// visibility the group has. The repositories with access are mirrored when
// GitHub's visibility is selected.
func TestObserveStatusOmittedVisibilityNoDrift(t *testing.T) {
	for _, ghVisibility := range []string{"all", "private", "selected"} {
		t.Run(ghVisibility, func(t *testing.T) {
			actions := &fake.MockActionsClient{
				MockListOrganizationRunnerGroups:    listGroups(ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr(ghVisibility) })),
				MockListRepositoryAccessRunnerGroup: namedRepoAccess(testRepoA),
			}
			e := newExternal(actions, nil)

			cr := newCR(withVisibility(""))
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
		})
	}
}
