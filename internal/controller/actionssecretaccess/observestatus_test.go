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

package actionssecretaccess

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

// observe sets atProvider.id to the external name, the secret name on GitHub.
func TestObserveStatusSetsAtProviderID(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret: getSecret(visibilityAll),
	}, nil)

	cr := newCR(visibilityAll)
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := cr.Status.AtProvider.ID, meta.GetExternalName(cr); got != want || got == "" {
		t.Errorf("atProvider.id = %q, want the external name %q", got, want)
	}
}

// A repository list that differs from the spec is drift Update corrects, and
// Ready is False meanwhile.
func TestObserveStatusRepoDriftSetsUnavailable(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos(testRepoA, testRepoB),
	}, nil)

	cr := newCR(visibilitySelected, testRepoA)
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || got.ResourceUpToDate {
		t.Errorf("Observe = %+v, want exists and not up to date", got)
	}
	assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
}

// An Observe-only import omits visibility: nothing is compared, so no
// mismatch is reported whatever visibility GitHub has. The repositories with
// access are mirrored when GitHub's visibility is selected.
func TestObserveStatusOmittedVisibilityNoMismatch(t *testing.T) {
	for _, ghVisibility := range []string{visibilityAll, visibilityPrivate, visibilitySelected} {
		t.Run(ghVisibility, func(t *testing.T) {
			e := newExternal(&fake.MockActionsClient{
				MockGetOrgSecret:                  getSecret(ghVisibility),
				MockListSelectedReposForOrgSecret: listRepos(testRepoA),
			}, nil)

			cr := newCR("")
			cr.Spec.ManagementPolicies = xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(upToDate, got); diff != "" {
				t.Errorf("Observe: -want, +got:\n%s", diff)
			}
			assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
			if cr.Status.AtProvider.ID == "" {
				t.Error("atProvider.id is empty, want the external name")
			}
			var wantRepos []v1alpha1.SecretSelectedRepoObservation
			if ghVisibility == visibilitySelected {
				wantRepos = []v1alpha1.SecretSelectedRepoObservation{{Repo: testRepoA}}
			}
			if diff := cmp.Diff(wantRepos, cr.Status.AtProvider.SelectedRepositories); diff != "" {
				t.Errorf("selectedRepositories: -want, +got:\n%s", diff)
			}
		})
	}
}
