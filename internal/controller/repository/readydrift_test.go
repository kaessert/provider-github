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

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
)

func withDifferentWebhookEvents() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.Webhooks[0].Events = []string{"release"}
	}
}

func withDifferentRulesetEnforcement() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.RepositoryRules[0].Enforcement = github.Ptr("disabled")
	}
}

func archivedRepositories() *fake.MockRepositoriesClient {
	repos := upToDateRepositories(nil)
	repos.MockGet = func(context.Context, string, string) (*github.Repository, *github.Response, error) {
		r := githubRepository()
		r.Archived = github.Ptr(true)
		return r, nil, nil
	}
	return repos
}

type driftCase struct {
	repos *fake.MockRepositoriesClient
	mods  []repositoryModifier
}

// driftCases has one case per Observe path that reports an existing,
// out-of-date repository.
func driftCases() map[string]driftCase {
	return map[string]driftCase{
		"ArchivedFlag":     {upToDateRepositories(nil), []repositoryModifier{withArchived(true)}},
		"Collaborators":    {upToDateRepositories(nil), []repositoryModifier{withExtraUser("missing-user", "pull")}},
		"Teams":            {upToDateRepositories(nil), []repositoryModifier{withTeamPermission()}},
		"Webhooks":         {upToDateRepositories(nil), []repositoryModifier{withDifferentWebhookEvents()}},
		"BranchProtection": {upToDateRepositories(nil), []repositoryModifier{withRequiredApprovingReviewCount(7)}},
		"RepositoryRules":  {upToDateRepositories(nil), []repositoryModifier{withDifferentRulesetEnforcement()}},
		"Settings":         {upToDateRepositories(nil), []repositoryModifier{withDifferentDescription()}},
		"Topics":           {upToDateRepositories(nil), []repositoryModifier{withDifferentTopics()}},
		"ArchivedTeams":    {archivedRepositories(), []repositoryModifier{withArchived(true), withTeamPermission()}},
		"ArchivedTopics":   {archivedRepositories(), []repositoryModifier{withArchived(true), withDifferentTopics()}},
		"ArchivedCollabs":  {archivedRepositories(), []repositoryModifier{withArchived(true), withRemovedUser()}},
	}
}

// withRemovedUser leaves a collaborator on GitHub that the spec no longer declares.
func withRemovedUser() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.Permissions.Users = nil
	}
}

func assertUnavailable(t *testing.T, c xpv2.Condition) {
	t.Helper()
	if c.Status != "False" || c.Reason != xpv2.ReasonUnavailable {
		t.Errorf("Ready = %s/%s, want False/%s", c.Status, c.Reason, xpv2.ReasonUnavailable)
	}
}

// Every drift path reports an existing, out-of-date repository with Ready
// False, so Ready does not stay True (or unset) while the drift is corrected.
func TestObserveDriftSetsUnavailable(t *testing.T) {
	for name, tc := range driftCases() {
		t.Run(name, func(t *testing.T) {
			cr := repository(tc.mods...)
			// A previous poll left the resource Available.
			cr.SetConditions(xpv2.Available())
			got, err := (&external{github: clientFor(tc.repos)}).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and not up to date", got)
			}
			assertUnavailable(t, cr.GetCondition(xpv2.TypeReady))
		})
	}
}

// A drift path that sets Ready on a fresh adoption (no condition yet) sets it.
func TestObserveDriftSetsUnavailableOnAdoption(t *testing.T) {
	cr := repository(withDifferentTopics())
	if _, err := (&external{github: clientFor(upToDateRepositories(nil))}).Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUnavailable(t, cr.GetCondition(xpv2.TypeReady))
}

// The up-to-date paths still report Available, archived or not.
func TestObserveInSyncSetsAvailable(t *testing.T) {
	cases := map[string]driftCase{
		"Active":   {upToDateRepositories(nil), nil},
		"Archived": {archivedRepositories(), []repositoryModifier{withArchived(true)}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := repository(tc.mods...)
			cr.SetConditions(xpv2.Unavailable())
			got, err := (&external{github: clientFor(tc.repos)}).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || !got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and up to date", got)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Status != "True" || c.Reason != xpv2.ReasonAvailable {
				t.Errorf("Ready = %s/%s, want True/%s", c.Status, c.Reason, xpv2.ReasonAvailable)
			}
		})
	}
}

// The namespaced kind is served by the same client through the scope bridge;
// the condition reaches the namespaced resource.
func TestObserveDriftSetsUnavailableNamespaced(t *testing.T) {
	for name, tc := range driftCases() {
		t.Run(name, func(t *testing.T) {
			cluster := repository(tc.mods...)
			ns := &namespacedv1alpha1.Repository{}
			ns.Namespace = "default"
			data, err := json.Marshal(cluster.Spec.ForProvider)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &ns.Spec.ForProvider); err != nil {
				t.Fatal(err)
			}
			meta.SetExternalName(ns, repo)
			ns.SetConditions(xpv2.Available())

			bridged := scopebridge.New[*namespacedv1alpha1.Repository](
				&external{github: clientFor(tc.repos), secretNamespace: ns.Namespace},
				func() *v1alpha1.Repository { return &v1alpha1.Repository{} },
			)
			got, err := bridged.Observe(context.Background(), ns)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and not up to date", got)
			}
			assertUnavailable(t, ns.GetCondition(xpv2.TypeReady))
		})
	}
}
