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

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

// Observe mirrors the hook GitHub reports into status.atProvider, whether or
// not it matches the spec. The webhook secret is not part of the mirror.
func TestObserveMirrorsHook(t *testing.T) {
	hook := ghHook(func(h *github.Hook) {
		h.Events = []string{"push", "issues"}
		h.Active = github.Ptr(false)
		h.Config.ContentType = github.Ptr("form")
		h.Config.InsecureSSL = github.Ptr("1")
		h.Config.Secret = github.Ptr(maskedSecret)
	})
	e := newExternal(&fake.MockOrganizationsClient{MockGetHook: getHook(hook)}, newKube(t))

	cr := newCR(withExternalName(testHookIDStr))
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := v1alpha1.OrganizationWebhookObservation{
		ID:          testHookID,
		Org:         testOrg,
		URL:         testURL,
		ContentType: "form",
		Events:      []string{"issues", "push"},
		Active:      github.Ptr(false),
		InsecureSSL: github.Ptr(true),
	}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}

// A hook GitHub reports with no events mirrors an empty list, not an absent one.
func TestMirrorHookNoEventsIsEmptyList(t *testing.T) {
	for name, events := range map[string][]string{"Nil": nil, "Empty": {}} {
		t.Run(name, func(t *testing.T) {
			ap := v1alpha1.OrganizationWebhookObservation{Events: []string{"push"}}
			mirrorHook(&ap, ghHook(func(h *github.Hook) { h.Events = events }), testOrg)
			if ap.Events == nil || len(ap.Events) != 0 {
				t.Errorf("events = %#v, want a non-nil empty list", ap.Events)
			}
		})
	}
}
