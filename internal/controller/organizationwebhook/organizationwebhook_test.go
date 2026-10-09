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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

const (
	testOrg             = "example-org"
	testCRName          = "sample-webhook"
	testHookID    int64 = 12345
	testHookIDStr       = "12345"
	testURL             = "https://hooks.example.com/github"
	testOtherURL        = "https://hooks.example.com/other"

	testNamespace  = "crossplane-system"
	testSourceName = "webhook-source"
	testSourceKey  = "token"
	testConnName   = "webhook-conn"
	testSecret     = "new-secret"
	testOldSecret  = "old-secret"
	maskedSecret   = "********"
)

type modifier func(*v1alpha1.OrganizationWebhook)

func withExternalName(n string) modifier {
	return func(cr *v1alpha1.OrganizationWebhook) { meta.SetExternalName(cr, n) }
}

func withSecretRef() modifier {
	return func(cr *v1alpha1.OrganizationWebhook) {
		cr.Spec.ForProvider.SecretKeyRef = &xpv2.SecretKeySelector{
			SecretReference: xpv2.SecretReference{Name: testSourceName, Namespace: testNamespace},
			Key:             testSourceKey,
		}
	}
}

func withConnSecretRef() modifier {
	return func(cr *v1alpha1.OrganizationWebhook) {
		cr.Spec.WriteConnectionSecretToReference = &xpv2.SecretReference{Name: testConnName, Namespace: testNamespace}
	}
}

func newCR(m ...modifier) *v1alpha1.OrganizationWebhook {
	cr := &v1alpha1.OrganizationWebhook{}
	cr.Name = testCRName
	cr.Spec.ForProvider.Org = testOrg
	cr.Spec.ForProvider.URL = testURL
	cr.Spec.ForProvider.ContentType = "json"
	cr.Spec.ForProvider.Events = []string{"push", "pull_request"}
	meta.SetExternalName(cr, testCRName)
	for _, f := range m {
		f(cr)
	}
	return cr
}

// ghHook builds the GitHub-side hook matching newCR's defaults.
func ghHook(m ...func(*github.Hook)) *github.Hook {
	h := &github.Hook{
		ID:     github.Ptr(testHookID),
		Events: []string{"push", "pull_request"},
		Active: github.Ptr(true),
		Config: &github.HookConfig{
			URL:         github.Ptr(testURL),
			ContentType: github.Ptr("json"),
			InsecureSSL: github.Ptr("0"),
		},
	}
	for _, f := range m {
		f(h)
	}
	return h
}

func withGHSecret(h *github.Hook) { h.Config.Secret = github.Ptr(maskedSecret) }

func getHook(h *github.Hook) func(context.Context, string, int64) (*github.Hook, *github.Response, error) {
	return func(_ context.Context, _ string, _ int64) (*github.Hook, *github.Response, error) {
		return h, fake.GenerateEmptyResponse(), nil
	}
}

func secret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}, Data: data}
}

func sourceSecret(value string) *corev1.Secret {
	return secret(testSourceName, map[string][]byte{testSourceKey: []byte(value)})
}

func connSecret(value string) *corev1.Secret {
	return secret(testConnName, map[string][]byte{connectionSecretKey: []byte(value)})
}

