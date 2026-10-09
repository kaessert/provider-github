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

package organizationvariable

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

const (
	testOrg          = "acme"
	testVariableName = "FOO"
	testValue        = "bar"

	testRepoA         = "repo-a"
	testRepoB         = "repo-b"
	testRepoAID int64 = 1001
	testRepoBID int64 = 1002
)

type modifier func(*v1alpha1.OrganizationVariable)

func withValue(v string) modifier {
	return func(cr *v1alpha1.OrganizationVariable) { cr.Spec.ForProvider.Value = v }
}

func withVisibility(v string) modifier {
	return func(cr *v1alpha1.OrganizationVariable) { cr.Spec.ForProvider.Visibility = v }
}

func withSelectedRepos(names ...string) modifier {
	return func(cr *v1alpha1.OrganizationVariable) {
		cr.Spec.ForProvider.SelectedRepositories = nil
		for _, n := range names {
			cr.Spec.ForProvider.SelectedRepositories = append(cr.Spec.ForProvider.SelectedRepositories,
				v1alpha1.VariableSelectedRepo{Repo: n})
		}
	}
}

func newCR(m ...modifier) *v1alpha1.OrganizationVariable {
	cr := &v1alpha1.OrganizationVariable{}
	cr.Spec.ForProvider.Org = testOrg
	cr.Spec.ForProvider.Value = testValue
	cr.Spec.ForProvider.Visibility = "all"
	meta.SetExternalName(cr, testVariableName)
	for _, f := range m {
		f(cr)
	}
	return cr
}

// repoIDByName backs Repositories.Get so the controller's name-to-ID
// lookups in selected-repo flows resolve to known IDs in tests.
func mockRepoGet(ids map[string]int64) func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
	return func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
		id, ok := ids[repo]
		if !ok {
			return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
		}
		return &github.Repository{ID: github.Ptr(id), Name: github.Ptr(repo)}, fake.GenerateEmptyResponse(), nil
	}
}

func ghVariable(value, visibility string) *github.ActionsVariable {
	return &github.ActionsVariable{
		Name:       testVariableName,
		Value:      value,
		Visibility: github.Ptr(visibility),
	}
}

// Observe must report ResourceExists=false on 404 so the managed
// reconciler proceeds to Create rather than declaring an error state.
func TestObserve_DoesNotExist_Returns404AsNotExists(t *testing.T) {
	e := newExternalWithActions(&fake.MockActionsClient{
		MockGetOrgVariable: func(_ context.Context, _, _ string) (*github.ActionsVariable, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
		},
	}, nil)

	got, err := e.Observe(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Errorf("ResourceExists = true, want false")
	}
}

// When the GitHub-side value matches the CR's value, the variable
// is up to date. Anchors the equality semantics of Observe.
func TestObserve_UpToDate_ValueAndVisibilityMatch(t *testing.T) {
	e := newExternalWithActions(&fake.MockActionsClient{
		MockGetOrgVariable: func(_ context.Context, _, _ string) (*github.ActionsVariable, *github.Response, error) {
			return ghVariable("bar", "all"), fake.GenerateEmptyResponse(), nil
		},
	}, nil)

	got, err := e.Observe(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
}

// Value drift between CR and GitHub is the most common reason a
// variable goes out of date — Observe must flag it so Update runs.
func TestObserve_ValueDrift_ReportsNotUpToDate(t *testing.T) {
	e := newExternalWithActions(&fake.MockActionsClient{
		MockGetOrgVariable: func(_ context.Context, _, _ string) (*github.ActionsVariable, *github.Response, error) {
			return ghVariable("OTHER", "all"), fake.GenerateEmptyResponse(), nil
		},
	}, nil)

	got, err := e.Observe(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = true on value drift, want false")
	}
}

// Visibility drift is independent of value drift — even with the
// right value, a wrong visibility must trigger an Update.
func TestObserve_VisibilityDrift_ReportsNotUpToDate(t *testing.T) {
	e := newExternalWithActions(&fake.MockActionsClient{
		MockGetOrgVariable: func(_ context.Context, _, _ string) (*github.ActionsVariable, *github.Response, error) {
			return ghVariable("bar", "private"), fake.GenerateEmptyResponse(), nil
		},
	}, nil)

	got, err := e.Observe(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = true on visibility drift, want false")
	}
}

// For visibility=selected, the selected-repo set must match (compared
// in ID-space so name ordering doesn't false-flag drift). When the
// repos line up, Observe is UpToDate.
func TestObserve_Selected_UpToDateWhenRepoIDsMatch(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockGetOrgVariable: func(_ context.Context, _, _ string) (*github.ActionsVariable, *github.Response, error) {
			return ghVariable("bar", "selected"), fake.GenerateEmptyResponse(), nil
		},
		MockListSelectedReposForOrgVariable: func(_ context.Context, _, _ string, _ *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			return &github.SelectedReposList{Repositories: []*github.Repository{
				{ID: github.Ptr(testRepoBID), Name: github.Ptr(testRepoB)},
				{ID: github.Ptr(testRepoAID), Name: github.Ptr(testRepoA)},
			}}, fake.GenerateEmptyResponse(), nil
		},
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID}),
	}
	e := newExternalWithActions(actions, repos)

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB))
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true (sets match in ID-space)")
	}
}

