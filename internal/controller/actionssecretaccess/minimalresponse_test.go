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
	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// minimalObserveCases are GitHub responses stripped of every optional field.
// Observe must not dereference what GitHub left out.
var minimalObserveCases = map[string]struct {
	reason     string
	secret     *github.Secret
	list       *github.SelectedReposList
	visibility string
	want       managed.ExternalObservation
	wantStatus corev1.ConditionStatus
	wantReason xpv2.ConditionReason
}{
	"NilSecretOmittedVisibility": {
		reason:     "A nil secret with no error is not compared when the spec omits visibility.",
		secret:     nil,
		want:       upToDate,
		wantStatus: corev1.ConditionTrue,
		wantReason: xpv2.ReasonAvailable,
	},
	"EmptySecretOmittedVisibility": {
		reason:     "An empty secret is not compared when the spec omits visibility.",
		secret:     &github.Secret{},
		want:       upToDate,
		wantStatus: corev1.ConditionTrue,
		wantReason: xpv2.ReasonAvailable,
	},
	"EmptySecretDeclaredVisibility": {
		reason:     "An empty secret differs from a declared visibility, which is reported in Ready and not as drift.",
		secret:     &github.Secret{},
		visibility: visibilityAll,
		want:       upToDate,
		wantStatus: corev1.ConditionFalse,
		wantReason: "VisibilityMismatch",
	},
	"EmptyRepositoryList": {
		reason:     "A selected secret with an empty repository list matches a spec that selects none.",
		secret:     &github.Secret{Visibility: visibilitySelected},
		list:       &github.SelectedReposList{},
		visibility: visibilitySelected,
		want:       upToDate,
		wantStatus: corev1.ConditionTrue,
		wantReason: xpv2.ReasonAvailable,
	},
}

func minimalExternal(secret *github.Secret, list *github.SelectedReposList) *external {
	return newExternal(&fake.MockActionsClient{
		MockGetOrgSecret: func(context.Context, string, string) (*github.Secret, *github.Response, error) {
			return secret, fake.GenerateEmptyResponse(), nil
		},
		MockListSelectedReposForOrgSecret: func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			return list, fake.GenerateEmptyResponse(), nil
		},
	}, nil)
}

func TestObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			cr := newCR(tc.visibility)
			got, err := minimalExternal(tc.secret, tc.list).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			assertReady(t, cr, tc.wantStatus, tc.wantReason)
		})
	}
}

func TestNamespacedObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			e := scopebridge.New[*namespacedv1alpha1.ActionsSecretAccess](minimalExternal(tc.secret, tc.list),
				func() *clusterv1alpha1.ActionsSecretAccess { return &clusterv1alpha1.ActionsSecretAccess{} })
			cr := ddtest.Convert(t, newCR(tc.visibility), &namespacedv1alpha1.ActionsSecretAccess{})
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			c := cr.GetCondition(xpv2.TypeReady)
			if c.Status != tc.wantStatus || c.Reason != tc.wantReason {
				t.Errorf("\n%s\nReady = %s/%s, want %s/%s", tc.reason, c.Status, c.Reason, tc.wantStatus, tc.wantReason)
			}
		})
	}
}
