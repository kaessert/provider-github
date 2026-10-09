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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

const (
	testOrg             = "example-org"
	testGroupName       = "shared-runners"
	testGroupID   int64 = 12345

	testRepoA         = "example-repo-a"
	testRepoB         = "example-repo-b"
	testRepoAID int64 = 67890
	testRepoBID int64 = 67891

	testWorkflowA = "example-org/example-repo-a/.github/workflows/build.yml@refs/heads/main"
	testWorkflowB = "example-org/example-repo-b/.github/workflows/deploy.yml@refs/heads/main"
)

type modifier func(*v1alpha1.RunnerGroup)

func withVisibility(v string) modifier {
	return func(cr *v1alpha1.RunnerGroup) { cr.Spec.ForProvider.Visibility = v }
}

func withSelectedRepos(names ...string) modifier {
	return func(cr *v1alpha1.RunnerGroup) {
		cr.Spec.ForProvider.SelectedRepositories = nil
		for _, n := range names {
			cr.Spec.ForProvider.SelectedRepositories = append(cr.Spec.ForProvider.SelectedRepositories,
				v1alpha1.RunnerGroupSelectedRepo{Repo: n})
		}
	}
}

func withWorkflows(wfs ...string) modifier {
	return func(cr *v1alpha1.RunnerGroup) {
		cr.Spec.ForProvider.SelectedWorkflows = nil
		for _, w := range wfs {
			cr.Spec.ForProvider.SelectedWorkflows = append(cr.Spec.ForProvider.SelectedWorkflows, v1alpha1.WorkflowRef(w))
		}
	}
}

func withID(id int64) modifier {
	return func(cr *v1alpha1.RunnerGroup) { cr.Status.AtProvider.ID = id }
}

func newCR(m ...modifier) *v1alpha1.RunnerGroup {
	cr := &v1alpha1.RunnerGroup{}
	cr.Spec.ForProvider.Org = testOrg
	cr.Spec.ForProvider.Visibility = "all"
	meta.SetExternalName(cr, testGroupName)
	for _, f := range m {
		f(cr)
	}
	return cr
}

// ghGroup builds the GitHub-side runner group matching newCR's defaults.
func ghGroup(m ...func(*github.RunnerGroup)) *github.RunnerGroup {
	g := &github.RunnerGroup{
		ID:                       github.Ptr(testGroupID),
		Name:                     github.Ptr(testGroupName),
		Visibility:               github.Ptr("all"),
		AllowsPublicRepositories: github.Ptr(false),
		RestrictedToWorkflows:    github.Ptr(false),
	}
	for _, f := range m {
		f(g)
	}
	return g
}

func listGroups(groups ...*github.RunnerGroup) func(context.Context, string, *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error) {
	return func(_ context.Context, _ string, _ *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error) {
		return &github.RunnerGroups{TotalCount: len(groups), RunnerGroups: groups}, fake.GenerateEmptyResponse(), nil
	}
}

func listRepoAccess(ids ...int64) func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
	return func(_ context.Context, _ string, _ int64, _ *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
		repos := make([]*github.Repository, 0, len(ids))
		for _, id := range ids {
			repos = append(repos, &github.Repository{ID: github.Ptr(id)})
		}
		return &github.ListRepositories{Repositories: repos}, fake.GenerateEmptyResponse(), nil
	}
}

// mockRepoGet backs Repositories.Get so selected-repo name lookups
// resolve to known IDs.
func mockRepoGet(ids map[string]int64) func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
	return func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
		id, ok := ids[repo]
		if !ok {
			return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
		}
		return &github.Repository{ID: github.Ptr(id), Name: github.Ptr(repo)}, fake.GenerateEmptyResponse(), nil
	}
}

func knownRepos() *fake.MockRepositoriesClient {
	return &fake.MockRepositoriesClient{
		MockGet: mockRepoGet(map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID}),
	}
}

// Runner groups are matched by name; a list without that name means
// the group does not exist and must be created.
func TestObserveNameNotListedReportsNotExists(t *testing.T) {
	other := ghGroup(func(g *github.RunnerGroup) { g.Name = github.Ptr("other-runners") })
	e := newExternal(&fake.MockActionsClient{MockListOrganizationRunnerGroups: listGroups(other)}, nil)

	got, err := e.Observe(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Errorf("ResourceExists = true, want false")
	}
}

// Observe must follow NextPage with the page number in the request;
// otherwise groups past the first page are never found and get
// re-created on every reconcile.
func TestObserveFindsGroupOnLaterPage(t *testing.T) {
	var pages []int
	e := newExternal(&fake.MockActionsClient{
		MockListOrganizationRunnerGroups: func(_ context.Context, _ string, opts *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error) {
			pages = append(pages, opts.Page)
			if opts.Page == 0 {
				other := ghGroup(func(g *github.RunnerGroup) { g.Name = github.Ptr("other-runners") })
				return &github.RunnerGroups{RunnerGroups: []*github.RunnerGroup{other}}, &github.Response{NextPage: 2}, nil
			}
			return &github.RunnerGroups{RunnerGroups: []*github.RunnerGroup{ghGroup()}}, fake.GenerateEmptyResponse(), nil
		},
	}, nil)

	got, err := e.Observe(context.Background(), newCR())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists {
		t.Errorf("ResourceExists = false, want true (group is on page 2)")
	}
	if diff := cmp.Diff([]int{0, 2}, pages); diff != "" {
		t.Errorf("requested pages: -want, +got:\n%s", diff)
	}
}

