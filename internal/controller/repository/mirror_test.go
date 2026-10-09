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
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

func observeMirror(t *testing.T, cr *v1alpha1.Repository, repos *fake.MockRepositoriesClient) {
	t.Helper()
	cr.Spec.ForProvider.Org = "acme"
	e := external{github: clientFor(repos)}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Observe mirrors everything GitHub reports into status.atProvider: the
// settings from the repository response, and the access, webhooks, branch
// protection and rulesets from the reads Observe already makes.
func TestObserveMirrorsRepository(t *testing.T) {
	cr := repository()
	observeMirror(t, cr, upToDateRepositories(nil))

	f := false
	tr := true
	want := v1alpha1.RepositoryObservation{
		ID:          repo,
		Description: description,
		Org:         "acme",
		Archived:    &f,
		Private:     &tr,
		IsTemplate:  &f,
		Topics:      []string{topic3, topic1, topic2},
		Permissions: v1alpha1.RepositoryPermissionsObservation{
			Users: []v1alpha1.RepositoryUserObservation{{User: user1, Role: "pull"}},
			Teams: []v1alpha1.RepositoryTeamObservation{{Team: team1, Role: team1Role}, {Team: team2, Role: team2Role}},
		},
		Webhooks: []v1alpha1.RepositoryWebhookObservation{{
			URL: webhook1url, InsecureSSL: &f, ContentType: webhook1ContentType, Events: []string{webhook1event1, webhook1event2}, Active: &tr,
		}},
		BranchProtectionRules: []v1alpha1.BranchProtectionRuleObservation{{
			Branch:               bpr1branch,
			RequiredStatusChecks: &v1alpha1.RequiredStatusChecksObservation{Strict: &tr, Checks: []v1alpha1.RequiredStatusCheckObservation{{Context: bpr1requiredStatusCheck}}},
			RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviewsObservation{
				DismissStaleReviews:          &f,
				RequireCodeOwnerReviews:      &f,
				RequiredApprovingReviewCount: new(int),
				RequireLastPushApproval:      &f,
				BypassPullRequestAllowances:  &v1alpha1.BypassPullRequestAllowancesObservation{Users: []string{user1}, Teams: []string{team1}, Apps: []string{githubApp1}},
				DismissalRestrictions:        &v1alpha1.DismissalRestrictionsObservation{Users: []string{user1}, Teams: []string{team1}, Apps: []string{githubApp1}},
			},
			BranchProtectionRestrictions:   &v1alpha1.BranchProtectionRestrictionsObservation{BlockCreations: &f, Users: []string{user1}, Teams: []string{team1}, Apps: []string{githubApp1}},
			EnforceAdmins:                  &tr,
			RequireLinearHistory:           &tr,
			AllowForcePushes:               &f,
			AllowDeletions:                 &f,
			RequiredConversationResolution: &tr,
			LockBranch:                     &f,
			AllowForkSyncing:               &f,
			RequireSignedCommits:           &f,
		}},
		RepositoryRules: []v1alpha1.RepositoryRulesetObservation{{
			Name:         rr1name,
			Enforcement:  &rr1enforcement,
			Target:       &rr1target,
			BypassActors: []v1alpha1.RulesetByPassActorsObservation{{ActorID: github.Ptr(rr1actorId), ActorType: &rr1actorType, BypassMode: &rr1bypassMode}},
			Conditions:   &v1alpha1.RulesetConditionsObservation{RefName: &v1alpha1.RulesetRefNameObservation{Include: rr1Include, Exclude: rr1Exclude}},
			Rules: &v1alpha1.RulesObservation{
				Creation: &tr, Deletion: &tr, Update: &tr, RequiredLinearHistory: &tr, RequiredSignatures: &tr, NonFastForward: &tr,
			},
		}},
		UnappliedBranchProtection: []v1alpha1.UnappliedBranchProtection{},
	}
	if diff := cmp.Diff(want, cr.Status.AtProvider); diff != "" {
		t.Errorf("atProvider: -want, +got:\n%s", diff)
	}
}

// The settings GitHub reports are mirrored whether or not they match the spec.
func TestObserveMirrorsSettings(t *testing.T) {
	repos := upToDateRepositories(nil)
	repos.MockGet = func(context.Context, string, string) (*github.Repository, *github.Response, error) {
		r := githubRepository()
		r.Owner = &github.User{Login: github.Ptr("the-owner")}
		r.DefaultBranch = github.Ptr("trunk")
		r.AllowMergeCommit = github.Ptr(true)
		r.AllowSquashMerge = github.Ptr(false)
		r.AllowRebaseMerge = github.Ptr(true)
		r.AllowAutoMerge = github.Ptr(true)
		r.AllowUpdateBranch = github.Ptr(false)
		r.DeleteBranchOnMerge = github.Ptr(true)
		r.HasIssues = github.Ptr(true)
		r.HasProjects = github.Ptr(false)
		r.HasWiki = github.Ptr(true)
		r.HasDiscussions = github.Ptr(false)
		r.MergeCommitTitle = github.Ptr("PR_TITLE")
		r.MergeCommitMessage = github.Ptr("PR_BODY")
		r.SquashMergeCommitTitle = github.Ptr("COMMIT_OR_PR_TITLE")
		r.SquashMergeCommitMessage = github.Ptr("COMMIT_MESSAGES")
		r.TemplateRepository = &github.Repository{Name: github.Ptr("tmpl"), Owner: &github.User{Login: github.Ptr("tmpl-owner")}}
		r.Fork = github.Ptr(true)
		r.Parent = &github.Repository{Name: github.Ptr("upstream"), Owner: &github.User{Login: github.Ptr("up-owner")}}
		return r, nil, nil
	}

	cr := repository(withDefaultBranch("main"))
	observeMirror(t, cr, repos)

	ap := cr.Status.AtProvider
	f, tr := false, true
	got := v1alpha1.RepositoryObservation{
		Org: ap.Org, DefaultBranch: ap.DefaultBranch, AllowMergeCommit: ap.AllowMergeCommit, AllowSquashMerge: ap.AllowSquashMerge,
		AllowRebaseMerge: ap.AllowRebaseMerge, AllowAutoMerge: ap.AllowAutoMerge, AllowUpdateBranch: ap.AllowUpdateBranch,
		DeleteBranchOnMerge: ap.DeleteBranchOnMerge, HasIssues: ap.HasIssues, HasProjects: ap.HasProjects, HasWiki: ap.HasWiki,
		HasDiscussions: ap.HasDiscussions, MergeCommitTitle: ap.MergeCommitTitle, MergeCommitMessage: ap.MergeCommitMessage,
		SquashMergeCommitTitle: ap.SquashMergeCommitTitle, SquashMergeCommitMessage: ap.SquashMergeCommitMessage,
		CreateFromTemplate: ap.CreateFromTemplate, CreateFork: ap.CreateFork,
	}
	want := v1alpha1.RepositoryObservation{
		Org: "the-owner", DefaultBranch: github.Ptr("trunk"), AllowMergeCommit: &tr, AllowSquashMerge: &f, AllowRebaseMerge: &tr,
		AllowAutoMerge: &tr, AllowUpdateBranch: &f, DeleteBranchOnMerge: &tr, HasIssues: &tr, HasProjects: &f, HasWiki: &tr,
		HasDiscussions: &f, MergeCommitTitle: github.Ptr("PR_TITLE"), MergeCommitMessage: github.Ptr("PR_BODY"),
		SquashMergeCommitTitle: github.Ptr("COMMIT_OR_PR_TITLE"), SquashMergeCommitMessage: github.Ptr("COMMIT_MESSAGES"),
		CreateFromTemplate: &v1alpha1.TemplateRepoObservation{Owner: "tmpl-owner", Repo: "tmpl"},
		CreateFork:         &v1alpha1.RepoForkObservation{Owner: "up-owner", Repo: "upstream"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("atProvider settings: -want, +got:\n%s", diff)
	}
	if ap.ForceDelete != nil {
		t.Errorf("forceDelete = %v, want it unset: GitHub has no such setting", *ap.ForceDelete)
	}
}

// GitHub is queried for webhooks, branch protection and rulesets only while the
// spec declares them, so a mirror of any of them is dropped when it does not.
func TestObserveMirrorDropsUnqueriedState(t *testing.T) {
	cr := repository()
	cr.Spec.ForProvider.Webhooks = nil
	cr.Spec.ForProvider.BranchProtectionRules = nil
	cr.Spec.ForProvider.RepositoryRules = nil
	cr.Status.AtProvider.Webhooks = []v1alpha1.RepositoryWebhookObservation{{URL: "stale"}}
	cr.Status.AtProvider.BranchProtectionRules = []v1alpha1.BranchProtectionRuleObservation{{Branch: "stale"}}
	cr.Status.AtProvider.RepositoryRules = []v1alpha1.RepositoryRulesetObservation{{Name: "stale"}}
	observeMirror(t, cr, upToDateRepositories(nil))

	ap := cr.Status.AtProvider
	if ap.Webhooks != nil || ap.BranchProtectionRules != nil || ap.RepositoryRules != nil {
		t.Errorf("atProvider = webhooks %v, branch protection %v, rules %v; want none", ap.Webhooks, ap.BranchProtectionRules, ap.RepositoryRules)
	}
}

// An archived repository's frozen settings are not read, and so not mirrored;
// the collaborators and teams it does read are.
func TestObserveMirrorsArchivedRepository(t *testing.T) {
	repos := upToDateRepositories(nil)
	repos.MockGet = func(context.Context, string, string) (*github.Repository, *github.Response, error) {
		r := githubRepository()
		r.Archived = github.Ptr(true)
		return r, nil, nil
	}
	cr := repository(withArchived(true))
	cr.Status.AtProvider.Webhooks = []v1alpha1.RepositoryWebhookObservation{{URL: "stale"}}
	cr.Status.AtProvider.BranchProtectionRules = []v1alpha1.BranchProtectionRuleObservation{{Branch: "stale"}}
	cr.Status.AtProvider.RepositoryRules = []v1alpha1.RepositoryRulesetObservation{{Name: "stale"}}
	observeMirror(t, cr, repos)

	ap := cr.Status.AtProvider
	if ap.Webhooks != nil || ap.BranchProtectionRules != nil || ap.RepositoryRules != nil {
		t.Errorf("atProvider = webhooks %v, branch protection %v, rules %v; want none", ap.Webhooks, ap.BranchProtectionRules, ap.RepositoryRules)
	}
	wantPerms := v1alpha1.RepositoryPermissionsObservation{
		Users: []v1alpha1.RepositoryUserObservation{{User: user1, Role: "pull"}},
		Teams: []v1alpha1.RepositoryTeamObservation{{Team: team1, Role: team1Role}, {Team: team2, Role: team2Role}},
	}
	if diff := cmp.Diff(wantPerms, ap.Permissions); diff != "" {
		t.Errorf("permissions: -want, +got:\n%s", diff)
	}
	if ap.Archived == nil || !*ap.Archived {
		t.Errorf("archived = %v, want true", ap.Archived)
	}
}

// fill sets every field reachable from v to a distinct non-zero value.
func fill(v reflect.Value, n *int) {
	*n++
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), n)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), n)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), n)
			}
		}
	case reflect.String:
		v.SetString("v" + string(rune('a'+*n%26)))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(*n))
	default:
	}
}

