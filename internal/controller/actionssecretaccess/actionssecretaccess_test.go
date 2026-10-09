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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

const (
	testOrg        = "test-org"
	testSecretName = "TEST_SECRET"

	testRepoA         = "repo-a"
	testRepoB         = "repo-b"
	testRepoAID int64 = 12345
	testRepoBID int64 = 67890

	visibilityAll      = "all"
	visibilityPrivate  = "private"
	visibilitySelected = "selected"
)

var upToDate = managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}

func newCR(visibility string, repos ...string) *v1alpha1.ActionsSecretAccess {
	cr := &v1alpha1.ActionsSecretAccess{}
	cr.Spec.ForProvider.Org = testOrg
	cr.Spec.ForProvider.Visibility = visibility
	for _, r := range repos {
		cr.Spec.ForProvider.SelectedRepositories = append(cr.Spec.ForProvider.SelectedRepositories, v1alpha1.SecretSelectedRepo{Repo: r})
	}
	meta.SetExternalName(cr, testSecretName)
	return cr
}

func getSecret(visibility string) func(context.Context, string, string) (*github.Secret, *github.Response, error) {
	return func(_ context.Context, _, _ string) (*github.Secret, *github.Response, error) {
		return &github.Secret{Name: testSecretName, Visibility: visibility}, fake.GenerateEmptyResponse(), nil
	}
}

func listRepos(names ...string) func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return func(_ context.Context, _, _ string, _ *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
		list := &github.SelectedReposList{}
		for _, n := range names {
			list.Repositories = append(list.Repositories, &github.Repository{Name: github.Ptr(n)})
		}
		return list, fake.GenerateEmptyResponse(), nil
	}
}

func listMustNotBeCalled(t *testing.T) func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return func(_ context.Context, _, _ string, _ *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
		t.Error("ListSelectedReposForOrgSecret was called, want no call")
		return &github.SelectedReposList{}, fake.GenerateEmptyResponse(), nil
	}
}

func newExternal(actions *fake.MockActionsClient, repos *fake.MockRepositoriesClient) *external {
	s := &ghclient.Services{Actions: actions}
	if repos != nil {
		s.Repositories = repos
	}
	return &external{github: &ghclient.Client{Services: s}}
}

func assertReady(t *testing.T, cr *v1alpha1.ActionsSecretAccess, status corev1.ConditionStatus, reason xpv2.ConditionReason) xpv2.Condition {
	t.Helper()
	c := cr.GetCondition(xpv2.TypeReady)
	if c.Status != status || c.Reason != reason {
		t.Errorf("Ready = %s/%s, want %s/%s", c.Status, c.Reason, status, reason)
	}
	return c
}

// A missing secret must surface in Ready but report exists+up-to-date: Create
// cannot make a secret without its value, so it must never be reached.
func TestObserveSecretNotFoundReadyFalseAndNoCreate(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret: func(_ context.Context, _, _ string) (*github.Secret, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
		},
	}, nil)

	cr := newCR(visibilitySelected, testRepoA)
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(upToDate, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	c := assertReady(t, cr, corev1.ConditionFalse, "SecretNotFound")
	want := "secret TEST_SECRET not found on GitHub; create it there first (this resource only manages repository access)"
	if c.Message != want {
		t.Errorf("Message = %q, want %q", c.Message, want)
	}
}

// Visibility cannot be changed through the API, so a mismatch must be reported
// in Ready without touching the repository list or triggering Update.
func TestObserveVisibilityMismatchReadyFalseAndNoList(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret:                  getSecret(visibilityPrivate),
		MockListSelectedReposForOrgSecret: listMustNotBeCalled(t),
	}, nil)

	cr := newCR(visibilitySelected, testRepoA)
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(upToDate, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	c := assertReady(t, cr, corev1.ConditionFalse, "VisibilityMismatch")
	want := `visibility is "private" on GitHub but "selected" in spec; change it on GitHub (the API cannot change visibility without the secret value)`
	if c.Message != want {
		t.Errorf("Message = %q, want %q", c.Message, want)
	}
}

// Repository lists are compared as sets; GitHub's ordering must not cause drift.
func TestObserveSelectedSameReposDifferentOrderUpToDate(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos(testRepoB, testRepoA),
	}, nil)

	cr := newCR(visibilitySelected, testRepoA, testRepoB)
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(upToDate, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
}

