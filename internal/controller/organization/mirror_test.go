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

package organization

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func secretClient(repos ...string) (func(context.Context, string, string) (*github.Secret, *github.Response, error), func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error)) {
	get := func(context.Context, string, string) (*github.Secret, *github.Response, error) {
		return githubOrgSecret(), fake.GenerateEmptyResponse(), nil
	}
	list := func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
		l := &github.SelectedReposList{}
		for i, r := range repos {
			l.Repositories = append(l.Repositories, &github.Repository{Name: github.Ptr(r), ID: github.Ptr(int64(i + 1))})
		}
		return l, fake.GenerateEmptyResponse(), nil
	}
	return get, list
}

// Observe mirrors the description GitHub reports, the repositories enabled for
// Actions and the repository access of the declared secrets into
// status.atProvider.
func TestObserveMirrorsOrganization(t *testing.T) {
	getSecret, listRepos := secretClient("zeta", "alpha")
	e := &external{github: &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGet: func(context.Context, string) (*github.Organization, *github.Response, error) {
				return githubOrganization(), nil, nil
			},
		},
		Actions: &fake.MockActionsClient{
			MockListEnabledReposInOrg: func(context.Context, string, *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
				return githubOrgRepoActions(), fake.GenerateEmptyResponse(), nil
			},
			MockGetOrgSecret:                  getSecret,
			MockListSelectedReposForOrgSecret: listRepos,
		},
		Dependabot: &fake.MockDependabotClient{
			MockGetOrgSecret:                  getSecret,
			MockListSelectedReposForOrgSecret: listRepos,
		},
		Repositories: &fake.MockRepositoriesClient{
			MockGet: func(_ context.Context, _, name string) (*github.Repository, *github.Response, error) {
				return &github.Repository{Name: github.Ptr(name), ID: github.Ptr(map[string]int64{"zeta": 1, "alpha": 2}[name])}, fake.GenerateEmptyResponse(), nil
			},
		},
	}}}

	cr := organization([]string{repo, repo2})
	accessList := []v1alpha1.SecretSelectedRepo{{Repo: "alpha"}, {Repo: "zeta"}}
	cr.Spec.ForProvider.Secrets.ActionsSecrets[0].RepositoryAccessList = accessList
	cr.Spec.ForProvider.Secrets.DependabotSecrets[0].RepositoryAccessList = accessList
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	access := []v1alpha1.SecretSelectedRepoObservation{{Repo: "alpha"}, {Repo: "zeta"}}
	want := v1alpha1.OrganizationObservation{
		ID:          org,
		Description: description,
		Actions: v1alpha1.ActionsConfigurationObservation{
			EnabledRepos: []v1alpha1.ActionEnabledRepoObservation{{Repo: repo}, {Repo: repo2}},
		},
		Secrets: &v1alpha1.SecretConfigurationObservation{
			ActionsSecrets:    []v1alpha1.OrgSecretObservation{{Name: orgSecret1, RepositoryAccessList: access}},
			DependabotSecrets: []v1alpha1.OrgSecretObservation{{Name: orgSecret1, RepositoryAccessList: access}},
		},
	}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}

// GitHub is queried for the enabled repositories and the secrets only while
// the spec names them, so a mirror of either is dropped when it stops doing so.
func TestObserveMirrorDropsUnqueriedState(t *testing.T) {
	cr := descriptionOnlyCR(description)
	cr.Status.AtProvider.Actions.EnabledRepos = []v1alpha1.ActionEnabledRepoObservation{{Repo: repo}}
	cr.Status.AtProvider.Secrets = &v1alpha1.SecretConfigurationObservation{}

	if _, err := descriptionOnlyExternal(description).Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := v1alpha1.OrganizationObservation{ID: org, Description: description}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}