// When every managed field matches, the group is up to date and its ID
// is recorded for Update and Delete. Workflow and repository order on
// GitHub's side must not count as drift.
func TestObserveUpToDateAllFieldsMatch(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockListOrganizationRunnerGroups: listGroups(ghGroup(func(g *github.RunnerGroup) {
			g.Visibility = github.Ptr("selected")
			g.AllowsPublicRepositories = github.Ptr(true)
			g.RestrictedToWorkflows = github.Ptr(true)
			g.SelectedWorkflows = []string{testWorkflowB, testWorkflowA}
		})),
		MockListRepositoryAccessRunnerGroup: listRepoAccess(testRepoBID, testRepoAID),
	}
	e := newExternal(actions, knownRepos())

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB), withWorkflows(testWorkflowA, testWorkflowB))
	cr.Spec.ForProvider.AllowsPublicRepositories = true
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
	if cr.Status.AtProvider.ID != testGroupID {
		t.Errorf("status.atProvider.id = %d, want %d", cr.Status.AtProvider.ID, testGroupID)
	}
}

// Each managed field is compared independently; drift in any one of
// them must trigger an Update.
func TestObserveFieldDriftReportsNotUpToDate(t *testing.T) {
	cases := map[string]struct {
		cr *v1alpha1.RunnerGroup
		gh *github.RunnerGroup
	}{
		"Visibility": {
			cr: newCR(),
			gh: ghGroup(func(g *github.RunnerGroup) { g.Visibility = github.Ptr("private") }),
		},
		"AllowsPublicRepositories": {
			cr: newCR(),
			gh: ghGroup(func(g *github.RunnerGroup) { g.AllowsPublicRepositories = github.Ptr(true) }),
		},
		"SelectedWorkflows": {
			cr: newCR(withWorkflows(testWorkflowA)),
			gh: ghGroup(func(g *github.RunnerGroup) {
				g.RestrictedToWorkflows = github.Ptr(true)
				g.SelectedWorkflows = []string{testWorkflowA, testWorkflowB}
			}),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newExternal(&fake.MockActionsClient{MockListOrganizationRunnerGroups: listGroups(tc.gh)}, nil)

			got, err := e.Observe(context.Background(), tc.cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Errorf("Observe = %+v, want exists and not up to date", got)
			}
		})
	}
}

// For visibility=selected, a repository with access on GitHub that the
// CR does not list is drift, so Update can revoke it.
func TestObserveSelectedRepoAccessDriftReportsNotUpToDate(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockListOrganizationRunnerGroups: listGroups(ghGroup(func(g *github.RunnerGroup) {
			g.Visibility = github.Ptr("selected")
		})),
		MockListRepositoryAccessRunnerGroup: listRepoAccess(testRepoAID, testRepoBID),
	}
	e := newExternal(actions, knownRepos())

	got, err := e.Observe(context.Background(), newCR(withVisibility("selected"), withSelectedRepos(testRepoA)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = true on repository access drift, want false")
	}
}

// When GitHub's repository names match the spec (ignoring case), repo
// access is up to date without any per-repository lookups.
func TestObserveSelectedNamesMatchNoRepoLookups(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockListOrganizationRunnerGroups: listGroups(ghGroup(func(g *github.RunnerGroup) {
			g.Visibility = github.Ptr("selected")
		})),
		MockListRepositoryAccessRunnerGroup: func(_ context.Context, _ string, _ int64, _ *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
			return &github.ListRepositories{Repositories: []*github.Repository{
				{ID: github.Ptr(testRepoBID), Name: github.Ptr(testRepoB)},
				{ID: github.Ptr(testRepoAID), Name: github.Ptr("Example-Repo-A")},
			}}, fake.GenerateEmptyResponse(), nil
		},
	}
	repos := &fake.MockRepositoriesClient{
		MockGet: func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
			t.Errorf("Repositories.Get(%q) called, want no lookups when names match", repo)
			return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
		},
	}
	e := newExternal(actions, repos)

	got, err := e.Observe(context.Background(), newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true (names match ignoring case)")
	}
}

