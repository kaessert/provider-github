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

	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-github/v90/github"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// An empty list in the spec and an absent list on GitHub both mean "none", so
// they are not drift: reporting them as drift updates the object on every poll
// and leaves it Ready=False. A real difference still is drift.
func TestObserveEmptyEqualsAbsent(t *testing.T) {
	type tweak struct {
		spec func(*clusterv1alpha1.Repository)
		gh   func(*fake.MockRepositoriesClient)
	}

	// ruleset changes the single declared ruleset and the one GitHub returns for it.
	ruleset := func(spec func(*clusterv1alpha1.RepositoryRuleset), gh func(*github.RepositoryRuleset)) tweak {
		return tweak{
			spec: func(cr *clusterv1alpha1.Repository) { spec(&cr.Spec.ForProvider.RepositoryRules[0]) },
			gh: func(repos *fake.MockRepositoriesClient) {
				repos.MockGetRuleset = func(_ context.Context, _, _ string, _ int64, _ bool) (*github.RepositoryRuleset, *github.Response, error) {
					rs := githubRuleset()[0]
					gh(rs)
					return rs, fake.GenerateEmptyResponse(), nil
				}
			},
		}
	}
	bypass := func(spec []*clusterv1alpha1.RulesetByPassActors, gh []*github.BypassActor) tweak {
		return ruleset(
			func(r *clusterv1alpha1.RepositoryRuleset) { r.BypassActors = spec },
			func(r *github.RepositoryRuleset) { r.BypassActors = gh },
		)
	}
	actor := func() ([]*clusterv1alpha1.RulesetByPassActors, []*github.BypassActor) {
		return []*clusterv1alpha1.RulesetByPassActors{{ActorID: &rr1actorId, ActorType: &rr1actorType, BypassMode: &rr1bypassMode}},
			[]*github.BypassActor{{
				ActorID:    &rr1actorId,
				ActorType:  github.Ptr(github.BypassActorType(rr1actorType)),
				BypassMode: github.Ptr(github.BypassMode(rr1bypassMode)),
			}}
	}
	specActor, ghActor := actor()

	// protection changes the single declared branch rule and the protection GitHub returns for it.
	protection := func(spec func(*clusterv1alpha1.BranchProtectionRule), gh func(*github.Protection)) tweak {
		return tweak{
			spec: func(cr *clusterv1alpha1.Repository) { spec(&cr.Spec.ForProvider.BranchProtectionRules[0]) },
			gh: func(repos *fake.MockRepositoriesClient) {
				repos.MockGetBranchProtection = func(_ context.Context, _, _, _ string) (*github.Protection, *github.Response, error) {
					p := githubProtectedBranch()
					gh(p)
					return p, fake.GenerateEmptyResponse(), nil
				}
			},
		}
	}
	empty := func() *[]string { return &[]string{} }

	cases := map[string]struct {
		reason string
		tweak  tweak
		want   bool
	}{
		"BypassActorsDeclaredEmptyGitHubAbsent": {
			reason: "bypassActors: [] and no bypass actors on GitHub is in sync",
			tweak:  bypass([]*clusterv1alpha1.RulesetByPassActors{}, nil),
			want:   true,
		},
		"BypassActorsDeclaredEmptyGitHubEmpty": {
			reason: "bypassActors: [] and an empty list on GitHub is in sync",
			tweak:  bypass([]*clusterv1alpha1.RulesetByPassActors{}, []*github.BypassActor{}),
			want:   true,
		},
		"BypassActorsDeclaredNilGitHubAbsent": {
			reason: "no bypassActors and no bypass actors on GitHub is in sync",
			tweak:  bypass(nil, nil),
			want:   true,
		},
		"BypassActorsDeclaredNilGitHubEmpty": {
			reason: "no bypassActors and an empty list on GitHub is in sync",
			tweak:  bypass(nil, []*github.BypassActor{}),
			want:   true,
		},
		"BypassActorsDeclaredEmptyGitHubHasActor": {
			reason: "bypassActors: [] while GitHub has an actor is drift",
			tweak:  bypass([]*clusterv1alpha1.RulesetByPassActors{}, ghActor),
			want:   false,
		},
		"BypassActorsDeclaredActorGitHubAbsent": {
			reason: "a declared bypass actor that GitHub lacks is drift",
			tweak:  bypass(specActor, nil),
			want:   false,
		},
		"RulesetRefNameExcludeDeclaredEmptyGitHubAbsent": {
			reason: "conditions.refName.exclude: [] and no exclusions on GitHub is in sync",
			tweak: ruleset(
				func(r *clusterv1alpha1.RepositoryRuleset) { r.Conditions.RefName.Exclude = []string{} },
				func(r *github.RepositoryRuleset) { r.Conditions.RefName.Exclude = nil },
			),
			want: true,
		},
		"RulesetRefNameExcludeDeclaredValueGitHubAbsent": {
			reason: "an exclusion GitHub lacks is drift",
			tweak: ruleset(
				func(*clusterv1alpha1.RepositoryRuleset) {},
				func(r *github.RepositoryRuleset) { r.Conditions.RefName.Exclude = nil },
			),
			want: false,
		},
		"RulesetRequiredDeploymentsEnvironmentsDeclaredEmptyGitHubAbsent": {
			reason: "requiredDeployments.environments: [] and none on GitHub is in sync",
			tweak: ruleset(
				func(r *clusterv1alpha1.RepositoryRuleset) {
					r.Rules.RequiredDeployments = &clusterv1alpha1.RulesRequiredDeployments{Environments: []string{}}
				},
				func(r *github.RepositoryRuleset) {
					r.Rules.RequiredDeployments = &github.RequiredDeploymentsRuleParameters{}
				},
			),
			want: true,
		},
		"RulesetRequiredDeploymentsEnvironmentDeclaredGitHubAbsent": {
			reason: "a required environment GitHub lacks is drift",
			tweak: ruleset(
				func(r *clusterv1alpha1.RepositoryRuleset) {
					r.Rules.RequiredDeployments = &clusterv1alpha1.RulesRequiredDeployments{Environments: []string{"prod"}}
				},
				func(r *github.RepositoryRuleset) {
					r.Rules.RequiredDeployments = &github.RequiredDeploymentsRuleParameters{}
				},
			),
			want: false,
		},
		"RulesetRequiredStatusChecksDeclaredEmptyGitHubEmpty": {
			reason: "requiredStatusChecks: [] and no checks on GitHub is in sync",
			tweak: ruleset(
				func(r *clusterv1alpha1.RepositoryRuleset) {
					r.Rules.RequiredStatusChecks = &clusterv1alpha1.RulesRequiredStatusChecks{RequiredStatusChecks: []*clusterv1alpha1.RulesRequiredStatusChecksParameters{}}
				},
				func(r *github.RepositoryRuleset) {
					r.Rules.RequiredStatusChecks = &github.RequiredStatusChecksRuleParameters{}
				},
			),
			want: true,
		},
		"RulesetRequiredStatusChecksDeclaredNilGitHubEmpty": {
			reason: "no requiredStatusChecks list and an empty one on GitHub is in sync",
			tweak: ruleset(
				func(r *clusterv1alpha1.RepositoryRuleset) {
					r.Rules.RequiredStatusChecks = &clusterv1alpha1.RulesRequiredStatusChecks{}
				},
				func(r *github.RepositoryRuleset) {
					r.Rules.RequiredStatusChecks = &github.RequiredStatusChecksRuleParameters{RequiredStatusChecks: []*github.RuleStatusCheck{}}
				},
			),
			want: true,
		},
		"BranchProtectionRestrictionsDeclaredEmptyGitHubAbsent": {
			reason: "branch protection restrictions with empty users, teams and apps, and none on GitHub, is in sync",
			tweak: protection(
				func(r *clusterv1alpha1.BranchProtectionRule) {
					r.BranchProtectionRestrictions = &clusterv1alpha1.BranchProtectionRestrictions{Users: []string{}, Teams: []string{}, Apps: []string{}}
				},
				func(p *github.Protection) { p.Restrictions = &github.BranchRestrictions{} },
			),
			want: true,
		},
		"BranchProtectionRestrictionsDeclaredEmptyGitHubHasUser": {
			reason: "a restricted user on GitHub that the spec leaves out is drift",
			tweak: protection(
				func(r *clusterv1alpha1.BranchProtectionRule) {
					r.BranchProtectionRestrictions = &clusterv1alpha1.BranchProtectionRestrictions{Users: []string{}, Teams: []string{}, Apps: []string{}}
				},
				func(p *github.Protection) {
					p.Restrictions = &github.BranchRestrictions{Users: []*github.User{{Login: &user1}}}
				},
			),
			want: false,
		},
		"BranchProtectionBypassAllowancesDeclaredEmptyGitHubAbsent": {
			reason: "bypass pull request allowances with empty lists, and none on GitHub, is in sync",
			tweak: protection(
				func(r *clusterv1alpha1.BranchProtectionRule) {
					r.RequiredPullRequestReviews.BypassPullRequestAllowances = &clusterv1alpha1.BypassPullRequestAllowancesRequest{Users: []string{}, Teams: []string{}, Apps: []string{}}
				},
				func(p *github.Protection) {
					p.RequiredPullRequestReviews.BypassPullRequestAllowances = &github.BypassPullRequestAllowances{}
				},
			),
			want: true,
		},
		"BranchProtectionDismissalRestrictionsDeclaredEmptyGitHubAbsent": {
			reason: "dismissal restrictions pointing at empty lists, and none on GitHub, is in sync",
			tweak: protection(
				func(r *clusterv1alpha1.BranchProtectionRule) {
					r.RequiredPullRequestReviews.DismissalRestrictions = &clusterv1alpha1.DismissalRestrictionsRequest{Users: empty(), Teams: empty(), Apps: empty()}
				},
				func(p *github.Protection) {
					p.RequiredPullRequestReviews.DismissalRestrictions = &github.DismissalRestrictions{}
				},
			),
			want: true,
		},
		"BranchProtectionDismissalRestrictionsDeclaredEmptyGitHubHasUser": {
			reason: "a dismissal user on GitHub that the spec leaves out is drift",
			tweak: protection(
				func(r *clusterv1alpha1.BranchProtectionRule) {
					r.RequiredPullRequestReviews.DismissalRestrictions = &clusterv1alpha1.DismissalRestrictionsRequest{Users: empty(), Teams: empty(), Apps: empty()}
				},
				func(p *github.Protection) {
					p.RequiredPullRequestReviews.DismissalRestrictions = &github.DismissalRestrictions{Users: []*github.User{{Login: &user1}}}
				},
			),
			want: false,
		},
		"BranchProtectionStatusChecksDeclaredEmptyGitHubEmpty": {
			reason: "required status checks: [] and an empty list on GitHub is in sync",
			tweak: protection(
				func(r *clusterv1alpha1.BranchProtectionRule) {
					r.RequiredStatusChecks.Checks = []*clusterv1alpha1.RequiredStatusCheck{}
				},
				func(p *github.Protection) { p.RequiredStatusChecks.Checks = &[]*github.RequiredStatusCheck{} },
			),
			want: true,
		},
		"BranchProtectionStatusChecksDeclaredEmptyGitHubAbsent": {
			reason: "required status checks: [] and no list on GitHub is in sync",
			tweak: protection(
				func(r *clusterv1alpha1.BranchProtectionRule) {
					r.RequiredStatusChecks.Checks = []*clusterv1alpha1.RequiredStatusCheck{}
				},
				func(p *github.Protection) { p.RequiredStatusChecks.Checks = nil },
			),
			want: true,
		},
		"TopicsDeclaredEmptyGitHubAbsent": {
			reason: "topics: [] and no topics on GitHub is in sync",
			tweak: tweak{
				spec: func(cr *clusterv1alpha1.Repository) { cr.Spec.ForProvider.Topics = []string{} },
				gh: func(repos *fake.MockRepositoriesClient) {
					repos.MockGet = func(_ context.Context, _, _ string) (*github.Repository, *github.Response, error) {
						r := githubRepository()
						r.Topics = nil
						return r, nil, nil
					}
				},
			},
			want: true,
		},
		"TopicsDeclaredValueGitHubAbsent": {
			reason: "a declared topic GitHub lacks is drift",
			tweak: tweak{
				spec: func(*clusterv1alpha1.Repository) {},
				gh: func(repos *fake.MockRepositoriesClient) {
					repos.MockGet = func(_ context.Context, _, _ string) (*github.Repository, *github.Response, error) {
						r := githubRepository()
						r.Topics = nil
						return r, nil, nil
					}
				},
			},
			want: false,
		},
		"WebhooksDeclaredEmptyGitHubAbsent": {
			reason: "webhooks: [] and no webhooks on GitHub is in sync",
			tweak: tweak{
				spec: func(cr *clusterv1alpha1.Repository) {
					cr.Spec.ForProvider.Webhooks = []clusterv1alpha1.RepositoryWebhook{}
				},
				gh: func(repos *fake.MockRepositoriesClient) {
					repos.MockListHooks = func(_ context.Context, _, _ string, _ *github.ListOptions) ([]*github.Hook, *github.Response, error) {
						return nil, fake.GenerateEmptyResponse(), nil
					}
				},
			},
			want: true,
		},
		"WebhookEventsDeclaredEmptyGitHubAbsent": {
			reason: "a webhook with events: [] and no events on GitHub is in sync",
			tweak: tweak{
				spec: func(cr *clusterv1alpha1.Repository) { cr.Spec.ForProvider.Webhooks[0].Events = []string{} },
				gh: func(repos *fake.MockRepositoriesClient) {
					repos.MockListHooks = func(_ context.Context, _, _ string, _ *github.ListOptions) ([]*github.Hook, *github.Response, error) {
						hooks := githubWebhooks()
						hooks[0].Events = nil
						return hooks, fake.GenerateEmptyResponse(), nil
					}
				},
			},
			want: true,
		},
		"TeamsDeclaredEmptyGitHubAbsent": {
			reason: "permissions.teams: [] and no teams on GitHub is in sync",
			tweak: tweak{
				spec: func(cr *clusterv1alpha1.Repository) {
					cr.Spec.ForProvider.Permissions.Teams = []clusterv1alpha1.RepositoryTeam{}
				},
				gh: func(repos *fake.MockRepositoriesClient) {
					repos.MockListTeams = func(_ context.Context, _, _ string, _ *github.ListOptions) ([]*github.Team, *github.Response, error) {
						return nil, fake.GenerateEmptyResponse(), nil
					}
				},
			},
			want: true,
		},
		"TeamsDeclaredGitHubAbsent": {
			reason: "declared teams that GitHub lacks are drift",
			tweak: tweak{
				spec: func(*clusterv1alpha1.Repository) {},
				gh: func(repos *fake.MockRepositoriesClient) {
					repos.MockListTeams = func(_ context.Context, _, _ string, _ *github.ListOptions) ([]*github.Team, *github.Response, error) {
						return nil, fake.GenerateEmptyResponse(), nil
					}
				},
			},
			want: false,
		},
	}

	for name, tc := range cases {
		observe := func(t *testing.T, scope string) {
			repos := upToDateRepositories(nil)
			// A declared actor GitHub lacks is probed for implicit write access; it has none.
			repos.MockGetPermissionLevel = func(_ context.Context, _, _, _ string) (*github.RepositoryPermissionLevel, *github.Response, error) {
				return &github.RepositoryPermissionLevel{Permission: github.Ptr(repoPermissionPull)}, fake.GenerateEmptyResponse(), nil
			}
			cr := repository()
			tc.tweak.spec(cr)
			tc.tweak.gh(repos)

			var mg resource.Managed = cr
			c := &external{github: clientFor(repos)}
			var (
				got managed.ExternalObservation
				err error
			)
			if scope == "Namespaced" {
				ns := ddtest.Convert(t, cr, &namespacedv1alpha1.Repository{})
				mg = ns
				got, err = scopebridge.New[*namespacedv1alpha1.Repository](c,
					func() *clusterv1alpha1.Repository { return &clusterv1alpha1.Repository{} }).Observe(context.Background(), ns)
			} else {
				got, err = c.Observe(context.Background(), cr)
			}
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.want {
				t.Errorf("%s\nResourceUpToDate = %v, want %v", tc.reason, got.ResourceUpToDate, tc.want)
			}
			ready := mg.GetCondition(xpv2.TypeReady).Status == corev1.ConditionTrue
			if ready != tc.want {
				t.Errorf("%s\nReady = %v, want %v", tc.reason, ready, tc.want)
			}
		}
		t.Run("Cluster/"+name, func(t *testing.T) { observe(t, "Cluster") })
		t.Run("Namespaced/"+name, func(t *testing.T) { observe(t, "Namespaced") })
	}
}
