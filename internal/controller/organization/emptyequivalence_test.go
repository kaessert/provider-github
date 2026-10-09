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

	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// A declared empty list and no entries on GitHub both mean "none", so they are
// not drift. A real difference still is.
func TestObserveEmptyEqualsAbsent(t *testing.T) {
	noRepos := func(*clusterv1alpha1.Organization) {}
	cases := map[string]struct {
		reason string
		spec   func(*clusterv1alpha1.Organization)
		// enabled is what GitHub lists as enabled for Actions.
		enabled []*github.Repository
		want    bool
	}{
		"EnabledReposDeclaredEmptyGitHubAbsent": {
			reason:  "actions.enabledRepos: [] and no enabled repositories on GitHub is in sync",
			spec:    noRepos,
			enabled: nil,
			want:    true,
		},
		"EnabledReposDeclaredEmptyGitHubEmpty": {
			reason:  "actions.enabledRepos: [] and an empty list on GitHub is in sync",
			spec:    noRepos,
			enabled: []*github.Repository{},
			want:    true,
		},
		"EnabledReposDeclaredRepoGitHubAbsent": {
			reason: "a declared enabled repository GitHub lacks is drift",
			spec: func(cr *clusterv1alpha1.Organization) {
				cr.Spec.ForProvider.Actions.EnabledRepos = []clusterv1alpha1.ActionEnabledRepo{{Repo: repo}}
			},
			enabled: nil,
			want:    false,
		},
		"EnabledReposDeclaredEmptyGitHubHasRepo": {
			reason:  "an enabled repository on GitHub that the spec leaves out is drift",
			spec:    noRepos,
			enabled: []*github.Repository{{Name: &repo}},
			want:    false,
		},
	}

	for name, tc := range cases {
		observe := func(t *testing.T, scope string) {
			cr := &clusterv1alpha1.Organization{}
			cr.Spec.ForProvider.Description = description
			cr.Spec.ForProvider.Actions = clusterv1alpha1.ActionsConfiguration{EnabledRepos: []clusterv1alpha1.ActionEnabledRepo{}}
			// Secrets declared without a repository access list, on secrets GitHub shares with every repository.
			cr.Spec.ForProvider.Secrets = &clusterv1alpha1.SecretConfiguration{
				ActionsSecrets:    []clusterv1alpha1.OrgSecret{{Name: orgSecret1}},
				DependabotSecrets: []clusterv1alpha1.OrgSecret{{Name: orgSecret1}},
			}
			tc.spec(cr)
			meta.SetExternalName(cr, org)

			allSecret := func(_ context.Context, _, _ string) (*github.Secret, *github.Response, error) {
				return &github.Secret{Name: orgSecret1, Visibility: "all"}, fake.GenerateEmptyResponse(), nil
			}
			c := &external{github: &ghclient.Client{Services: &ghclient.Services{
				Organizations: &fake.MockOrganizationsClient{
					MockGet: func(context.Context, string) (*github.Organization, *github.Response, error) {
						return githubOrganization(), nil, nil
					},
				},
				Actions: &fake.MockActionsClient{
					MockListEnabledReposInOrg: func(context.Context, string, *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
						return &github.ActionsEnabledOnOrgRepos{Repositories: tc.enabled}, fake.GenerateEmptyResponse(), nil
					},
					MockGetOrgSecret: allSecret,
				},
				Dependabot: &fake.MockDependabotClient{MockGetOrgSecret: allSecret},
			}}}

			var (
				mg  resource.Managed = cr
				got managed.ExternalObservation
				err error
			)
			if scope == "Namespaced" {
				ns := ddtest.Convert(t, cr, &namespacedv1alpha1.Organization{})
				mg = ns
				got, err = scopebridge.New[*namespacedv1alpha1.Organization](c,
					func() *clusterv1alpha1.Organization { return &clusterv1alpha1.Organization{} }).Observe(context.Background(), ns)
			} else {
				got, err = c.Observe(context.Background(), cr)
			}
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.want {
				t.Errorf("%s\nResourceUpToDate = %v, want %v", tc.reason, got.ResourceUpToDate, tc.want)
			}
			if ready := mg.GetCondition(xpv2.TypeReady).Status == corev1.ConditionTrue; ready != tc.want {
				t.Errorf("%s\nReady = %v, want %v", tc.reason, ready, tc.want)
			}
		}
		t.Run("Cluster/"+name, func(t *testing.T) { observe(t, "Cluster") })
		t.Run("Namespaced/"+name, func(t *testing.T) { observe(t, "Namespaced") })
	}
}