// A selected variable with no repositories must be up to date when
// GitHub also lists none; an empty and a nil ID list are the same set.
func TestObserve_Selected_NoRepos_UpToDate(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockGetOrgVariable: func(_ context.Context, _, _ string) (*github.ActionsVariable, *github.Response, error) {
			return ghVariable("bar", "selected"), fake.GenerateEmptyResponse(), nil
		},
		MockListSelectedReposForOrgVariable: func(_ context.Context, _, _ string, _ *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			return &github.SelectedReposList{}, fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternalWithActions(actions, nil)

	got, err := e.Observe(context.Background(), newCR(withVisibility("selected")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false with no selected repos on either side, want true")
	}
}

// When GH has different selected repos than CR (here: extra repo on
// GH side), Observe reports NotUpToDate so Update can converge.
func TestObserve_Selected_DriftReportsNotUpToDate(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockGetOrgVariable: func(_ context.Context, _, _ string) (*github.ActionsVariable, *github.Response, error) {
			return ghVariable("bar", "selected"), fake.GenerateEmptyResponse(), nil
		},
		MockListSelectedReposForOrgVariable: func(_ context.Context, _, _ string, _ *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			return &github.SelectedReposList{Repositories: []*github.Repository{
				{ID: github.Ptr(testRepoAID)},
				{ID: github.Ptr(testRepoBID)},
			}}, fake.GenerateEmptyResponse(), nil
		},
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID}),
	}
	e := newExternalWithActions(actions, repos)

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA))
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = true on selected-repo drift, want false")
	}
}

// Create with visibility=all sends value + visibility but NO
// SelectedRepositoryIDs — sending IDs when visibility != selected
// is rejected by the GitHub API.
func TestCreate_VisibilityAll_OmitsSelectedRepoIDs(t *testing.T) {
	var captured *github.ActionsCreateOrgVariableRequest
	actions := &fake.MockActionsClient{
		MockCreateOrgVariable: func(_ context.Context, _ string, v github.ActionsCreateOrgVariableRequest) (*github.Response, error) {
			captured = &v
			return fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternalWithActions(actions, nil)

	if _, err := e.Create(context.Background(), newCR(withValue("bar"))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("CreateOrgVariable was not invoked")
	}
	if captured.Value != "bar" {
		t.Errorf("Value = %q, want bar", captured.Value)
	}
	if captured.SelectedRepositoryIDs != nil {
		t.Errorf("SelectedRepositoryIDs = %v, want nil", captured.SelectedRepositoryIDs)
	}
}

// Create with visibility=selected must resolve repo names to IDs and
// pass them in SelectedRepositoryIDs so GitHub knows which repos can
// read the variable on the very first reconcile.
func TestCreate_VisibilitySelected_ResolvesAndSendsRepoIDs(t *testing.T) {
	var captured *github.ActionsCreateOrgVariableRequest
	actions := &fake.MockActionsClient{
		MockCreateOrgVariable: func(_ context.Context, _ string, v github.ActionsCreateOrgVariableRequest) (*github.Response, error) {
			captured = &v
			return fake.GenerateEmptyResponse(), nil
		},
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID}),
	}
	e := newExternalWithActions(actions, repos)

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB))
	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.SelectedRepositoryIDs == nil {
		t.Fatal("SelectedRepositoryIDs is nil, want IDs for selected repos")
	}
	got := captured.SelectedRepositoryIDs
	if diff := cmp.Diff([]int64{testRepoAID, testRepoBID}, got); diff != "" {
		t.Errorf("SelectedRepositoryIDs: -want, +got:\n%s", diff)
	}
}

