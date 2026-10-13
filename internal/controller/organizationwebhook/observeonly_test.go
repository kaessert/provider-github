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

	"github.com/google/go-github/v90/github"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/clients/fake"
)

// observeOnlyCR is what an Observe-only import declares: the organization and
// the hook ID, and nothing else.
func observeOnlyCR(policies ...xpv2.ManagementAction) *v1alpha1.OrganizationWebhook {
	cr := newCR(withExternalName(testHookIDStr), func(cr *v1alpha1.OrganizationWebhook) {
		cr.Spec.ForProvider.URL = ""
		cr.Spec.ForProvider.ContentType = ""
		cr.Spec.ForProvider.Events = nil
	})
	cr.Spec.ManagementPolicies = policies
	return cr
}

// disabledInsecureHook is a hook the spec's defaults (active, secure, no
// secret) do not describe.
func disabledInsecureHook() *github.Hook {
	return ghHook(func(h *github.Hook) {
		h.Active = github.Ptr(false)
		h.Config.InsecureSSL = github.Ptr("1")
		withGHSecret(h)
	})
}

func observeHook(t *testing.T, cr *v1alpha1.OrganizationWebhook, h *github.Hook) bool {
	t.Helper()
	e := newExternal(&fake.MockOrganizationsClient{MockGetHook: getHook(h)}, newKube(t))
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists {
		t.Fatalf("Observe = %+v, want the hook to exist", got)
	}
	return got.ResourceUpToDate
}

// An omitted active, insecureSsl and secretKeyRef are not compared under an
// Observe-only policy.
func TestObserveOnlyOmittedFieldsNoDrift(t *testing.T) {
	cr := observeOnlyCR(xpv2.ManagementActionObserve)
	if !observeHook(t, cr, disabledInsecureHook()) {
		t.Fatalf("ResourceUpToDate = false, want true")
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonAvailable {
		t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.ReasonAvailable)
	}
}

// Under a policy that writes, the omitted fields keep their defaults and
// report drift, as before.
func TestObserveWritesAllowedOmittedFieldsStillDrift(t *testing.T) {
	for name, hook := range map[string]*github.Hook{
		"Active":      ghHook(func(h *github.Hook) { h.Active = github.Ptr(false) }),
		"InsecureSSL": ghHook(func(h *github.Hook) { h.Config.InsecureSSL = github.Ptr("1") }),
		"Secret":      ghHook(withGHSecret),
	} {
		for policyName, policies := range map[string][]xpv2.ManagementAction{
			"Default":       nil,
			"All":           {xpv2.ManagementActionAll},
			"ObserveCreate": {xpv2.ManagementActionObserve, xpv2.ManagementActionCreate},
		} {
			t.Run(name+policyName, func(t *testing.T) {
				cr := observeOnlyCR(policies...)
				if observeHook(t, cr, hook) {
					t.Fatalf("ResourceUpToDate = true, want false")
				}
				assertUnavailable(t, cr)
			})
		}
	}
}

// A field the Observe-only spec does declare is still compared, an explicitly
// empty events list included.
func TestObserveOnlyDeclaredFieldsStillDrift(t *testing.T) {
	cases := map[string]struct {
		hook *github.Hook
		mod  func(*v1alpha1.OrganizationWebhook)
	}{
		"Active": {
			hook: disabledInsecureHook(),
			mod:  func(cr *v1alpha1.OrganizationWebhook) { cr.Spec.ForProvider.Active = github.Ptr(true) },
		},
		"InsecureSSL": {
			hook: disabledInsecureHook(),
			mod: func(cr *v1alpha1.OrganizationWebhook) {
				cr.Spec.ForProvider.Active = github.Ptr(false)
				cr.Spec.ForProvider.InsecureSSL = github.Ptr(false)
			},
		},
		"EmptyEvents": {
			hook: ghHook(),
			mod:  func(cr *v1alpha1.OrganizationWebhook) { cr.Spec.ForProvider.Events = []string{} },
		},
		"Events": {
			hook: ghHook(),
			mod:  func(cr *v1alpha1.OrganizationWebhook) { cr.Spec.ForProvider.Events = []string{"release"} },
		},
		"URL": {
			hook: ghHook(),
			mod:  func(cr *v1alpha1.OrganizationWebhook) { cr.Spec.ForProvider.URL = testOtherURL },
		},
		"ContentType": {
			hook: ghHook(),
			mod:  func(cr *v1alpha1.OrganizationWebhook) { cr.Spec.ForProvider.ContentType = "form" },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyCR(xpv2.ManagementActionObserve)
			tc.mod(cr)
			if observeHook(t, cr, tc.hook) {
				t.Fatalf("ResourceUpToDate = true, want false")
			}
			assertUnavailable(t, cr)
		})
	}
}