// An extra repository on GitHub is drift that Update must remove.
func TestObserveSelectedDifferentReposNotUpToDate(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos(testRepoA, testRepoB),
	}, nil)

	got, err := e.Observe(context.Background(), newCR(visibilitySelected, testRepoA))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
}

// GitHub repository names are case-insensitive and neither side's spelling is
// canonical, so a spec name differing only in case from the listed one must not
// read as drift; otherwise Update rewrites the list on every poll while the
// resource shows Ready.
func TestObserveSelectedNameCaseDiffersUpToDate(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos("Repo-A"),
	}, nil)

	cr := newCR(visibilitySelected, "REPO-A")
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(upToDate, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
}

// The declared list is a set: a repository listed twice in spec, in any case,
// must not read as drift against GitHub's single entry; otherwise Update
// rewrites the list on every poll while the resource shows Ready.
func TestObserveSelectedDuplicateSpecRepoUpToDate(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret:                  getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: listRepos(testRepoA),
	}, nil)

	cr := newCR(visibilitySelected, testRepoA, "Repo-A")
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(upToDate, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
}

// Repositories beyond the first page must be compared; otherwise a secret with
// more than 100 repositories would report drift forever.
func TestObserveSelectedPaginationCollectsAllPages(t *testing.T) {
	var pages []int
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret: getSecret(visibilitySelected),
		MockListSelectedReposForOrgSecret: func(_ context.Context, _, _ string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			pages = append(pages, opts.Page)
			if opts.Page == 0 {
				resp := fake.GenerateEmptyResponse()
				resp.NextPage = 2
				return &github.SelectedReposList{Repositories: []*github.Repository{{Name: github.Ptr(testRepoA)}}}, resp, nil
			}
			return &github.SelectedReposList{Repositories: []*github.Repository{{Name: github.Ptr(testRepoB)}}}, fake.GenerateEmptyResponse(), nil
		},
	}, nil)

	cr := newCR(visibilitySelected, testRepoA, testRepoB)
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(upToDate, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff([]int{0, 2}, pages); diff != "" {
		t.Errorf("pages requested: -want, +got:\n%s", diff)
	}
}

// For all/private there is no repository list on GitHub, so a matching
// visibility is Ready without any List call.
func TestObserveNotSelectedVisibilityMatchesReadyAndNoList(t *testing.T) {
	for _, v := range []string{visibilityAll, visibilityPrivate} {
		t.Run(v, func(t *testing.T) {
			e := newExternal(&fake.MockActionsClient{
				MockGetOrgSecret:                  getSecret(v),
				MockListSelectedReposForOrgSecret: listMustNotBeCalled(t),
			}, nil)

			cr := newCR(v)
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(upToDate, got); diff != "" {
				t.Errorf("Observe: -want, +got:\n%s", diff)
			}
			assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
		})
	}
}

// listSelected returns one page of selected repositories carrying their IDs.
func listSelected(ids map[string]int64) func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return func(_ context.Context, _, _ string, _ *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
		list := &github.SelectedReposList{}
		for n, id := range ids {
			list.Repositories = append(list.Repositories, &github.Repository{ID: github.Ptr(id), Name: github.Ptr(n)})
		}
		return list, fake.GenerateEmptyResponse(), nil
	}
}

func recordSet(got *github.SelectedRepoIDs) func(context.Context, string, string, []int64) (*github.Response, error) {
	return func(_ context.Context, _, _ string, ids []int64) (*github.Response, error) {
		*got = ids
		return fake.GenerateEmptyResponse(), nil
	}
}

func repoGetMustNotBeCalled(t *testing.T) *fake.MockRepositoriesClient {
	return &fake.MockRepositoriesClient{
		MockGet: func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
			t.Errorf("Repositories.Get(%q) was called, want no call", repo)
			return &github.Repository{}, fake.GenerateEmptyResponse(), nil
		},
	}
}

// GitHub's Set endpoint takes repository IDs, so Update must resolve spec
// names the secret does not yet have and send exactly the spec IDs in spec order.
func TestUpdateSetsResolvedRepoIDs(t *testing.T) {
	var got github.SelectedRepoIDs
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: listRepos(),
		MockSetSelectedReposForOrgSecret:  recordSet(&got),
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
			ids := map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID}
			return &github.Repository{ID: github.Ptr(ids[repo]), Name: github.Ptr(repo)}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(actions, repos)

	if _, err := e.Update(context.Background(), newCR(visibilitySelected, testRepoA, testRepoB)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(github.SelectedRepoIDs{testRepoAID, testRepoBID}, got); diff != "" {
		t.Errorf("SetSelectedReposForOrgSecret IDs: -want, +got:\n%s", diff)
	}
}

