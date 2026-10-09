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

package organizationwebhook

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// bareWebhook points at an existing hook and declares nothing else: no url,
// content type, events or secret.
func bareWebhook() *clusterv1alpha1.OrganizationWebhook {
	cr := &clusterv1alpha1.OrganizationWebhook{}
	cr.Spec.ForProvider.Org = testOrg
	meta.SetExternalName(cr, testHookIDStr)
	return cr
}

func hookAnswering(h *github.Hook) *external {
	e := newExternal(&fake.MockOrganizationsClient{
		MockGetHook: getHook(h),
	}, nil)
	return &e
}

// minimalObserveCases are GitHub responses stripped of every optional field.
// The config GitHub leaves out is a nil pointer that Observe must not
// dereference.
var minimalObserveCases = map[string]struct {
	reason string
	gh     *github.Hook
	want   managed.ExternalObservation
	ready  xpv2.ConditionReason
}{
	"NilHook": {
		reason: "A nil hook with no error means the hook does not exist.",
		gh:     nil,
		want:   managed.ExternalObservation{ResourceExists: false},
	},
	"ActiveHookWithoutConfig": {
		reason: "An active hook with no config matches a spec that declares nothing.",
		gh:     &github.Hook{ID: github.Ptr(testHookID), Active: github.Ptr(true)},
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
		ready:  xpv2.ReasonAvailable,
	},
	"EmptyHook": {
		reason: "A hook with no fields is inactive, which differs from the default of active.",
		gh:     &github.Hook{},
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false, ResourceLateInitialized: true},
		ready:  xpv2.ReasonUnavailable,
	},
}

func TestObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			cr := bareWebhook()
			got, err := hookAnswering(tc.gh).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Reason != tc.ready {
				t.Errorf("\n%s\nReady reason = %q, want %q", tc.reason, c.Reason, tc.ready)
			}
		})
	}
}

func TestNamespacedObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			e := scopebridge.New[*namespacedv1alpha1.OrganizationWebhook](hookAnswering(tc.gh),
				func() *clusterv1alpha1.OrganizationWebhook { return &clusterv1alpha1.OrganizationWebhook{} })
			cr := ddtest.Convert(t, bareWebhook(), &namespacedv1alpha1.OrganizationWebhook{})
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Reason != tc.ready {
				t.Errorf("\n%s\nReady reason = %q, want %q", tc.reason, c.Reason, tc.ready)
			}
		})
	}
}
