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
	"testing"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/provider-github/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/controller/scopebridge"
	"github.com/provider-github/provider-github/internal/driftdetection/ddtest"
)

// observeOnlyRepository is what an Observe-only import declares: the organization and
// the external name, and nothing else.
func observeOnlyRepository(policies ...xpv2.ManagementAction) *v1alpha1.Repository {
	cr := &v1alpha1.Repository{}
	cr.Spec.ForProvider.Org = "acme"
	cr.Spec.ManagementPolicies = policies
	meta.SetExternalName(cr, repo)
	return cr
}

// publicRepositories serves a public, template repository with a description,
// collaborators, teams and webhooks, whatever the spec says.
func publicRepositories(archivedOnGitHub bool) func() *externalFixture {
	return func() *externalFixture {
		repos := upToDateRepositories(nil)
		repos.MockGet = func(context.Context, string, string) (*github.Repository, *github.Response, error) {
			r := githubRepository()
			r.Private = github.Ptr(false)
			r.IsTemplate = github.Ptr(true)
			r.Archived = github.Ptr(archivedOnGitHub)
			return r, nil, nil
		}
		return &externalFixture{e: &external{github: clientFor(repos, upToDateRulesets())}}
	}
}

type externalFixture struct{ e *external }

// An Observe-only import declares none of archived, private, isTemplate,
// description, permissions: none is compared, so the repository is up to date
// and Ready, whether GitHub holds it active or archived.
func TestObserveOnlyOmittedFieldsNoDrift(t *testing.T) {
	for name, archivedOnGitHub := range map[string]bool{"Active": false, "Archived": true} {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyRepository(xpv2.ManagementActionObserve)
			got, err := publicRepositories(archivedOnGitHub)().e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || !got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and up to date", got)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonAvailable {
				t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.ReasonAvailable)
			}
			if c := cr.GetCondition(typeSettingsPartial); c.Status == "True" {
				t.Errorf("SettingsPartial = %s, want not set for an omitted field", c.Status)
			}
		})
	}
}

// An omitted private is not compared against a public repository, and an
// unrequested description or isTemplate is not pushed.
func TestObserveOnlyOmittedPrivateNoSettingsDrift(t *testing.T) {
	cr := observeOnlyRepository(xpv2.ManagementActionObserve, xpv2.ManagementActionLateInitialize)
	gh := githubRepository()
	gh.Private = github.Ptr(false)
	if got := pushedSettings(editRequest(cr, gh, repo), gh); len(got) != 0 {
		t.Errorf("pushedSettings = %+v, want none for an undeclared spec", got)
	}
}

// Under a policy that writes, omitted fields keep their defaults and report
// drift against a repository that differs from them, as before.
func TestObserveWritesAllowedOmittedFieldsStillDrift(t *testing.T) {
	for name, policies := range map[string][]xpv2.ManagementAction{
		"Default":       nil,
		"All":           {xpv2.ManagementActionAll},
		"ObserveUpdate": {xpv2.ManagementActionObserve, xpv2.ManagementActionUpdate},
		"ObserveCreate": {xpv2.ManagementActionObserve, xpv2.ManagementActionCreate},
	} {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyRepository(policies...)
			got, err := publicRepositories(false)().e.Observe(context.Background(), cr)
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

// A field the Observe-only spec does declare is still compared, an explicitly
// empty list included.
func TestObserveOnlyDeclaredFieldsStillDrift(t *testing.T) {
	cases := map[string]struct {
		archivedOnGitHub bool
		mod              func(*v1alpha1.Repository)
	}{
		"Private":     {mod: func(r *v1alpha1.Repository) { r.Spec.ForProvider.Private = github.Ptr(true) }},
		"Description": {mod: func(r *v1alpha1.Repository) { r.Spec.ForProvider.Description = "declared elsewhere" }},
		"IsTemplate":  {mod: func(r *v1alpha1.Repository) { r.Spec.ForProvider.IsTemplate = github.Ptr(false) }},
		"Archived":    {archivedOnGitHub: true, mod: func(r *v1alpha1.Repository) { r.Spec.ForProvider.Archived = github.Ptr(false) }},
		"EmptyTeams":  {mod: func(r *v1alpha1.Repository) { r.Spec.ForProvider.Permissions.Teams = []v1alpha1.RepositoryTeam{} }},
		"EmptyUsers":  {mod: func(r *v1alpha1.Repository) { r.Spec.ForProvider.Permissions.Users = []v1alpha1.RepositoryUser{} }},
		"ArchivedEmptyTeams": {archivedOnGitHub: true, mod: func(r *v1alpha1.Repository) {
			r.Spec.ForProvider.Permissions.Teams = []v1alpha1.RepositoryTeam{}
		}},
		"ArchivedEmptyUsers": {archivedOnGitHub: true, mod: func(r *v1alpha1.Repository) {
			r.Spec.ForProvider.Permissions.Users = []v1alpha1.RepositoryUser{}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyRepository(xpv2.ManagementActionObserve)
			tc.mod(cr)
			got, err := publicRepositories(tc.archivedOnGitHub)().e.Observe(context.Background(), cr)
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

// The namespaced variant serves the same Observe-only import: its management
// policy crosses the scope bridge, so the omitted fields are not compared.
func TestNamespacedObserveOnlyOmittedFieldsNoDrift(t *testing.T) {
	e := scopebridge.New[*namespacedv1alpha1.Repository](publicRepositories(false)().e,
		func() *v1alpha1.Repository { return &v1alpha1.Repository{} })
	cr := ddtest.Convert(t, observeOnlyRepository(xpv2.ManagementActionObserve), &namespacedv1alpha1.Repository{})
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || !got.ResourceUpToDate {
		t.Fatalf("Observe = %+v, want exists and up to date", got)
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonAvailable {
		t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.ReasonAvailable)
	}
}