// Every observation type that mirrors a spec type carries every field of it
// that GitHub can report: converting a fully populated spec value loses
// nothing but the references that point at a secret.
func TestMirrorKeepsEverySpecField(t *testing.T) {
	check := func(name string, spec any, obs any) {
		t.Helper()
		n := 0
		fill(reflect.ValueOf(spec).Elem(), &n)

		in, err := json.Marshal(spec)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := json.Unmarshal(in, obs); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := json.Marshal(obs)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		var inMap, outMap map[string]any
		_ = json.Unmarshal(in, &inMap)
		_ = json.Unmarshal(out, &outMap)
		// The webhook secret reference is a spec input.
		delete(inMap, "secretKeyRef")
		if diff := cmp.Diff(inMap, outMap); diff != "" {
			t.Errorf("%s: spec value and its mirror differ: -spec, +mirror:\n%s", name, diff)
		}
	}
	check("webhook", &v1alpha1.RepositoryWebhook{}, &v1alpha1.RepositoryWebhookObservation{})
	check("branch protection", &v1alpha1.BranchProtectionRule{}, &v1alpha1.BranchProtectionRuleObservation{})
	check("ruleset", &v1alpha1.RepositoryRuleset{}, &v1alpha1.RepositoryRulesetObservation{})
}