// Update cost must scale with the repositories added, not the list size:
// removing an extra repository must take every ID from the list and cost no
// Repositories.Get, or a large secret cannot converge within the reconcile timeout.
func TestUpdateRemoveExtraRepoNoRepoGet(t *testing.T) {
	var got github.SelectedRepoIDs
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: listSelected(map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID, "repo-extra": 1}),
		MockSetSelectedReposForOrgSecret:  recordSet(&got),
	}
	e := newExternal(actions, repoGetMustNotBeCalled(t))

	if _, err := e.Update(context.Background(), newCR(visibilitySelected, testRepoA, testRepoB)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(github.SelectedRepoIDs{testRepoAID, testRepoBID}, got); diff != "" {
		t.Errorf("SetSelectedReposForOrgSecret IDs: -want, +got:\n%s", diff)
	}
}

// Adding one repository must cost exactly one Repositories.Get, for that name
// only; the IDs of repositories already selected come from the list.
func TestUpdateAddOneRepoGetsOnlyThatRepo(t *testing.T) {
	var got github.SelectedRepoIDs
	var gets []string
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: listSelected(map[string]int64{testRepoA: testRepoAID}),
		MockSetSelectedReposForOrgSecret:  recordSet(&got),
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
			gets = append(gets, repo)
			return &github.Repository{ID: github.Ptr(testRepoBID), Name: github.Ptr(repo)}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(actions, repos)

	if _, err := e.Update(context.Background(), newCR(visibilitySelected, testRepoA, testRepoB)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff([]string{testRepoB}, gets); diff != "" {
		t.Errorf("Repositories.Get names: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff(github.SelectedRepoIDs{testRepoAID, testRepoBID}, got); diff != "" {
		t.Errorf("SetSelectedReposForOrgSecret IDs: -want, +got:\n%s", diff)
	}
}

// IDs on later list pages must be used too; otherwise every repository past
// the first 100 would cost a Repositories.Get on each Update.
func TestUpdateIDsFromLaterPagesNoRepoGet(t *testing.T) {
	var got github.SelectedRepoIDs
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: func(_ context.Context, _, _ string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			if opts.Page == 0 {
				resp := fake.GenerateEmptyResponse()
				resp.NextPage = 2
				return &github.SelectedReposList{Repositories: []*github.Repository{{ID: github.Ptr(testRepoAID), Name: github.Ptr(testRepoA)}}}, resp, nil
			}
			return &github.SelectedReposList{Repositories: []*github.Repository{{ID: github.Ptr(testRepoBID), Name: github.Ptr(testRepoB)}}}, fake.GenerateEmptyResponse(), nil
		},
		MockSetSelectedReposForOrgSecret: recordSet(&got),
	}
	e := newExternal(actions, repoGetMustNotBeCalled(t))

	if _, err := e.Update(context.Background(), newCR(visibilitySelected, testRepoA, testRepoB)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(github.SelectedRepoIDs{testRepoAID, testRepoBID}, got); diff != "" {
		t.Errorf("SetSelectedReposForOrgSecret IDs: -want, +got:\n%s", diff)
	}
}

// GitHub repository names are case-insensitive and neither side's spelling is
// canonical, so a spec name differing only in case from a listed one must take
// its ID from the list: no Repositories.Get, or every Update pays for a lookup
// GitHub resolves to the same repository.
func TestUpdateNameCaseDiffersIDFromListNoRepoGet(t *testing.T) {
	var got github.SelectedRepoIDs
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: listSelected(map[string]int64{"Repo-A": testRepoAID, "repo-extra": 1}),
		MockSetSelectedReposForOrgSecret:  recordSet(&got),
	}
	e := newExternal(actions, repoGetMustNotBeCalled(t))

	if _, err := e.Update(context.Background(), newCR(visibilitySelected, "REPO-A")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(github.SelectedRepoIDs{testRepoAID}, got); diff != "" {
		t.Errorf("SetSelectedReposForOrgSecret IDs: -want, +got:\n%s", diff)
	}
}

// The declared list is a set: a repository listed twice in spec must be sent
// to GitHub once, with its ID taken from the list, so Update writes the same
// set Observe compares against.
func TestUpdateDuplicateSpecRepoSetOnceNoRepoGet(t *testing.T) {
	var got github.SelectedRepoIDs
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: listSelected(map[string]int64{testRepoA: testRepoAID, "repo-extra": 1}),
		MockSetSelectedReposForOrgSecret:  recordSet(&got),
	}
	e := newExternal(actions, repoGetMustNotBeCalled(t))

	if _, err := e.Update(context.Background(), newCR(visibilitySelected, testRepoA, "Repo-A")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(github.SelectedRepoIDs{testRepoAID}, got); diff != "" {
		t.Errorf("SetSelectedReposForOrgSecret IDs: -want, +got:\n%s", diff)
	}
}

// A new repository declared twice must cost one Repositories.Get, for its first
// spelling, and be sent to GitHub once: the declared list is a set.
func TestUpdateDuplicateNewRepoOneRepoGetSetOnce(t *testing.T) {
	var got github.SelectedRepoIDs
	var gets []string
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: listSelected(map[string]int64{testRepoA: testRepoAID}),
		MockSetSelectedReposForOrgSecret:  recordSet(&got),
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
			gets = append(gets, repo)
			return &github.Repository{ID: github.Ptr(testRepoBID), Name: github.Ptr(repo)}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(actions, repos)

	if _, err := e.Update(context.Background(), newCR(visibilitySelected, testRepoA, testRepoB, "Repo-B")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff([]string{testRepoB}, gets); diff != "" {
		t.Errorf("Repositories.Get names: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff(github.SelectedRepoIDs{testRepoAID, testRepoBID}, got); diff != "" {
		t.Errorf("SetSelectedReposForOrgSecret IDs: -want, +got:\n%s", diff)
	}
}

// A declared name that resolves to an already-selected repository under
// another name means the repository was renamed on GitHub: the list keeps
// reporting drift, and writing would change nothing and repeat every poll, so
// Update must fail with the fix instead of calling Set.
func TestUpdateRenamedRepoErrorAndNoSet(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockListSelectedReposForOrgSecret: listSelected(map[string]int64{"new-name": testRepoAID, testRepoB: testRepoBID}),
		MockSetSelectedReposForOrgSecret: func(_ context.Context, _, _ string, _ []int64) (*github.Response, error) {
			t.Error("SetSelectedReposForOrgSecret was called, want no call")
			return fake.GenerateEmptyResponse(), nil
		},
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: func(_ context.Context, _, _ string) (*github.Repository, *github.Response, error) {
			// GitHub redirects the old name to the renamed repository.
			return &github.Repository{ID: github.Ptr(testRepoAID), Name: github.Ptr("new-name")}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(actions, repos)

	_, err := e.Update(context.Background(), newCR(visibilitySelected, "old-name", testRepoB))
	want := "repository old-name is named new-name on GitHub; update selectedRepositories to the new name"
	if err == nil || err.Error() != want {
		t.Errorf("Update error = %v, want %q", err, want)
	}
}

// A resource being deleted must be reported gone without calling GitHub.
// Delete is a no-op, so if Observe kept reporting it exists the reconciler
// would call Delete every poll and never remove the finalizer.
func TestObserveDeletingNotExistsAndNoClientCall(t *testing.T) {
	e := newExternal(&fake.MockActionsClient{
		MockGetOrgSecret: func(_ context.Context, _, _ string) (*github.Secret, *github.Response, error) {
			t.Error("GetOrgSecret was called, want no call")
			return &github.Secret{Name: testSecretName, Visibility: visibilitySelected}, fake.GenerateEmptyResponse(), nil
		},
		MockListSelectedReposForOrgSecret: listMustNotBeCalled(t),
	}, nil)

	cr := newCR(visibilitySelected, testRepoA)
	cr.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: false}, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
}

// The provider does not own the secret, so deleting the resource must not call
// GitHub; any call would panic on the nil client interfaces.
func TestDeleteNoClientCall(t *testing.T) {
	e := &external{github: &ghclient.Client{Services: &ghclient.Services{}}}

	if _, err := e.Delete(context.Background(), newCR(visibilitySelected, testRepoA)); err != nil {
		t.Errorf("Delete returned %v, want nil", err)
	}
}
