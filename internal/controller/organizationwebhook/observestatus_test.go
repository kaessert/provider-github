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
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func assertUnavailable(t *testing.T, cr *v1alpha1.OrganizationWebhook) {
	t.Helper()
	c := cr.GetCondition(xpv2.TypeReady)
	if c.Status != corev1.ConditionFalse || c.Reason != xpv2.ReasonUnavailable {
		t.Errorf("Ready = %s/%s, want False/%s", c.Status, c.Reason, xpv2.ReasonUnavailable)
	}
}

// Every drift path reports Ready=False while Update corrects it.
func TestObserveStatusDriftSetsUnavailable(t *testing.T) {
	cases := map[string]struct {
		hook *github.Hook
	}{
		"Config": {hook: ghHook(func(h *github.Hook) { h.Events = []string{"push"} })},
		"Secret": {hook: ghHook(withGHSecret)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newExternal(&fake.MockOrganizationsClient{MockGetHook: getHook(tc.hook)}, newKube(t))

			cr := newCR(withExternalName(testHookIDStr))
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Errorf("Observe = %+v, want exists and not up to date", got)
			}
			assertUnavailable(t, cr)
		})
	}
}

// An Observe-only import omits url, contentType and events: none is compared,
// whatever the hook holds.
func TestObserveStatusOmittedFieldsNoDrift(t *testing.T) {
	e := newExternal(&fake.MockOrganizationsClient{MockGetHook: getHook(ghHook(func(h *github.Hook) {
		h.Config.URL = github.Ptr(testOtherURL)
		h.Config.ContentType = github.Ptr("form")
		h.Events = []string{"release"}
	}))}, newKube(t))

	cr := newCR(withExternalName(testHookIDStr), func(cr *v1alpha1.OrganizationWebhook) {
		cr.Spec.ManagementPolicies = xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
		cr.Spec.ForProvider.URL = ""
		cr.Spec.ForProvider.ContentType = ""
		cr.Spec.ForProvider.Events = nil
	})
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonAvailable {
		t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.ReasonAvailable)
	}
	if cr.Status.AtProvider.ID != testHookID {
		t.Errorf("status.atProvider.id = %d, want %d", cr.Status.AtProvider.ID, testHookID)
	}
}

// Without a url there is nothing to match a hook by, so a resource with no
// numeric external name is absent and the hook list is not read.
func TestObserveStatusOmittedURLNoLookupByURL(t *testing.T) {
	e := newExternal(&fake.MockOrganizationsClient{
		MockListHooks: func(context.Context, string, *github.ListOptions) ([]*github.Hook, *github.Response, error) {
			t.Error("ListHooks was called, want no call")
			return nil, fake.GenerateEmptyResponse(), nil
		},
	}, newKube(t))

	cr := newCR(func(cr *v1alpha1.OrganizationWebhook) { cr.Spec.ForProvider.URL = "" })
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Errorf("ResourceExists = true, want false")
	}
}
