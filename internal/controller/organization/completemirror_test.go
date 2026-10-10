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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
)

// A section that differs from the spec does not stop the sections after it
// from being read and mirrored. The repositories each secret gives access to
// are what GitHub holds, the Ready message names the first section that
// differs, and the organization is still reported out of date.
func TestObserveMirrorsLaterSectionsWhenAnEarlierOneDrifts(t *testing.T) {
	access := []v1alpha1.SecretSelectedRepoObservation{{Repo: orgSecretRepo1}}
	inSync := []v1alpha1.OrgSecretObservation{{Name: orgSecret1, RepositoryAccessList: access}}
	noAccess := []v1alpha1.OrgSecretObservation{{Name: orgSecret1, RepositoryAccessList: []v1alpha1.SecretSelectedRepoObservation{}}}

	// Both of the first two differ: the secrets are read after the enabled repositories.
	both := driftClients()["EnabledRepos"].client
	both.Actions.(*fake.MockActionsClient).MockGetOrgSecret = func(context.Context, string, string) (*github.Secret, *github.Response, error) {
		return nil, fake.GenerateEmptyResponse(), nil
	}

	cases := map[string]struct {
		client      *ghclient.Client
		cr          *v1alpha1.Organization
		wantMessage string
		wantActions []v1alpha1.OrgSecretObservation
		wantDepend  []v1alpha1.OrgSecretObservation
	}{
		"EnabledRepos": {
			client: driftClients()["EnabledRepos"].client, cr: organization([]string{repo, repo2}),
			wantMessage: "drift: actions.enabledRepos", wantActions: inSync, wantDepend: inSync,
		},
		"EnabledReposAndActionsSecrets": {
			client: both, cr: organization([]string{repo, repo2}),
			wantMessage: "drift: actions.enabledRepos", wantActions: noAccess, wantDepend: inSync,
		},
		"ActionsSecrets": {
			client: driftClients()["ActionsSecrets"].client, cr: organization([]string{repo, repo2}),
			wantMessage: "drift: secrets.actionsSecrets", wantActions: noAccess, wantDepend: inSync,
		},
		"Description": {
			client: inSyncClient(), cr: organization([]string{repo, repo2}, withDescription()),
			wantMessage: "drift: description", wantActions: inSync, wantDepend: inSync,
		},
	}
	for name, tc := range cases {
		check := func(t *testing.T, got managed.ExternalObservation, ap v1alpha1.OrganizationObservation, ready xpv2.Condition) {
			t.Helper()
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and not up to date", got)
			}
			if !strings.HasPrefix(ready.Message, tc.wantMessage) {
				t.Errorf("Ready message = %q, want it to start with %q", ready.Message, tc.wantMessage)
			}
			if ap.Secrets == nil {
				t.Fatal("secrets were not mirrored")
			}
			if diff := cmp.Diff(tc.wantActions, ap.Secrets.ActionsSecrets); diff != "" {
				t.Errorf("actionsSecrets: -want, +got:\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantDepend, ap.Secrets.DependabotSecrets); diff != "" {
				t.Errorf("dependabotSecrets: -want, +got:\n%s", diff)
			}
			if ap.Description != description {
				t.Errorf("description = %q, want %q", ap.Description, description)
			}
		}

		t.Run("Cluster/"+name, func(t *testing.T) {
			cr := tc.cr.DeepCopy()
			got, err := (&external{github: tc.client}).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			check(t, got, cr.Status.AtProvider, cr.GetCondition(xpv2.TypeReady))
		})

		t.Run("Namespaced/"+name, func(t *testing.T) {
			ns := &namespacedv1alpha1.Organization{}
			ns.Namespace = "default"
			if err := roundTrip(tc.cr, ns); err != nil {
				t.Fatal(err)
			}
			meta.SetExternalName(ns, org)
			bridged := scopebridge.New[*namespacedv1alpha1.Organization](
				&external{github: tc.client},
				func() *v1alpha1.Organization { return &v1alpha1.Organization{} },
			)
			got, err := bridged.Observe(context.Background(), ns)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			data, err := json.Marshal(ns.Status.AtProvider)
			if err != nil {
				t.Fatal(err)
			}
			var ap v1alpha1.OrganizationObservation
			if err := json.Unmarshal(data, &ap); err != nil {
				t.Fatal(err)
			}
			check(t, got, ap, ns.GetCondition(xpv2.TypeReady))
		})
	}
}