func newKube(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// newExternal wires only the organizations client. Other fake methods
// stay nil, so an unexpected call panics.
func newExternal(orgs *fake.MockOrganizationsClient, kube client.Client) external {
	return external{github: &ghclient.Client{Services: &ghclient.Services{Organizations: orgs}}, kube: kube}
}

// A known hook ID is fetched directly; a matching hook is up to date,
// Available, and its ID is recorded in status.
func TestObserve_ByID_UpToDate(t *testing.T) {
	var gotID int64
	orgs := &fake.MockOrganizationsClient{
		MockGetHook: func(_ context.Context, _ string, id int64) (*github.Hook, *github.Response, error) {
			gotID = id
			return ghHook(), fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(orgs, newKube(t))

	cr := newCR(withExternalName(testHookIDStr))
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	if gotID != testHookID {
		t.Errorf("GetHook id = %d, want %d", gotID, testHookID)
	}
	if cr.Status.AtProvider.ID != testHookID {
		t.Errorf("status.atProvider.id = %d, want %d", cr.Status.AtProvider.ID, testHookID)
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.Available().Reason {
		t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.Available().Reason)
	}
}

// A hook deleted on GitHub must be re-created, not reported as an error.
func TestObserve_ByID_404_ReportsNotExists(t *testing.T) {
	orgs := &fake.MockOrganizationsClient{
		MockGetHook: func(_ context.Context, _ string, _ int64) (*github.Hook, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
		},
	}
	e := newExternal(orgs, newKube(t))

	got, err := e.Observe(context.Background(), newCR(withExternalName(testHookIDStr)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Errorf("ResourceExists = true, want false")
	}
}

// Without a known ID, an existing hook with the spec URL is adopted even
// past the first page, and its ID becomes the external name so later
// reconciles fetch it directly. The external name change must be
// persisted.
func TestObserve_AdoptsByURLOnLaterPage(t *testing.T) {
	var pages []int
	orgs := &fake.MockOrganizationsClient{
		MockListHooks: func(_ context.Context, _ string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
			pages = append(pages, opts.Page)
			if opts.Page == 0 {
				other := ghHook(func(h *github.Hook) {
					h.ID = github.Ptr(int64(99999))
					h.Config.URL = github.Ptr(testOtherURL)
				})
				return []*github.Hook{other}, &github.Response{NextPage: 2}, nil
			}
			return []*github.Hook{ghHook()}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(orgs, newKube(t))

	cr := newCR()
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true, ResourceLateInitialized: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff([]int{0, 2}, pages); diff != "" {
		t.Errorf("requested pages: -want, +got:\n%s", diff)
	}
	if n := meta.GetExternalName(cr); n != testHookIDStr {
		t.Errorf("external name = %q, want %q", n, testHookIDStr)
	}
	if cr.Status.AtProvider.ID != testHookID {
		t.Errorf("status.atProvider.id = %d, want %d", cr.Status.AtProvider.ID, testHookID)
	}
}

// Without a known ID and no hook with the spec URL, the hook must be created.
func TestObserve_URLNotListed_ReportsNotExists(t *testing.T) {
	orgs := &fake.MockOrganizationsClient{
		MockListHooks: func(_ context.Context, _ string, _ *github.ListOptions) ([]*github.Hook, *github.Response, error) {
			other := ghHook(func(h *github.Hook) { h.Config.URL = github.Ptr(testOtherURL) })
			return []*github.Hook{other}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(orgs, newKube(t))

	got, err := e.Observe(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Errorf("ResourceExists = true, want false")
	}
}

// GitHub does not preserve event order, so the same events in another
// order are not drift.
func TestObserve_EventsReordered_UpToDate(t *testing.T) {
	orgs := &fake.MockOrganizationsClient{
		MockGetHook: getHook(ghHook(func(h *github.Hook) { h.Events = []string{"pull_request", "push"} })),
	}
	e := newExternal(orgs, newKube(t))

	got, err := e.Observe(context.Background(), newCR(withExternalName(testHookIDStr)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true")
	}
}

// Each managed field is compared independently; drift in any one of
// them must trigger an Update. Unset active and insecureSsl mean true
// and false.
func TestObserve_FieldDrift_ReportsNotUpToDate(t *testing.T) {
	cases := map[string]*github.Hook{
		"Url":         ghHook(func(h *github.Hook) { h.Config.URL = github.Ptr(testOtherURL) }),
		"ContentType": ghHook(func(h *github.Hook) { h.Config.ContentType = github.Ptr("form") }),
		"Events":      ghHook(func(h *github.Hook) { h.Events = []string{"push"} }),
		"Active":      ghHook(func(h *github.Hook) { h.Active = github.Ptr(false) }),
		"InsecureSsl": ghHook(func(h *github.Hook) { h.Config.InsecureSSL = github.Ptr("1") }),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			e := newExternal(&fake.MockOrganizationsClient{MockGetHook: getHook(h)}, newKube(t))

			got, err := e.Observe(context.Background(), newCR(withExternalName(testHookIDStr)))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Errorf("Observe = %+v, want exists and not up to date", got)
			}
		})
	}
}

// GitHub only reports that a secret is set, so the referenced secret is
// compared with the one recorded in the connection secret at the last
// apply. A rotated, missing or unwanted secret must trigger an Update.
func TestObserve_Secret(t *testing.T) {
	cases := map[string]struct {
		cr       *v1alpha1.OrganizationWebhook
		gh       *github.Hook
		objs     []client.Object
		upToDate bool
	}{
		"AppliedMatchesDesired": {
			cr:       newCR(withExternalName(testHookIDStr), withSecretRef(), withConnSecretRef()),
			gh:       ghHook(withGHSecret),
			objs:     []client.Object{sourceSecret(testSecret), connSecret(testSecret)},
			upToDate: true,
		},
		"Rotated": {
			cr:   newCR(withExternalName(testHookIDStr), withSecretRef(), withConnSecretRef()),
			gh:   ghHook(withGHSecret),
			objs: []client.Object{sourceSecret(testSecret), connSecret(testOldSecret)},
		},
		"NeverApplied": {
			cr:   newCR(withExternalName(testHookIDStr), withSecretRef(), withConnSecretRef()),
			gh:   ghHook(withGHSecret),
			objs: []client.Object{sourceSecret(testSecret)},
		},
		"GitHubHasNoSecret": {
			cr:   newCR(withExternalName(testHookIDStr), withSecretRef(), withConnSecretRef()),
			gh:   ghHook(),
			objs: []client.Object{sourceSecret(testSecret), connSecret(testSecret)},
		},
		"GitHubHasSecretSpecHasNone": {
			cr:   newCR(withExternalName(testHookIDStr), withConnSecretRef()),
			gh:   ghHook(withGHSecret),
			objs: []client.Object{connSecret(testSecret)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newExternal(&fake.MockOrganizationsClient{MockGetHook: getHook(tc.gh)}, newKube(t, tc.objs...))

			got, err := e.Observe(context.Background(), tc.cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate != tc.upToDate {
				t.Errorf("Observe = %+v, want exists and ResourceUpToDate=%v", got, tc.upToDate)
			}
		})
	}
}

// Rotation detection needs the connection secret, so a secretKeyRef
// without writeConnectionSecretToRef is rejected before any
// GitHub call.
func TestObserve_SecretRefWithoutConnectionSecret_Errors(t *testing.T) {
	e := newExternal(&fake.MockOrganizationsClient{}, newKube(t, sourceSecret(testSecret)))

	_, err := e.Observe(context.Background(), newCR(withExternalName(testHookIDStr), withSecretRef()))
	if err == nil || err.Error() != errNoConnectionSecretRef {
		t.Errorf("Observe error = %v, want %q", err, errNoConnectionSecretRef)
	}
}

// Create sends the full desired config, records the new ID as the
// external name, and publishes the applied secret for later drift checks.
func TestCreate_SetsExternalNameAndPublishesSecret(t *testing.T) {
	var sent *github.Hook
	orgs := &fake.MockOrganizationsClient{
		MockCreateHook: func(_ context.Context, _ string, h *github.Hook) (*github.Hook, *github.Response, error) {
			sent = h
			return &github.Hook{ID: github.Ptr(testHookID)}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(orgs, newKube(t, sourceSecret(testSecret)))

	cr := newCR(withSecretRef(), withConnSecretRef())
	got, err := e.Create(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := managed.ExternalCreation{ConnectionDetails: managed.ConnectionDetails{connectionSecretKey: []byte(testSecret)}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Create: -want, +got:\n%s", diff)
	}
	wantHook := &github.Hook{
		Events: []string{"push", "pull_request"},
		Active: github.Ptr(true),
		Config: &github.HookConfig{
			URL:         github.Ptr(testURL),
			ContentType: github.Ptr("json"),
			InsecureSSL: github.Ptr("0"),
			Secret:      github.Ptr(testSecret),
		},
	}
	if diff := cmp.Diff(wantHook, sent); diff != "" {
		t.Errorf("CreateHook hook: -want, +got:\n%s", diff)
	}
	if n := meta.GetExternalName(cr); n != testHookIDStr {
		t.Errorf("external name = %q, want %q", n, testHookIDStr)
	}
	if cr.Status.AtProvider.ID != testHookID {
		t.Errorf("status.atProvider.id = %d, want %d", cr.Status.AtProvider.ID, testHookID)
	}
}

// Without a secretKeyRef no secret is sent and nothing is published.
func TestCreate_NoSecret(t *testing.T) {
	var sent *github.Hook
	orgs := &fake.MockOrganizationsClient{
		MockCreateHook: func(_ context.Context, _ string, h *github.Hook) (*github.Hook, *github.Response, error) {
			sent = h
			return &github.Hook{ID: github.Ptr(testHookID)}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(orgs, newKube(t))

	got, err := e.Create(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ConnectionDetails != nil {
		t.Errorf("ConnectionDetails = %v, want nil", got.ConnectionDetails)
	}
	if sent.Config.Secret != nil {
		t.Errorf("CreateHook secret = %q, want unset", *sent.Config.Secret)
	}
}

// Update edits the hook by its ID with the current secret and publishes
// it, so a rotation stops being drift once applied.
func TestUpdate_EditsByIDAndPublishesSecret(t *testing.T) {
	var gotID int64
	var sent *github.Hook
	orgs := &fake.MockOrganizationsClient{
		MockEditHook: func(_ context.Context, _ string, id int64, h *github.Hook) (*github.Hook, *github.Response, error) {
			gotID, sent = id, h
			return h, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(orgs, newKube(t, sourceSecret(testSecret), connSecret(testOldSecret)))

	got, err := e.Update(context.Background(), newCR(withExternalName(testHookIDStr), withSecretRef(), withConnSecretRef()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := managed.ExternalUpdate{ConnectionDetails: managed.ConnectionDetails{connectionSecretKey: []byte(testSecret)}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Update: -want, +got:\n%s", diff)
	}
	if gotID != testHookID {
		t.Errorf("EditHook id = %d, want %d", gotID, testHookID)
	}
	if sent.Config.GetSecret() != testSecret {
		t.Errorf("EditHook secret = %q, want %q", sent.Config.GetSecret(), testSecret)
	}
}

// A hook already gone on GitHub is a successful delete.
func TestDelete_404IsNotAnError(t *testing.T) {
	orgs := &fake.MockOrganizationsClient{
		MockDeleteHook: func(_ context.Context, _ string, _ int64) (*github.Response, error) {
			return fake.GenerateEmptyResponse(), fake.Generate404Response()
		},
	}
	e := newExternal(orgs, newKube(t))

	if _, err := e.Delete(context.Background(), newCR(withExternalName(testHookIDStr))); err != nil {
		t.Errorf("Delete returned %v on 404, want nil", err)
	}
}

// A namespaced resource reads the webhook secret from its own namespace only,
// whatever namespace the selector names.
func TestDesiredSecretSecretNamespacePinned(t *testing.T) {
	kube := newKube(t, sourceSecret(testSecret))
	ref := &xpv2.SecretKeySelector{
		SecretReference: xpv2.SecretReference{Name: testSourceName, Namespace: "tenant-b"},
		Key:             testSourceKey,
	}

	free := external{kube: kube}
	if _, err := free.desiredSecret(context.Background(), ref); err == nil {
		t.Fatal("cluster-scoped resource read a Secret from the namespace in the selector, which holds none")
	}

	pinned := external{kube: kube, secretNamespace: testNamespace}
	got, err := pinned.desiredSecret(context.Background(), ref)
	if err != nil {
		t.Fatalf("desiredSecret: %v", err)
	}
	if got != testSecret {
		t.Errorf("secret = %q, want the one in %s", got, testNamespace)
	}
}