// Update reuses the same payload-construction path as Create. This
// test pins that visibility transitions away from "selected" don't
// leak a stale repo-ID list (which would re-add associations the
// user just removed).
func TestUpdate_VisibilityChange_DropsRepoIDs(t *testing.T) {
	var captured *github.ActionsUpdateOrgVariableRequest
	actions := &fake.MockActionsClient{
		MockUpdateOrgVariable: func(_ context.Context, _, _ string, v github.ActionsUpdateOrgVariableRequest) (*github.Response, error) {
			captured = &v
			return fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternalWithActions(actions, nil)

	cr := newCR(withVisibility("all"))
	if _, err := e.Update(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.SelectedRepositoryIDs != nil {
		t.Errorf("SelectedRepositoryIDs leaked into visibility=all update: %v", captured.SelectedRepositoryIDs)
	}
}

// Update must send name, value, visibility and the selected repo IDs:
// every field left out of the PATCH keeps its old value on GitHub, so
// the drift Observe reported would never converge.
func TestUpdate_VisibilitySelected_SendsFullPayload(t *testing.T) {
	var captured *github.ActionsUpdateOrgVariableRequest
	actions := &fake.MockActionsClient{
		MockUpdateOrgVariable: func(_ context.Context, _, _ string, v github.ActionsUpdateOrgVariableRequest) (*github.Response, error) {
			captured = &v
			return fake.GenerateEmptyResponse(), nil
		},
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID}),
	}
	e := newExternalWithActions(actions, repos)

	cr := newCR(withValue("new"), withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB))
	if _, err := e.Update(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("UpdateOrgVariable was not invoked")
	}
	want := github.ActionsUpdateOrgVariableRequest{
		Name:                  github.Ptr(testVariableName),
		Value:                 github.Ptr("new"),
		Visibility:            github.Ptr("selected"),
		SelectedRepositoryIDs: []int64{testRepoAID, testRepoBID},
	}
	if diff := cmp.Diff(want, *captured); diff != "" {
		t.Errorf("UpdateOrgVariable request: -want, +got:\n%s", diff)
	}
}

// Delete must swallow 404 so a controller restart that lost track of
// an already-deleted variable doesn't block finalizer removal.
func TestDelete_404IsNotAnError(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockDeleteOrgVariable: func(_ context.Context, _, _ string) (*github.Response, error) {
			return fake.GenerateEmptyResponse(), fake.Generate404Response()
		},
	}
	e := newExternalWithActions(actions, nil)

	if _, err := e.Delete(context.Background(), newCR()); err != nil {
		t.Errorf("Delete returned %v on 404, want nil", err)
	}
}

// newExternalWithActions wires only the client subsystems each test
// needs. Other fake methods stay nil — invoking them would panic,
// which surfaces accidental over-calling by the controller.
func newExternalWithActions(actions *fake.MockActionsClient, repos *fake.MockRepositoriesClient) external {
	c := &ghclient.Services{Actions: actions}
	if repos != nil {
		c.Repositories = repos
	}
	return external{github: &ghclient.Client{Services: c}}
}

// Marker so the linter doesn't complain about an unused import when
// test cases get commented out.
var _ resource.Managed = (*v1alpha1.OrganizationVariable)(nil)

func init() {
	_ = test.EquateErrors
}
