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
	"encoding/json"
	"testing"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
)

// inSyncClient serves an organization whose description, enabled repositories
// and secrets all match organization([]string{repo, repo2}).
func inSyncClient() *ghclient.Client {
	return &ghclient.Client{Services: &ghclient.Services{
		Organizations: &fake.MockOrganizationsClient{
			MockGet: func(context.Context, string) (*github.Organization, *github.Response, error) {
				return githubOrganization(), nil, nil
			},
		},
		Actions: &fake.MockActionsClient{
			MockListEnabledReposInOrg: func(context.Context, string, *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
				return githubOrgRepoActions(), fake.GenerateEmptyResponse(), nil
			},
			MockGetOrgSecret: func(context.Context, string, string) (*github.Secret, *github.Response, error) {
				return githubOrgSecret(), fake.GenerateEmptyResponse(), nil
			},
			MockListSelectedReposForOrgSecret: func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
				return githubSelectedReposForOrgSecret(), fake.GenerateEmptyResponse(), nil
			},
		},
		Dependabot: &fake.MockDependabotClient{
			MockGetOrgSecret: func(context.Context, string, string) (*github.Secret, *github.Response, error) {
				return githubOrgSecret(), fake.GenerateEmptyResponse(), nil
			},
			MockListSelectedReposForOrgSecret: func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
				return githubSelectedReposForOrgSecret(), fake.GenerateEmptyResponse(), nil
			},
		},
		Repositories: &fake.MockRepositoriesClient{
			MockGet: func(_ context.Context, _, name string) (*github.Repository, *github.Response, error) {
				if name == orgSecretRepo1 {
					return githubOrgSecretRepo(), fake.GenerateEmptyResponse(), nil
				}
				return nil, fake.GenerateEmptyResponse(), nil
			},
		},
	}}
}

func driftClients() map[string]struct {
	client *ghclient.Client
	cr     *v1alpha1.Organization
} {
	type tc = struct {
		client *ghclient.Client
		cr     *v1alpha1.Organization
	}
	enabledRepos := inSyncClient()
	enabledRepos.Actions.(*fake.MockActionsClient).MockListEnabledReposInOrg = func(context.Context, string, *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
		return &github.ActionsEnabledOnOrgRepos{Repositories: []*github.Repository{{Name: &repo}}}, fake.GenerateEmptyResponse(), nil
	}
	actionsSecrets := inSyncClient()
	actionsSecrets.Actions.(*fake.MockActionsClient).MockGetOrgSecret = func(context.Context, string, string) (*github.Secret, *github.Response, error) {
		return nil, fake.GenerateEmptyResponse(), nil
	}
	actionsSecrets.Actions.(*fake.MockActionsClient).MockListSelectedReposForOrgSecret = func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
		return nil, fake.GenerateEmptyResponse(), nil
	}
	dependabotSecrets := inSyncClient()
	dependabotSecrets.Dependabot.(*fake.MockDependabotClient).MockGetOrgSecret = func(context.Context, string, string) (*github.Secret, *github.Response, error) {
		return nil, fake.GenerateEmptyResponse(), nil
	}
	dependabotSecrets.Dependabot.(*fake.MockDependabotClient).MockListSelectedReposForOrgSecret = func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
		return nil, fake.GenerateEmptyResponse(), nil
	}
	return map[string]tc{
		"EnabledRepos":      {enabledRepos, organization([]string{repo, repo2})},
		"ActionsSecrets":    {actionsSecrets, organization([]string{repo, repo2})},
		"DependabotSecrets": {dependabotSecrets, organization([]string{repo, repo2})},
		"Description":       {inSyncClient(), organization([]string{repo, repo2}, withDescription())},
	}
}

// roundTrip copies the declared parameters of the cluster-scoped form onto
// its namespaced twin, which serialise identically.
func roundTrip(from *v1alpha1.Organization, to *namespacedv1alpha1.Organization) error {
	data, err := json.Marshal(from.Spec.ForProvider)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &to.Spec.ForProvider)
}

func assertUnavailable(t *testing.T, c xpv2.Condition) {
	t.Helper()
	if c.Status != "False" || c.Reason != xpv2.ReasonUnavailable {
		t.Errorf("Ready = %s/%s, want False/%s", c.Status, c.Reason, xpv2.ReasonUnavailable)
	}
}

// Every drift path reports an existing, out-of-date organization with Ready
// False, so Ready does not stay True (or unset) while the drift is corrected.
func TestObserveDriftSetsUnavailable(t *testing.T) {
	for name, tc := range driftClients() {
		t.Run(name, func(t *testing.T) {
			// A previous poll left the resource Available.
			tc.cr.SetConditions(xpv2.Available())
			got, err := (&external{github: tc.client}).Observe(context.Background(), tc.cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Errorf("Observe = %+v, want exists and not up to date", got)
			}
			assertUnavailable(t, tc.cr.GetCondition(xpv2.TypeReady))
		})
	}
}

// The up-to-date path still reports Available.
func TestObserveInSyncSetsAvailable(t *testing.T) {
	cr := organization([]string{repo, repo2})
	cr.SetConditions(xpv2.Unavailable())
	got, err := (&external{github: inSyncClient()}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || !got.ResourceUpToDate {
		t.Fatalf("Observe = %+v, want exists and up to date", got)
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Status != "True" || c.Reason != xpv2.ReasonAvailable {
		t.Errorf("Ready = %s/%s, want True/%s", c.Status, c.Reason, xpv2.ReasonAvailable)
	}
}

// The namespaced kind is served by the same client through the scope bridge;
// the condition reaches the namespaced resource.
func TestObserveDriftSetsUnavailableNamespaced(t *testing.T) {
	for name, tc := range driftClients() {
		t.Run(name, func(t *testing.T) {
			ns := &namespacedv1alpha1.Organization{}
			ns.Namespace = "default"
			if err := roundTrip(tc.cr, ns); err != nil {
				t.Fatal(err)
			}
			meta.SetExternalName(ns, org)
			ns.SetConditions(xpv2.Available())

			bridged := scopebridge.New[*namespacedv1alpha1.Organization](
				&external{github: tc.client},
				func() *v1alpha1.Organization { return &v1alpha1.Organization{} },
			)
			got, err := bridged.Observe(context.Background(), ns)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Errorf("Observe = %+v, want exists and not up to date", got)
			}
			assertUnavailable(t, ns.GetCondition(xpv2.TypeReady))
		})
	}
}
