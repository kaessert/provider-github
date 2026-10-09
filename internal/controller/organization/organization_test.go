/*
Copyright 2022 The Crossplane Authors.

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

package organization

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/google/go-github/v90/github"
)

var (
	org              = "test-org"
	description      = "test description"
	otherDescription = "other description"
	repo             = "test-repo"
	repo2            = "test-repo2"
	orgSecret1       = "org-secret1"
	orgSecretRepo1   = "org-secret-repo1"
	orgSecretRepo1ID = 123456
)

type organizationModifier func(*v1alpha1.Organization)

func withDescription() organizationModifier {
	return func(r *v1alpha1.Organization) {
		r.Spec.ForProvider.Description = otherDescription
	}
}

func organization(repos []string, m ...organizationModifier) *v1alpha1.Organization {
	cr := &v1alpha1.Organization{}

	cr.Spec.ForProvider.Description = description
	cr.Spec.ForProvider.Actions = v1alpha1.ActionsConfiguration{
		EnabledRepos: make([]v1alpha1.ActionEnabledRepo, len(repos)),
	}
	for i, repo := range repos {
		cr.Spec.ForProvider.Actions.EnabledRepos[i] = v1alpha1.ActionEnabledRepo{
			Repo: repo,
		}
	}

	cr.Spec.ForProvider.Secrets = &v1alpha1.SecretConfiguration{
		ActionsSecrets: []v1alpha1.OrgSecret{
			{
				Name: orgSecret1,
				RepositoryAccessList: []v1alpha1.SecretSelectedRepo{
					{Repo: orgSecretRepo1},
				},
			},
		},
		DependabotSecrets: []v1alpha1.OrgSecret{
			{
				Name: orgSecret1,
				RepositoryAccessList: []v1alpha1.SecretSelectedRepo{
					{Repo: orgSecretRepo1},
				},
			},
		},
	}

	meta.SetExternalName(cr, org)

	for _, f := range m {
		f(cr)
	}
	return cr
}

func githubOrganization() *github.Organization {
	return &github.Organization{
		Description: &description,
		Name:        &org,
	}
}

func githubOrgRepoActions() *github.ActionsEnabledOnOrgRepos {
	repos := []*github.Repository{
		{Name: &repo},
		{Name: &repo2},
	}
	return &github.ActionsEnabledOnOrgRepos{Repositories: repos}
}

func githubOrgSecret() *github.Secret {
	return &github.Secret{
		Name:       orgSecret1,
		Visibility: "selected",
	}
}

func githubSelectedReposForOrgSecret() *github.SelectedReposList {
	id := int64(orgSecretRepo1ID)
	return &github.SelectedReposList{
		Repositories: []*github.Repository{
			{
				Name: &orgSecretRepo1,
				ID:   &id,
			},
		},
	}
}

func githubOrgSecretRepo() *github.Repository {
	id := int64(orgSecretRepo1ID)
	return &github.Repository{
		Name: &orgSecretRepo1,
		ID:   &id,
	}
}

func TestObserve(t *testing.T) {
	type fields struct {
		github *ghclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"NotUpToDate": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Organizations: &fake.MockOrganizationsClient{
							MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
								return githubOrganization(), nil, nil
							},
						},
						Actions: &fake.MockActionsClient{
							MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
								return githubOrgRepoActions(), fake.GenerateEmptyResponse(), nil
							},
							MockGetOrgSecret: func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListSelectedReposForOrgSecret: func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Dependabot: &fake.MockDependabotClient{
							MockGetOrgSecret: func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
							MockListSelectedReposForOrgSecret: func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
						Repositories: &fake.MockRepositoriesClient{
							MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg:  organization([]string{repo, repo2}, withDescription()),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		"UpToDate": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Organizations: &fake.MockOrganizationsClient{
							MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
								return githubOrganization(), nil, nil
							},
						},
						Actions: &fake.MockActionsClient{
							MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
								return githubOrgRepoActions(), fake.GenerateEmptyResponse(), nil
							},
							MockGetOrgSecret: func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
								return githubOrgSecret(), fake.GenerateEmptyResponse(), nil
							},
							MockListSelectedReposForOrgSecret: func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
								return githubSelectedReposForOrgSecret(), fake.GenerateEmptyResponse(), nil
							},
						},
						Dependabot: &fake.MockDependabotClient{
							MockGetOrgSecret: func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
								return githubOrgSecret(), fake.GenerateEmptyResponse(), nil
							},
							MockListSelectedReposForOrgSecret: func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
								return githubSelectedReposForOrgSecret(), fake.GenerateEmptyResponse(), nil
							},
						},
						Repositories: &fake.MockRepositoriesClient{
							MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
								if repo == orgSecretRepo1 {
									return githubOrgSecretRepo(), fake.GenerateEmptyResponse(), nil
								}
								return nil, fake.GenerateEmptyResponse(), nil
							},
						},
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg:  organization([]string{repo, repo2}),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"DoesNotExists": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Organizations: &fake.MockOrganizationsClient{
							MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
						},
						Actions: &fake.MockActionsClient{
							MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
							MockGetOrgSecret: func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
							MockListSelectedReposForOrgSecret: func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
						},
						Dependabot: &fake.MockDependabotClient{
							MockGetOrgSecret: func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
							MockListSelectedReposForOrgSecret: func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
						},
						Repositories: &fake.MockRepositoriesClient{
							MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
						},
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg:  organization([]string{repo, repo2}),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   false,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{github: tc.fields.github}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

// listEnabledReposInOrg must iterate beyond the first page when GitHub
// returns NextPage > 0. Without pagination, only page 1 is visible to
// the diff against the CR's enabled-repos list, every CR-declared repo
// past page 1 looks "missing", and the controller burns calls
// re-Adding repos that are already enabled.
func TestListEnabledReposInOrg_Paginates(t *testing.T) {
	pages := [][]*github.Repository{
		{{Name: github.Ptr("a")}, {Name: github.Ptr("b")}},
		{{Name: github.Ptr("c")}},
	}
	calls := 0
	gh := &ghclient.Client{
		Services: &ghclient.Services{
			Actions: &fake.MockActionsClient{
				MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
					i := calls
					calls++
					resp := &github.ActionsEnabledOnOrgRepos{Repositories: pages[i]}
					httpResp := fake.GenerateEmptyResponse()
					if i < len(pages)-1 {
						httpResp.NextPage = i + 2
					}
					return resp, httpResp, nil
				},
			},
		},
	}

	got, err := listEnabledReposInOrg(context.Background(), gh, "test-org")
	if err != nil {
		t.Fatalf("listEnabledReposInOrg: %v", err)
	}
	if calls != len(pages) {
		t.Errorf("upstream called %d times, want %d (loop exited early — pagination broken)", calls, len(pages))
	}
	if len(got) != 3 {
		t.Fatalf("returned %d repos, want 3 (page contents not concatenated)", len(got))
	}
	names := []string{*got[0].Name, *got[1].Name, *got[2].Name}
	want := []string{"a", "b", "c"}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("names mismatch (-want +got):\n%s", diff)
	}
}

// setEnabledReposForActions must skip the PUT when the org's current
// enabled-repos list already matches the CR. This protects against
// wasteful idempotent writes when Observe flagged the CR as out-of-date
// for some other reason (e.g. description or secrets drifted).
func TestSetEnabledReposForActions_SkipsWhenAlreadyMatching(t *testing.T) {
	setCalled := false
	gh := &ghclient.Client{
		Services: &ghclient.Services{
			Actions: &fake.MockActionsClient{
				MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
					return &github.ActionsEnabledOnOrgRepos{
						Repositories: []*github.Repository{{Name: github.Ptr("r1")}, {Name: github.Ptr("r2")}},
					}, fake.GenerateEmptyResponse(), nil
				},
				MockSetEnabledReposInOrg: func(ctx context.Context, owner string, ids []int64) (*github.Response, error) {
					setCalled = true
					return fake.GenerateEmptyResponse(), nil
				},
			},
		},
	}
	cr := organization([]string{"r1", "r2"})
	if err := setEnabledReposForActions(context.Background(), gh, org, cr); err != nil {
		t.Fatalf("setEnabledReposForActions: %v", err)
	}
	if setCalled {
		t.Error("SetEnabledReposInOrg was called even though current state matched CR — wastes one API call per Update when the drift is elsewhere")
	}
}

// setEnabledReposForActions must call SetEnabledReposInOrg exactly once
// with the IDs of all repos in the CR spec, and it must seed the
// repo-name→ID cache from the list response so it only fetches IDs for
// newly-added repos. Without that optimization, every Update on a large
// org would re-fetch every repo's ID — O(N) avoidable Get calls.
func TestSetEnabledReposForActions_CallsSetWithResolvedIDs(t *testing.T) {
	idR1, idR2 := int64(11), int64(22)
	var setIDs []int64
	setCalls := 0
	var lookedUp []string
	gh := &ghclient.Client{
		Services: &ghclient.Services{
			Actions: &fake.MockActionsClient{
				MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
					// Org currently has only r1 (with ID, as real GitHub returns); CR wants r1+r2.
					return &github.ActionsEnabledOnOrgRepos{
						Repositories: []*github.Repository{{Name: github.Ptr("r1"), ID: &idR1}},
					}, fake.GenerateEmptyResponse(), nil
				},
				MockSetEnabledReposInOrg: func(ctx context.Context, owner string, ids []int64) (*github.Response, error) {
					setCalls++
					setIDs = append([]int64(nil), ids...)
					return fake.GenerateEmptyResponse(), nil
				},
			},
			Repositories: &fake.MockRepositoriesClient{
				MockGet: func(ctx context.Context, owner, repoName string) (*github.Repository, *github.Response, error) {
					lookedUp = append(lookedUp, repoName)
					if repoName == "r2" {
						return &github.Repository{ID: &idR2, Name: github.Ptr("r2")}, fake.GenerateEmptyResponse(), nil
					}
					t.Fatalf("unexpected repo lookup: %s (cache seeding should have made this unnecessary)", repoName)
					return nil, nil, nil
				},
			},
		},
	}
	cr := organization([]string{"r1", "r2"})
	if err := setEnabledReposForActions(context.Background(), gh, org, cr); err != nil {
		t.Fatalf("setEnabledReposForActions: %v", err)
	}
	if setCalls != 1 {
		t.Errorf("SetEnabledReposInOrg called %d times, want 1 (single bulk replace is the point of Option 1)", setCalls)
	}
	want := []int64{idR1, idR2}
	if diff := cmp.Diff(want, setIDs); diff != "" {
		t.Errorf("Set IDs mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"r2"}, lookedUp); diff != "" {
		t.Errorf("Repositories.Get lookups mismatch (-want +got):\n%s\nCache seeding from listEnabledReposInOrg result must skip already-enabled repos.", diff)
	}
}

// setEnabledReposForActions must surface a pagination error from
// listEnabledReposInOrg without calling Set. A silent partial-page
// result would leak into the diff and could trigger a wrong Set call
// against a truncated view of the enabled-repos list.
func TestSetEnabledReposForActions_PaginationErrorMidWalk(t *testing.T) {
	listCalls := 0
	setCalled := false
	gh := &ghclient.Client{
		Services: &ghclient.Services{
			Actions: &fake.MockActionsClient{
				MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
					listCalls++
					if listCalls == 1 {
						// Page 1: return some repos and signal there's a next page.
						r := fake.GenerateEmptyResponse()
						r.NextPage = 2
						return &github.ActionsEnabledOnOrgRepos{Repositories: []*github.Repository{{Name: github.Ptr("r1")}}}, r, nil
					}
					// Page 2: simulate an API error mid-walk.
					return nil, fake.GenerateEmptyResponse(), errors.New("github: 503 service unavailable")
				},
				MockSetEnabledReposInOrg: func(ctx context.Context, owner string, ids []int64) (*github.Response, error) {
					setCalled = true
					return fake.GenerateEmptyResponse(), nil
				},
			},
		},
	}
	cr := organization([]string{"r1", "r2"})
	err := setEnabledReposForActions(context.Background(), gh, org, cr)
	if err == nil {
		t.Fatal("expected pagination error to surface; silent partial pages would let the helper Set a wrong list")
	}
	if setCalled {
		t.Error("SetEnabledReposInOrg was called despite a partial-page error — the desired-state PUT must not fire on incomplete diff input")
	}
}

// setEnabledReposForActions must surface a Set error verbatim.
// Update() relies on the helper's error to decide whether to mark the
// CR Synced=False, so swallowing a Set failure would silently mislabel
// state as in-sync.
func TestSetEnabledReposForActions_SetErrorPropagates(t *testing.T) {
	wantErr := errors.New("github: 422 invalid repository id")
	gh := &ghclient.Client{
		Services: &ghclient.Services{
			Actions: &fake.MockActionsClient{
				// CR wants only r1; GH has r1+r2 enabled. Names differ, so
				// Set fires (with r1's resolved ID from the seeded cache).
				MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
					return &github.ActionsEnabledOnOrgRepos{
						Repositories: []*github.Repository{
							{Name: github.Ptr("r1"), ID: github.Ptr(int64(11))},
							{Name: github.Ptr("r2"), ID: github.Ptr(int64(22))},
						},
					}, fake.GenerateEmptyResponse(), nil
				},
				MockSetEnabledReposInOrg: func(ctx context.Context, owner string, ids []int64) (*github.Response, error) {
					return fake.GenerateEmptyResponse(), wantErr
				},
			},
		},
	}
	cr := organization([]string{"r1"})
	err := setEnabledReposForActions(context.Background(), gh, org, cr)
	if err == nil || !errorContains(err, "422") {
		t.Fatalf("expected Set error to bubble up unchanged, got: %v", err)
	}
}

// setEnabledReposForActions must surface an error from the per-repo
// Repositories.Get fallback (cache miss path). A swallow here would
// produce zero-value IDs in the Set call, which GitHub rejects — but
// would manifest as an unrelated 422, not the actual root cause.
func TestSetEnabledReposForActions_GetErrorPropagates(t *testing.T) {
	wantErr := errors.New("github: 404 not found")
	setCalled := false
	gh := &ghclient.Client{
		Services: &ghclient.Services{
			Actions: &fake.MockActionsClient{
				MockListEnabledReposInOrg: func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
					return &github.ActionsEnabledOnOrgRepos{
						Repositories: []*github.Repository{{Name: github.Ptr("r1"), ID: github.Ptr(int64(11))}},
					}, fake.GenerateEmptyResponse(), nil
				},
				MockSetEnabledReposInOrg: func(ctx context.Context, owner string, ids []int64) (*github.Response, error) {
					setCalled = true
					return fake.GenerateEmptyResponse(), nil
				},
			},
			Repositories: &fake.MockRepositoriesClient{
				MockGet: func(ctx context.Context, owner, repoName string) (*github.Repository, *github.Response, error) {
					return nil, fake.GenerateEmptyResponse(), wantErr
				},
			},
		},
	}
	cr := organization([]string{"r1", "r2"}) // r2 is a cache miss → triggers Get → returns error
	err := setEnabledReposForActions(context.Background(), gh, org, cr)
	if err == nil || !errorContains(err, "404") {
		t.Fatalf("expected Get error to surface, got: %v", err)
	}
	if setCalled {
		t.Error("SetEnabledReposInOrg was called despite ID-resolution failure — that would have produced a malformed PUT and a confusing GitHub-side error")
	}
}

func errorContains(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}