// A spec that still uses a renamed repository's old name resolves to the
// same ID GitHub lists under the new name, so it is not drift.
func TestObserveSelectedRenamedRepoUpToDateByID(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockListOrganizationRunnerGroups: listGroups(ghGroup(func(g *github.RunnerGroup) {
			g.Visibility = github.Ptr("selected")
		})),
		MockListRepositoryAccessRunnerGroup: func(_ context.Context, _ string, _ int64, _ *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
			return &github.ListRepositories{Repositories: []*github.Repository{
				{ID: github.Ptr(testRepoAID), Name: github.Ptr("example-repo-a-renamed")},
			}}, fake.GenerateEmptyResponse(), nil
		},
	}
	var lookups []string
	repos := &fake.MockRepositoriesClient{
		MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
			lookups = append(lookups, repo)
			return mockRepoGet(map[string]int64{testRepoA: testRepoAID})(ctx, owner, repo)
		},
	}
	e := newExternal(actions, repos)

	got, err := e.Observe(context.Background(), newCR(withVisibility("selected"), withSelectedRepos(testRepoA)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true (old name resolves to the same ID)")
	}
	if diff := cmp.Diff([]string{testRepoA}, lookups); diff != "" {
		t.Errorf("Repositories.Get lookups: -want, +got:\n%s", diff)
	}
}

// Create must send repository IDs and workflow restrictions in the
// create request, so the group is correct from its first reconcile,
// and record the returned ID.
func TestCreateSendsRepoIDsAndWorkflows(t *testing.T) {
	var captured github.CreateRunnerGroupRequest
	actions := &fake.MockActionsClient{
		MockCreateOrganizationRunnerGroup: func(_ context.Context, _ string, req github.CreateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error) {
			captured = req
			return ghGroup(), fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(actions, knownRepos())

	cr := newCR(withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB), withWorkflows(testWorkflowA))
	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := github.CreateRunnerGroupRequest{
		Name:                     github.Ptr(testGroupName),
		Visibility:               github.Ptr("selected"),
		SelectedRepositoryIDs:    []int64{testRepoAID, testRepoBID},
		AllowsPublicRepositories: github.Ptr(false),
		RestrictedToWorkflows:    github.Ptr(true),
		SelectedWorkflows:        []string{testWorkflowA},
	}
	if diff := cmp.Diff(want, captured); diff != "" {
		t.Errorf("CreateRunnerGroupRequest: -want, +got:\n%s", diff)
	}
	if cr.Status.AtProvider.ID != testGroupID {
		t.Errorf("status.atProvider.id = %d, want %d", cr.Status.AtProvider.ID, testGroupID)
	}
}

// The update endpoint cannot change repository access, so for
// visibility=selected Update must also replace the access list.
func TestUpdateSelectedUpdatesGroupAndSetsRepoAccess(t *testing.T) {
	var updated github.UpdateRunnerGroupRequest
	var setIDs []int64
	var setGroupID int64
	actions := &fake.MockActionsClient{
		MockUpdateOrganizationRunnerGroup: func(_ context.Context, _ string, _ int64, req github.UpdateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error) {
			updated = req
			return ghGroup(), fake.GenerateEmptyResponse(), nil
		},
		MockSetRepositoryAccessRunnerGroup: func(_ context.Context, _ string, groupID int64, req github.SetRepoAccessRunnerGroupRequest) (*github.Response, error) {
			setGroupID = groupID
			setIDs = req.SelectedRepositoryIDs
			return fake.GenerateEmptyResponse(), nil
		},
	}
	e := newExternal(actions, knownRepos())

	cr := newCR(withID(testGroupID), withVisibility("selected"), withSelectedRepos(testRepoA, testRepoB), withWorkflows(testWorkflowA))
	if _, err := e.Update(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.GetVisibility() != "selected" || !updated.GetRestrictedToWorkflows() {
		t.Errorf("UpdateRunnerGroupRequest = %+v, want visibility selected and restricted", updated)
	}
	if setGroupID != testGroupID {
		t.Errorf("SetRepositoryAccessRunnerGroup group ID = %d, want %d", setGroupID, testGroupID)
	}
	if diff := cmp.Diff([]int64{testRepoAID, testRepoBID}, setIDs); diff != "" {
		t.Errorf("SelectedRepositoryIDs: -want, +got:\n%s", diff)
	}
}

// Delete must swallow 404 so an already-deleted group doesn't block
// finalizer removal.
func TestDelete404IsNotAnError(t *testing.T) {
	actions := &fake.MockActionsClient{
		MockDeleteOrganizationRunnerGroup: func(_ context.Context, _ string, _ int64) (*github.Response, error) {
			return fake.GenerateEmptyResponse(), fake.Generate404Response()
		},
	}
	e := newExternal(actions, nil)

	if _, err := e.Delete(context.Background(), newCR(withID(testGroupID))); err != nil {
		t.Errorf("Delete returned %v on 404, want nil", err)
	}
}

// newExternal wires only the client subsystems each test needs. Other
// fake methods stay nil, so an unexpected call panics.
func newExternal(actions *fake.MockActionsClient, repos *fake.MockRepositoriesClient) external {
	c := &ghclient.Services{Actions: actions}
	if repos != nil {
		c.Repositories = repos
	}
	return external{github: &ghclient.Client{Services: c}}
}
