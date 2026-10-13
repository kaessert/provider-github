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

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/clients/fake"
)

// observeOnlyCR is what an Observe-only import declares: the organization and
// the name, and nothing else.
func observeOnlyCR(m ...modifier) *v1alpha1.RunnerGroup {
	cr := newCR(withVisibility(""))
	cr.Spec.ManagementPolicies = xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
	for _, f := range m {
		f(cr)
	}
	return cr
}

// restrictedGroup is a runner group that allows public repositories and is
// restricted to two workflows.
func restrictedGroup() *github.RunnerGroup {
	return ghGroup(func(g *github.RunnerGroup) {
		g.AllowsPublicRepositories = github.Ptr(true)
		g.RestrictedToWorkflows = github.Ptr(true)
		g.SelectedWorkflows = []string{testWorkflowA, testWorkflowB}
	})
}

func observeRestricted(t *testing.T, cr *v1alpha1.RunnerGroup) bool {
	t.Helper()
	return observeGroup(t, cr, restrictedGroup())
}

func observeGroup(t *testing.T, cr *v1alpha1.RunnerGroup, g *github.RunnerGroup) bool {
	t.Helper()
	e := newExternal(&fake.MockActionsClient{MockListOrganizationRunnerGroups: listGroups(g)}, nil)
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists {
		t.Fatalf("Observe = %+v, want the group to exist", got)
	}
	return got.ResourceUpToDate
}

// An omitted allowsPublicRepositories and selectedWorkflows are not compared
// under an Observe-only policy.
func TestObserveOnlyOmittedFieldsNoDrift(t *testing.T) {
	cr := observeOnlyCR()
	if !observeRestricted(t, cr) {
		t.Fatalf("ResourceUpToDate = false, want true")
	}
	assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
}

// Under a policy that writes, the omitted fields keep their defaults and
// report drift, as before.
func TestObserveWritesAllowedOmittedFieldsStillDrift(t *testing.T) {
	for name, policies := range map[string]xpv2.ManagementPolicies{
		"Default":       nil,
		"All":           {xpv2.ManagementActionAll},
		"ObserveUpdate": {xpv2.ManagementActionObserve, xpv2.ManagementActionUpdate},
	} {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyCR()
			cr.Spec.ManagementPolicies = policies
			cr.Spec.ForProvider.Visibility = "all"
			if observeRestricted(t, cr) {
				t.Fatalf("ResourceUpToDate = true, want false")
			}
			assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
		})
	}
}

// allowsPublicRepositories is a plain bool, so only a declared true is told
// apart from an omitted field.
func TestObserveOnlyDeclaredAllowsPublicRepositoriesStillDrifts(t *testing.T) {
	cr := observeOnlyCR(func(cr *v1alpha1.RunnerGroup) { cr.Spec.ForProvider.AllowsPublicRepositories = true })
	if observeGroup(t, cr, ghGroup()) {
		t.Fatalf("ResourceUpToDate = true, want false")
	}
	assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
}

// A selectedWorkflows the Observe-only spec does declare is still compared, an
// explicitly empty list included.
func TestObserveOnlyDeclaredFieldsStillDrift(t *testing.T) {
	cases := map[string]modifier{
		"SelectedWorkflows": func(cr *v1alpha1.RunnerGroup) {
			cr.Spec.ForProvider.AllowsPublicRepositories = true
			cr.Spec.ForProvider.SelectedWorkflows = []v1alpha1.WorkflowRef{testWorkflowA}
		},
		"EmptySelectedWorkflows": func(cr *v1alpha1.RunnerGroup) {
			cr.Spec.ForProvider.AllowsPublicRepositories = true
			cr.Spec.ForProvider.SelectedWorkflows = []v1alpha1.WorkflowRef{}
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyCR(mod)
			if observeRestricted(t, cr) {
				t.Fatalf("ResourceUpToDate = true, want false")
			}
			assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
		})
	}
}