// A fingerprint recorded in status must keep matching the rule it was taken
// from across releases, whatever the rule's actor lists hold. The values are
// those of the encoding the fingerprints were first taken over.
func TestRuleHashIsStable(t *testing.T) {
	list := func(v ...string) *[]string { return &v }
	cases := map[string]struct {
		rule v1alpha1.BranchProtectionRule
		want string
	}{
		"Full": {
			rule: v1alpha1.BranchProtectionRule{
				Branch:               "main",
				RequiredStatusChecks: &v1alpha1.RequiredStatusChecks{Strict: true, Checks: []*v1alpha1.RequiredStatusCheck{{Context: "ci", AppID: github.Ptr(int64(7))}}},
				RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{
					DismissStaleReviews: true, RequireCodeOwnerReviews: true, RequiredApprovingReviewCount: 2, RequireLastPushApproval: github.Ptr(true),
					BypassPullRequestAllowances: &v1alpha1.BypassPullRequestAllowancesRequest{Users: []string{"u"}, Teams: []string{"t"}, Apps: []string{"a"}},
					DismissalRestrictions:       &v1alpha1.DismissalRestrictionsRequest{Users: list("u"), Teams: list("t"), Apps: list("a")},
				},
				BranchProtectionRestrictions:   &v1alpha1.BranchProtectionRestrictions{BlockCreations: github.Ptr(true), Users: []string{"u"}, Teams: []string{"t"}, Apps: []string{"a"}},
				EnforceAdmins:                  true,
				RequireLinearHistory:           github.Ptr(true),
				AllowForcePushes:               github.Ptr(false),
				AllowDeletions:                 github.Ptr(true),
				RequiredConversationResolution: github.Ptr(true),
				LockBranch:                     github.Ptr(false),
				AllowForkSyncing:               github.Ptr(true),
				RequireSignedCommits:           github.Ptr(true),
			},
			want: "b7131c9371e7218e",
		},
		"Minimal": {
			rule: v1alpha1.BranchProtectionRule{Branch: "dev"},
			want: "a9408c8236dfb50f",
		},
		"UnsetAndEmptyActorLists": {
			rule: v1alpha1.BranchProtectionRule{
				Branch: "x",
				RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{
					BypassPullRequestAllowances: &v1alpha1.BypassPullRequestAllowancesRequest{Users: []string{}, Apps: []string{}},
					DismissalRestrictions:       &v1alpha1.DismissalRestrictionsRequest{Users: new([]string), Apps: list("a")},
				},
				BranchProtectionRestrictions: &v1alpha1.BranchProtectionRestrictions{Users: []string{}},
			},
			want: "4468a4c3aca48d9b",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ruleHash(tc.rule); got != tc.want {
				t.Errorf("ruleHash = %s, want %s", got, tc.want)
			}
		})
	}
}
