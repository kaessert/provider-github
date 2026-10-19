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

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/provider-github/provider-github/internal/clients"
	"github.com/provider-github/provider-github/internal/clients/fake"
	"github.com/provider-github/provider-github/internal/clients/rulesets"
	"github.com/provider-github/provider-github/internal/telemetry"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-github/v90/github"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Unlike many Kubernetes projects Crossplane does not use third party testing
// libraries, per the common Go test review comments. Crossplane encourages the
// use of table driven unit tests. The tests of the crossplane-runtime project
// are representative of the testing style Crossplane encourages.
//
// https://github.com/golang/go/wiki/TestComments
// https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md#contributing-code

type repositoryModifier func(*v1alpha1.Repository)

var (
	repo        = "test-repo"
	description = "desc"
	archived    = false
	private     = true
	isTemplate  = false

	user1     = "test-user-1"
	user1Role = "admin"
	user2     = "test-user-1"
	user2Role = "pull"

	team1     = "test-team-1"
	team1Role = "admin"
	team2     = "test-team-2"
	team2Role = "pull"

	githubApp1 = "my-awesome-app"

	webhook1url            = "https://example.org/webhook"
	webhook1active         = true
	webhook1InsecureSsl    = false
	webhook1InsecureSslStr = "0"
	webhook1ContentType    = "json"
	webhook1event1         = "push"
	webhook1event2         = "workflow_job"

	bpr1branch                         = "main"
	bpr1enforceAdmins                  = true
	bpr1requireLinearHistory           = true
	bpr1allowForcePushes               = false
	bpr1allowDeletions                 = false
	bpr1requiredConversationResolution = true
	bpr1lockBranch                     = false
	bpr1allowForkSyncing               = false
	bpr1requireSignedCommits           = false
	bpr1requiredStatusCheck            = "terraform_validate"

	rr1Id                         int64 = 123
	rr1name                             = "test-ruleset-1"
	rr1target                           = "branch"
	rr1enforcement                      = "active"
	rr1actorType                        = "Team"
	rr1bypassMode                       = "always"
	rr1rulesCreation                    = true
	rr1rulesDeletion                    = true
	rr1rulesUpdate                      = true
	rr1rulesRequiredLinearHistory       = true
	rr1rulesRequiredSignatures          = true
	rr1rulesNonFastForward              = true
	rr1actorId                    int64 = 123
	rr1Include                          = []string{"include"}
	rr1Exclude                          = []string{"exclude"}

	topic1 = "go"
	topic2 = "kubernetes"
	topic3 = "crossplane"
)

func withTeamPermission() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.Permissions.Teams[1].Role = team1Role
	}
}

func withDifferentTopics() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.Topics = []string{"different", "topics"}
	}
}

func withDifferentDescription() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.Description = description + "-drift"
	}
}

func withDefaultBranch(b string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.DefaultBranch = &b
	}
}

func withAllowMergeCommit(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.AllowMergeCommit = &b
	}
}

func withAllowSquashMerge(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.AllowSquashMerge = &b
	}
}

func withAllowRebaseMerge(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.AllowRebaseMerge = &b
	}
}

func withAllowAutoMerge(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.AllowAutoMerge = &b
	}
}

func withAllowUpdateBranch(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.AllowUpdateBranch = &b
	}
}

func withDeleteBranchOnMerge(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.DeleteBranchOnMerge = &b
	}
}

func withHasIssues(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.HasIssues = &b
	}
}

func withHasProjects(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.HasProjects = &b
	}
}

func withHasWiki(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.HasWiki = &b
	}
}

func withHasDiscussions(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.HasDiscussions = &b
	}
}

func withMergeCommitTitle(s string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.MergeCommitTitle = &s
	}
}

func withMergeCommitMessage(s string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.MergeCommitMessage = &s
	}
}

func withSquashMergeCommitTitle(s string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.SquashMergeCommitTitle = &s
	}
}

func withSquashMergeCommitMessage(s string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.SquashMergeCommitMessage = &s
	}
}

func withArchived(b bool) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.Archived = &b
	}
}

func withExtraUser(login, role string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.Permissions.Users = append(r.Spec.ForProvider.Permissions.Users, v1alpha1.RepositoryUser{User: login, Role: role})
	}
}

func repository(m ...repositoryModifier) *v1alpha1.Repository {
	cr := &v1alpha1.Repository{}
	cr.Spec.ForProvider.Permissions = v1alpha1.RepositoryPermissions{
		Users: []v1alpha1.RepositoryUser{
			{
				User: strings.ToUpper(user1),
				Role: user1Role,
			},
			{
				User: strings.ToUpper(user2),
				Role: user2Role,
			},
		},
		Teams: []v1alpha1.RepositoryTeam{
			{
				Team: strings.ToUpper(team1),
				Role: team1Role,
			},
			{
				Team: strings.ToUpper(team2),
				Role: team2Role,
			},
		},
	}

	cr.Spec.ForProvider.Webhooks = []v1alpha1.RepositoryWebhook{
		{
			URL:         webhook1url,
			ContentType: webhook1ContentType,
			Events:      []string{webhook1event1, webhook1event2},
			InsecureSSL: &webhook1InsecureSsl,
			Active:      &webhook1active,
		},
	}

	cr.Spec.ForProvider.BranchProtectionRules = []v1alpha1.BranchProtectionRule{
		{
			Branch:                         bpr1branch,
			EnforceAdmins:                  bpr1enforceAdmins,
			RequireLinearHistory:           &bpr1requireLinearHistory,
			AllowForcePushes:               &bpr1allowForcePushes,
			AllowDeletions:                 &bpr1allowDeletions,
			RequiredConversationResolution: &bpr1requiredConversationResolution,
			LockBranch:                     &bpr1lockBranch,
			AllowForkSyncing:               &bpr1allowForkSyncing,
			RequireSignedCommits:           &bpr1requireSignedCommits,
			RequiredStatusChecks: &v1alpha1.RequiredStatusChecks{
				Strict: true,
				Checks: []*v1alpha1.RequiredStatusCheck{
					{
						Context: bpr1requiredStatusCheck,
					},
				},
			},
			BranchProtectionRestrictions: &v1alpha1.BranchProtectionRestrictions{
				Users: []string{
					strings.ToUpper(user1),
				},
				Teams: []string{
					strings.ToUpper(team1),
				},
				Apps: []string{
					strings.ToUpper(githubApp1),
				},
			},
			RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{
				BypassPullRequestAllowances: &v1alpha1.BypassPullRequestAllowancesRequest{
					Users: []string{
						strings.ToUpper(user1),
					},
					Teams: []string{
						strings.ToUpper(team1),
					},
					Apps: []string{
						strings.ToUpper(githubApp1),
					},
				},
				DismissalRestrictions: &v1alpha1.DismissalRestrictionsRequest{
					Users: &[]string{
						strings.ToUpper(user1),
					},
					Teams: &[]string{
						strings.ToUpper(team1),
					},
					Apps: &[]string{
						strings.ToUpper(githubApp1),
					},
				},
			},
		},
	}
	cr.Spec.ForProvider.RepositoryRules = &[]v1alpha1.RepositoryRuleset{
		{
			Name:        rr1name,
			Target:      &rr1target,
			Enforcement: &rr1enforcement,
			Conditions: &v1alpha1.RulesetConditions{
				RefName: &v1alpha1.RulesetRefName{
					Include: rr1Include,
					Exclude: rr1Exclude,
				},
			},
			BypassActors: []*v1alpha1.RulesetByPassActors{
				{
					ActorID:    &rr1actorId,
					ActorType:  &rr1actorType,
					BypassMode: &rr1bypassMode,
				},
			},
			Rules: &v1alpha1.Rules{
				Creation:              &rr1rulesCreation,
				Deletion:              &rr1rulesDeletion,
				Update:                &rr1rulesUpdate,
				RequiredLinearHistory: &rr1rulesRequiredLinearHistory,
				RequiredSignatures:    &rr1rulesRequiredSignatures,
				NonFastForward:        &rr1rulesNonFastForward,
			},
		},
	}

	cr.Spec.ForProvider.Topics = []string{topic1, topic2, topic3}
	cr.Spec.ForProvider.Description = description

	meta.SetExternalName(cr, repo)

	for _, f := range m {
		f(cr)
	}
	return cr
}

func githubRepository() *github.Repository {
	return &github.Repository{
		Name:        &repo,
		Description: &description,
		Archived:    &archived,
		Private:     &private,
		IsTemplate:  &isTemplate,
		Fork:        github.Ptr(false),
		Topics:      []string{topic1, topic2, topic3},
	}
}

func githubWebhooks() []*github.Hook {
	return []*github.Hook{
		{
			Config: &github.HookConfig{
				URL:         &webhook1url,
				ContentType: &webhook1ContentType,
				InsecureSSL: &webhook1InsecureSslStr,
			},
			Events: []string{webhook1event1, webhook1event2},
			Active: github.Ptr(webhook1active),
		},
	}
}

func githubProtectedBranch() *github.Protection {
	return &github.Protection{
		RequiredStatusChecks: &github.RequiredStatusChecks{
			Strict: true,
			Checks: &[]*github.RequiredStatusCheck{
				{
					Context: bpr1requiredStatusCheck,
				},
			},
		},
		EnforceAdmins: &github.AdminEnforcement{
			Enabled: bpr1enforceAdmins,
		},
		RequireLinearHistory: &github.RequireLinearHistory{
			Enabled: bpr1requireLinearHistory,
		},
		AllowForcePushes: &github.AllowForcePushes{
			Enabled: bpr1allowForcePushes,
		},
		AllowDeletions: &github.AllowDeletions{
			Enabled: bpr1allowDeletions,
		},
		RequiredConversationResolution: &github.RequiredConversationResolution{
			Enabled: bpr1requiredConversationResolution,
		},
		LockBranch: &github.LockBranch{
			Enabled: &bpr1lockBranch,
		},
		AllowForkSyncing: &github.AllowForkSyncing{
			Enabled: &bpr1allowForkSyncing,
		},
		RequiredSignatures: &github.SignaturesProtectedBranch{
			Enabled: &bpr1requireSignedCommits,
		},
		Restrictions: &github.BranchRestrictions{
			Users: []*github.User{
				{
					Login: &user1,
				},
			},
			Teams: []*github.Team{
				{
					Slug: &team1,
				},
			},
			Apps: []*github.App{
				{
					Slug: &githubApp1,
				},
			},
		},
		RequiredPullRequestReviews: &github.PullRequestReviewsEnforcement{
			BypassPullRequestAllowances: &github.BypassPullRequestAllowances{
				Users: []*github.User{
					{
						Login: &user1,
					},
				},
				Teams: []*github.Team{
					{
						Slug: &team1,
					},
				},
				Apps: []*github.App{
					{
						Slug: &githubApp1,
					},
				},
			},
			DismissalRestrictions: &github.DismissalRestrictions{
				Users: []*github.User{
					{
						Login: &user1,
					},
				},
				Teams: []*github.Team{
					{
						Slug: &team1,
					},
				},
				Apps: []*github.App{
					{
						Slug: &githubApp1,
					},
				},
			},
		},
	}
}

func githubRuleset() []*rulesets.Ruleset {
	return []*rulesets.Ruleset{
		{
			ID:          &rr1Id,
			Name:        rr1name,
			Target:      github.Ptr(rr1target),
			Enforcement: rr1enforcement,
			Conditions: &rulesets.Conditions{
				RefName: &rulesets.RefName{
					Include: rr1Include,
					Exclude: rr1Exclude,
				},
			},
			BypassActors: []*rulesets.BypassActor{
				{
					ActorID:    &rr1actorId,
					ActorType:  github.Ptr(rr1actorType),
					BypassMode: github.Ptr(rr1bypassMode),
				},
			},
			Rules: []*rulesets.Rule{
				{Type: "creation"},
				{Type: "deletion"},
				{Type: "update"},
				{Type: "required_linear_history"},
				{Type: "required_signatures"},
				{Type: "non_fast_forward"},
			},
		},
	}
}

// githubRules is the typed form of githubRuleset's rules, with fetch and merge off.
func githubRules() *rulesets.ModelledRules {
	return &rulesets.ModelledRules{
		Creation:              &rulesets.EmptyRuleParameters{},
		Deletion:              &rulesets.EmptyRuleParameters{},
		Update:                &rulesets.UpdateRuleParameters{UpdateAllowsFetchAndMerge: github.Ptr(false)},
		RequiredLinearHistory: &rulesets.EmptyRuleParameters{},
		RequiredSignatures:    &rulesets.EmptyRuleParameters{},
		NonFastForward:        &rulesets.EmptyRuleParameters{},
	}
}

func githubCollaborators() []*github.User {
	return []*github.User{
		{
			Login: &user1,
			Permissions: repoPermissions(map[string]bool{
				user1Role: true,
			}),
		},
		{
			Login: &user2,
			Permissions: repoPermissions(map[string]bool{
				user2Role: true,
			}),
		},
	}
}

func githubTeams() []*github.Team {
	return []*github.Team{
		{
			Slug:       &team1,
			Permission: &team1Role,
		},
		{
			Slug:       &team2,
			Permission: &team2Role,
		},
	}
}

func githubBranches() []*github.Branch {
	return []*github.Branch{
		{
			Name:      &bpr1branch,
			Protected: github.Ptr(true),
		},
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
			fields: fields{github: &ghclient.Client{
				Services: &ghclient.Services{
					Rulesets: upToDateRulesets(),
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							return githubRepository(), nil, nil
						},
						MockEdit: func(ctx context.Context, owner, repo string, repository *github.Repository) (*github.Repository, *github.Response, error) {
							return nil, nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return githubTeams(), fake.GenerateEmptyResponse(), nil
						},
						MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
							return githubWebhooks(), fake.GenerateEmptyResponse(), nil
						},
						MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
							return githubBranches(), fake.GenerateEmptyResponse(), nil
						},
						MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
							return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
						},
					},
				},
			},
			},
			args: args{
				mg: repository(withTeamPermission()),
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
			fields: fields{github: &ghclient.Client{
				Services: &ghclient.Services{
					Rulesets: upToDateRulesets(),
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							return githubRepository(), nil, nil
						},
						MockEdit: func(ctx context.Context, owner, repo string, repository *github.Repository) (*github.Repository, *github.Response, error) {
							return nil, nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return githubTeams(), fake.GenerateEmptyResponse(), nil
						},
						MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
							return githubWebhooks(), fake.GenerateEmptyResponse(), nil
						},
						MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
							return githubBranches(), fake.GenerateEmptyResponse(), nil
						},
						MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
							return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
						},
					},
				},
			},
			},
			args: args{
				mg: repository(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"NotUpToDateTopicsMismatch": {
			fields: fields{github: &ghclient.Client{
				Services: &ghclient.Services{
					Rulesets: upToDateRulesets(),
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							return githubRepository(), nil, nil
						},
						MockEdit: func(ctx context.Context, owner, repo string, repository *github.Repository) (*github.Repository, *github.Response, error) {
							return nil, nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return githubTeams(), fake.GenerateEmptyResponse(), nil
						},
						MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
							return githubWebhooks(), fake.GenerateEmptyResponse(), nil
						},
						MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
							return githubBranches(), fake.GenerateEmptyResponse(), nil
						},
						MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
							return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
						},
					},
				},
			},
			},
			args: args{
				mg: repository(withDifferentTopics()),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		"DoesNotExist": {
			fields: fields{
				github: &ghclient.Client{
					Services: &ghclient.Services{
						Repositories: &fake.MockRepositoriesClient{
							MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
								return nil, nil, fake.Generate404Response()
							},
						},
					},
				},
			},
			args: args{
				mg: repository(),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   false,
					ResourceUpToDate: false,
				},
				err: nil,
			},
		},
		// A declared collaborator with an outstanding (unaccepted) invitation does
		// not register as drift: ListCollaborators(direct) omits them, but the
		// pending invitation is recognized, so Observe stays up to date instead of
		// re-inviting every reconcile.
		"PendingInvitationUpToDate": {
			fields: fields{github: &ghclient.Client{
				Services: &ghclient.Services{
					Rulesets: upToDateRulesets(),
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							return githubRepository(), nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListInvitations: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.RepositoryInvitation, *github.Response, error) {
							return []*github.RepositoryInvitation{{Invitee: &github.User{Login: github.Ptr("pending-user")}}}, fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return githubTeams(), fake.GenerateEmptyResponse(), nil
						},
						MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
							return githubWebhooks(), fake.GenerateEmptyResponse(), nil
						},
						MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
							return githubBranches(), fake.GenerateEmptyResponse(), nil
						},
						MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
							return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
						},
					},
				},
			}},
			args: args{
				mg: repository(withExtraUser("pending-user", "pull")),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		// An archived repo with matching teams, collaborators and topics is up to
		// date. Branch protection, rulesets and webhooks are frozen for writing while
		// archived, not for reading, so they are read for the mirror and compared
		// with nothing.
		"ArchivedUpToDate": {
			fields: fields{github: clientFor(archivedRepositories(), upToDateRulesets())},
			args: args{
				mg: repository(withArchived(true)),
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
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

// TestUpdate pins archived-repo write partitioning. While archived GitHub permits
// team access, topics and collaborator removals but rejects settings/Edit, branch
// protection, rulesets, webhooks and collaborator additions with 403. Update must
// reconcile the permitted dimensions and never issue the frozen ones (which would
// loop). Archiving a live repo issues one Edit(archived=true); unarchiving issues
// Edit(archived=false) before the normal reconcile.
func TestUpdate(t *testing.T) {
	bareRepo := func(archived bool) *v1alpha1.Repository {
		cr := &v1alpha1.Repository{}
		cr.Spec.ForProvider.Archived = &archived
		meta.SetExternalName(cr, repo)
		return cr
	}
	ghRepo := func(arch bool) *github.Repository {
		return &github.Repository{Name: &repo, Archived: &arch, Fork: github.Ptr(false), Topics: []string{topic1, topic2, topic3}}
	}

	type want struct {
		editArchived  []bool // archived value of each Edit call, in order
		frozenWrite   string // a frozen write reached while archived (empty = none)
		addTeamRepo   int
		removeCollab  int
		replaceTopics int
		err           error
	}
	cases := map[string]struct {
		reason  string
		gh      *github.Repository
		cr      *v1alpha1.Repository
		ghUsers []*github.User
		ghTeams []*github.Team
		want    want
	}{
		"AlreadyArchivedNoChanges": {
			reason: "Desired archived, already archived, nothing to change: no Edit and no writes.",
			gh:     ghRepo(true),
			cr:     bareRepo(true),
			want:   want{},
		},
		"TransitionToArchived": {
			reason: "Desired archived, currently live: exactly one Edit(archived=true), no frozen writes.",
			gh:     ghRepo(false),
			cr:     bareRepo(true),
			want:   want{editArchived: []bool{true}},
		},
		"UnarchiveThenReconcile": {
			reason: "Desired live, currently archived: Edit(archived=false) to unarchive, then the normal reconcile Edit.",
			gh:     ghRepo(true),
			cr:     bareRepo(false),
			want:   want{editArchived: []bool{false, false}},
		},
		"ArchivedReconcilesAllowedSkipsFrozen": {
			reason:  "Archived repo with a full spec: teams and topics reconcile; settings, branch protection, rulesets, webhooks and collaborator additions are frozen.",
			gh:      ghRepo(true),
			cr:      repository(withArchived(true)),
			ghUsers: githubCollaborators(), // matches CR users → no collaborator writes
			ghTeams: []*github.Team{},      // CR teams absent → AddTeamRepoBySlug fires
			want:    want{addTeamRepo: 2, replaceTopics: 1},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var editArchived []bool
			frozenWrite := ""
			addTeamRepo, removeCollab, replaceTopics := 0, 0, 0
			repoClient := &fake.MockRepositoriesClient{
				MockGet: func(ctx context.Context, owner, r string) (*github.Repository, *github.Response, error) {
					return tc.gh, nil, nil
				},
				MockEdit: func(ctx context.Context, owner, r string, rr *github.Repository) (*github.Repository, *github.Response, error) {
					editArchived = append(editArchived, rr.GetArchived())
					return rr, nil, nil
				},
				MockListCollaborators: func(ctx context.Context, owner, r string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
					return tc.ghUsers, fake.GenerateEmptyResponse(), nil
				},
				MockListTeams: func(ctx context.Context, owner, r string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
					return tc.ghTeams, fake.GenerateEmptyResponse(), nil
				},
				MockReplaceAllTopics: func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
					replaceTopics++
					return topics, fake.GenerateEmptyResponse(), nil
				},
				MockRemoveCollaborator: func(ctx context.Context, owner, r, user string) (*github.Response, error) {
					removeCollab++
					return fake.GenerateEmptyResponse(), nil
				},
				// Frozen-while-archived writes: must not be reached.
				MockAddCollaborator: func(ctx context.Context, owner, r, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
					frozenWrite = "AddCollaborator"
					return nil, fake.GenerateEmptyResponse(), nil
				},
				MockUpdateBranchProtection: func(ctx context.Context, owner, r, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error) {
					frozenWrite = "UpdateBranchProtection"
					return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
				},
				MockCreateHook: func(ctx context.Context, owner, r string, hook *github.Hook) (*github.Hook, *github.Response, error) {
					frozenWrite = "CreateHook"
					return hook, fake.GenerateEmptyResponse(), nil
				},
			}
			rulesetsClient := &fake.MockRulesetsClient{
				MockCreateRuleset: func(ctx context.Context, owner, r string, rs rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
					frozenWrite = "CreateRuleset"
					return &rs, fake.GenerateEmptyResponse(), nil
				},
			}
			teamsClient := &fake.MockTeamsClient{
				MockAddTeamRepoBySlug: func(ctx context.Context, org, slug, owner, r string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
					addTeamRepo++
					return fake.GenerateEmptyResponse(), nil
				},
				MockRemoveTeamRepoBySlug: func(ctx context.Context, org, slug, owner, r string) (*github.Response, error) {
					return fake.GenerateEmptyResponse(), nil
				},
			}
			e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repoClient, Rulesets: rulesetsClient, Teams: teamsClient}}}
			_, err := e.Update(context.Background(), tc.cr)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nUpdate(): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.editArchived, editArchived); diff != "" {
				t.Errorf("\n%s\nUpdate() Edit archived values: -want, +got:\n%s", tc.reason, diff)
			}
			if frozenWrite != tc.want.frozenWrite {
				t.Errorf("\n%s\nUpdate() frozen write reached = %q, want %q", tc.reason, frozenWrite, tc.want.frozenWrite)
			}
			if addTeamRepo != tc.want.addTeamRepo {
				t.Errorf("\n%s\nUpdate() AddTeamRepoBySlug calls = %d, want %d", tc.reason, addTeamRepo, tc.want.addTeamRepo)
			}
			if removeCollab != tc.want.removeCollab {
				t.Errorf("\n%s\nUpdate() RemoveCollaborator calls = %d, want %d", tc.reason, removeCollab, tc.want.removeCollab)
			}
			if replaceTopics != tc.want.replaceTopics {
				t.Errorf("\n%s\nUpdate() ReplaceAllTopics calls = %d, want %d", tc.reason, replaceTopics, tc.want.replaceTopics)
			}
		})
	}
}

func ghUserWithPerm(login, perm string) *github.User {
	return &github.User{Login: github.Ptr(login), Permissions: repoPermissions(map[string]bool{perm: true})}
}

// repoPermissions builds go-github's permissions struct from permission names.
func repoPermissions(perms map[string]bool) *github.RepositoryPermissions {
	return &github.RepositoryPermissions{
		Admin:    github.Ptr(perms["admin"]),
		Maintain: github.Ptr(perms["maintain"]),
		Push:     github.Ptr(perms["push"]),
		Triage:   github.Ptr(perms["triage"]),
		Pull:     github.Ptr(perms["pull"]),
	}
}

// TestCategorizeCollaborators pins the collaborator buckets. The key invariant is
// that a declared collaborator with an outstanding invitation lands in pendingInvite
// (in flight) rather than toUpsert, so the controller does not re-invite them on
// every reconcile — the loop the prod repos were stuck in.
func TestCategorizeCollaborators(t *testing.T) {
	u := func(login, role string) v1alpha1.RepositoryUser {
		return v1alpha1.RepositoryUser{User: login, Role: role}
	}
	type want struct {
		toRemove     map[string]string
		toUpsert     map[string]string
		pending      []string
		roleEnforced []string
	}
	cases := map[string]struct {
		reason    string
		crUsers   []v1alpha1.RepositoryUser
		ghUsers   []*github.User
		invites   []*github.RepositoryInvitation
		orgAdmins map[string]bool
		want      want
	}{
		"AllPresentMatching": {
			reason:  "Declared users all active at the declared role: no drift.",
			crUsers: []v1alpha1.RepositoryUser{u("alice", "push")},
			ghUsers: []*github.User{ghUserWithPerm("alice", "push")},
			want:    want{toRemove: map[string]string{}, toUpsert: map[string]string{}},
		},
		"PendingInviteNotReAdded": {
			reason:  "A declared user with an outstanding invitation is pending, not an add.",
			crUsers: []v1alpha1.RepositoryUser{u("alice", "push"), u("bob", "pull")},
			ghUsers: []*github.User{ghUserWithPerm("alice", "push")},
			invites: []*github.RepositoryInvitation{{Invitee: &github.User{Login: github.Ptr("bob")}}},
			want:    want{toRemove: map[string]string{}, toUpsert: map[string]string{}, pending: []string{"bob"}},
		},
		"GenuineAdd": {
			reason:  "A declared user who is neither active nor invited is a real add.",
			crUsers: []v1alpha1.RepositoryUser{u("alice", "push"), u("carol", "pull")},
			ghUsers: []*github.User{ghUserWithPerm("alice", "push")},
			want:    want{toRemove: map[string]string{}, toUpsert: map[string]string{"carol": "pull"}},
		},
		"RoleChange": {
			reason:  "A declared user active at a different role is an upsert.",
			crUsers: []v1alpha1.RepositoryUser{u("alice", "admin")},
			ghUsers: []*github.User{ghUserWithPerm("alice", "push")},
			want:    want{toRemove: map[string]string{}, toUpsert: map[string]string{"alice": "admin"}},
		},
		"MaintainMatching": {
			reason:  "GitHub reports a maintainer with maintain, push, triage and pull all set; read as push, the role would be re-applied every reconcile.",
			crUsers: []v1alpha1.RepositoryUser{u("alice", "maintain")},
			ghUsers: []*github.User{{Login: github.Ptr("alice"), Permissions: repoPermissions(map[string]bool{"maintain": true, "push": true, "triage": true, "pull": true})}},
			want:    want{toRemove: map[string]string{}, toUpsert: map[string]string{}},
		},
		"Remove": {
			reason:  "An active collaborator absent from the CR is removed.",
			crUsers: nil,
			ghUsers: []*github.User{ghUserWithPerm("alice", "push")},
			want:    want{toRemove: map[string]string{"alice": "push"}, toUpsert: map[string]string{}},
		},
		"OrgAdminRoleEnforced": {
			reason:    "An org owner declared below admin shows as admin on GitHub and can't be downgraded: enforced, not an upsert.",
			crUsers:   []v1alpha1.RepositoryUser{u("owner1", "push")},
			ghUsers:   []*github.User{ghUserWithPerm("owner1", "admin")},
			orgAdmins: map[string]bool{"owner1": true},
			want:      want{toRemove: map[string]string{}, toUpsert: map[string]string{}, roleEnforced: []string{"owner1"}},
		},
		"NonOwnerGhAdminDowngradeIsUpsert": {
			reason:    "A non-owner showing admin on GitHub but declared lower is a real role change, not enforced.",
			crUsers:   []v1alpha1.RepositoryUser{u("dave", "push")},
			ghUsers:   []*github.User{ghUserWithPerm("dave", "admin")},
			orgAdmins: map[string]bool{}, // dave is not an org owner
			want:      want{toRemove: map[string]string{}, toUpsert: map[string]string{"dave": "push"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &ghclient.Client{Services: &ghclient.Services{
				Repositories: &fake.MockRepositoriesClient{
					MockListCollaborators: func(ctx context.Context, owner, r string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
						return tc.ghUsers, fake.GenerateEmptyResponse(), nil
					},
					MockListInvitations: func(ctx context.Context, owner, r string, opts *github.ListOptions) ([]*github.RepositoryInvitation, *github.Response, error) {
						return tc.invites, fake.GenerateEmptyResponse(), nil
					},
				},
				Organizations: &fake.MockOrganizationsClient{
					MockGetOrgMembership: func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
						role := "member"
						if tc.orgAdmins[user] {
							role = "admin"
						}
						return &github.Membership{Role: github.Ptr(role)}, fake.GenerateEmptyResponse(), nil
					},
				},
			}}
			got, err := categorizeCollaborators(context.Background(), gh, "org", "repo", tc.crUsers)
			if err != nil {
				t.Fatalf("%s\nunexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.toRemove, got.toRemove); diff != "" {
				t.Errorf("%s\ntoRemove: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.toUpsert, got.toUpsert); diff != "" {
				t.Errorf("%s\ntoUpsert: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.pending, got.pendingInvite); diff != "" {
				t.Errorf("%s\npendingInvite: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.roleEnforced, got.roleEnforced); diff != "" {
				t.Errorf("%s\nroleEnforced: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// TestSetCollaboratorPartialCondition checks the condition surfaces pending invitees
// (True) and reports a clean state otherwise (False), so the skip is never silent.
func TestSetCollaboratorPartialCondition(t *testing.T) {
	cases := map[string]struct {
		pending      []string
		roleEnforced []string
		wantStatus   corev1.ConditionStatus
		wantReason   xpv2.ConditionReason
	}{
		"None":         {wantStatus: corev1.ConditionFalse, wantReason: reasonAllCollaboratorsPresent},
		"Pending":      {pending: []string{"bob", "alice"}, wantStatus: corev1.ConditionTrue, wantReason: reasonPendingInvitation},
		"RoleEnforced": {roleEnforced: []string{"owner1"}, wantStatus: corev1.ConditionTrue, wantReason: reasonRoleEnforcedByOrg},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := &v1alpha1.Repository{}
			setCollaboratorPartialCondition(cr, tc.pending, tc.roleEnforced)
			got := cr.GetCondition(typeCollaboratorPartial)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %v, want %v", got.Status, tc.wantStatus)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("reason = %v, want %v", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestObserveMainSettingsDrift pins drift detection for the repo main-settings
// fields. Each case flips a single spec field; Observe must report not-up-to-date.
func TestObserveMainSettingsDrift(t *testing.T) {
	upToDateClient := func() *ghclient.Client {
		return &ghclient.Client{
			Services: &ghclient.Services{
				Rulesets: upToDateRulesets(),
				Repositories: &fake.MockRepositoriesClient{
					MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
						return githubRepository(), nil, nil
					},
					MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
						return githubCollaborators(), fake.GenerateEmptyResponse(), nil
					},
					MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
						return githubTeams(), fake.GenerateEmptyResponse(), nil
					},
					MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
						return githubWebhooks(), fake.GenerateEmptyResponse(), nil
					},
					MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
						return githubBranches(), fake.GenerateEmptyResponse(), nil
					},
					MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
						return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
					},
				},
			},
		}
	}

	cases := map[string]repositoryModifier{
		"DescriptionDrift":              withDifferentDescription(),
		"DefaultBranchDrift":            withDefaultBranch("main"),
		"AllowMergeCommitDrift":         withAllowMergeCommit(true),
		"AllowSquashMergeDrift":         withAllowSquashMerge(true),
		"AllowRebaseMergeDrift":         withAllowRebaseMerge(true),
		"AllowAutoMergeDrift":           withAllowAutoMerge(true),
		"AllowUpdateBranchDrift":        withAllowUpdateBranch(true),
		"DeleteBranchOnMergeDrift":      withDeleteBranchOnMerge(true),
		"HasIssuesDrift":                withHasIssues(true),
		"HasProjectsDrift":              withHasProjects(true),
		"HasWikiDrift":                  withHasWiki(true),
		"HasDiscussionsDrift":           withHasDiscussions(true),
		"MergeCommitTitleDrift":         withMergeCommitTitle("PR_TITLE"),
		"MergeCommitMessageDrift":       withMergeCommitMessage("PR_BODY"),
		"SquashMergeCommitTitleDrift":   withSquashMergeCommitTitle("PR_TITLE"),
		"SquashMergeCommitMessageDrift": withSquashMergeCommitMessage("PR_BODY"),
	}

	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{github: upToDateClient()}
			got, err := e.Observe(context.Background(), repository(mod))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists {
				t.Errorf("Observe(): want ResourceExists=true, got false")
			}
			if got.ResourceUpToDate {
				t.Errorf("Observe(): want ResourceUpToDate=false (drift), got true")
			}
		})
	}
}

// filterMissingBranchProtectionRules is what stops the controller from
// calling UpdateBranchProtection on branches that don't exist. Asserts:
// rules for missing branches are removed from the map, rules for existing
// branches are kept untouched, and the skipped list is returned sorted so
// the condition message is stable across reconciles.
// filterMissingBranchProtectionRules has two responsibilities:
//
//   - Short-circuit branches already in protectedSet without calling
//     GetBranch (they exist by construction — you can't protect a
//     missing branch).
//   - For the rest, hit Repositories.GetBranch: 404 means the branch
//     is missing and the rule is dropped; 2xx means the branch exists
//     but is unprotected (the rule will be applied by Update); any
//     other error propagates.
//
// The wantCalls assertion pins the short-circuit — if a future change
// stops respecting protectedSet, every reconcile would emit an extra
// GetBranch per already-protected branch, which is the whole API-cost
// reason the protectedSet path exists.
func TestFilterMissingBranchProtectionRules(t *testing.T) {
	ctx := context.Background()
	const org = "test-org"
	const repo = "test-repo"

	type mockBranches struct {
		exists  map[string]bool   // GetBranch returns 200 with name == requested
		missing map[string]bool   // GetBranch returns 404
		renamed map[string]string // GetBranch returns 200 but with a different name (key → value mapping)
		errFor  string            // GetBranch returns a non-404 error for this branch
	}

	cases := map[string]struct {
		rules        map[string]v1alpha1.BranchProtectionRule
		protectedSet map[string]bool
		mock         mockBranches
		wantRules    map[string]v1alpha1.BranchProtectionRule
		wantSkipped  []string
		wantCalls    []string
		wantErr      bool
	}{
		"AllAlreadyProtected_NoGetBranchCalls": {
			rules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"develop": {Branch: "develop"},
			},
			protectedSet: map[string]bool{"main": true, "develop": true},
			wantRules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"develop": {Branch: "develop"},
			},
			wantSkipped: nil,
			wantCalls:   nil,
		},
		"UnprotectedButExisting_CheckedAndKept": {
			rules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"develop": {Branch: "develop"},
			},
			protectedSet: map[string]bool{"main": true},
			mock:         mockBranches{exists: map[string]bool{"develop": true}},
			wantRules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"develop": {Branch: "develop"},
			},
			wantSkipped: nil,
			wantCalls:   []string{"develop"},
		},
		"MissingBranches_Skipped": {
			rules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"release": {Branch: "release"},
				"ghost":   {Branch: "ghost"},
			},
			protectedSet: map[string]bool{"main": true},
			mock: mockBranches{
				missing: map[string]bool{"release": true, "ghost": true},
			},
			wantRules:   map[string]v1alpha1.BranchProtectionRule{"main": {Branch: "main"}},
			wantSkipped: []string{"ghost", "release"},
			wantCalls:   []string{"ghost", "release"},
		},
		"MixedExistingAndMissing": {
			rules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"develop": {Branch: "develop"},
				"ghost":   {Branch: "ghost"},
			},
			protectedSet: map[string]bool{"main": true},
			mock: mockBranches{
				exists:  map[string]bool{"develop": true},
				missing: map[string]bool{"ghost": true},
			},
			wantRules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"develop": {Branch: "develop"},
			},
			wantSkipped: []string{"ghost"},
			wantCalls:   []string{"develop", "ghost"},
		},
		"EmptyInput_NoCallsNoSkipped": {
			rules:        map[string]v1alpha1.BranchProtectionRule{},
			protectedSet: map[string]bool{"main": true},
			wantRules:    map[string]v1alpha1.BranchProtectionRule{},
			wantSkipped:  nil,
			wantCalls:    nil,
		},
		"Non404Error_Propagates": {
			rules:        map[string]v1alpha1.BranchProtectionRule{"feature": {Branch: "feature"}},
			protectedSet: map[string]bool{},
			mock:         mockBranches{errFor: "feature"},
			wantErr:      true,
		},
		// Renamed branches must not be silently accepted as existing — the rule
		// will 404 on the actual UpdateBranchProtection call because PUT doesn't
		// follow the rename redirect. Surface the new name in the skipped entry
		// so the operator knows what to put in the CR.
		"RenamedBranch_DroppedAndNameSurfaced": {
			rules: map[string]v1alpha1.BranchProtectionRule{
				"main":    {Branch: "main"},
				"develop": {Branch: "develop"},
			},
			protectedSet: map[string]bool{},
			mock: mockBranches{
				renamed: map[string]string{"main": "trunk"},
				exists:  map[string]bool{"develop": true},
			},
			wantRules:   map[string]v1alpha1.BranchProtectionRule{"develop": {Branch: "develop"}},
			wantSkipped: []string{"main (was renamed to trunk)"},
			wantCalls:   []string{"develop", "main"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var calls []string
			gh := &ghclient.Client{
				Services: &ghclient.Services{
					Repositories: &fake.MockRepositoriesClient{
						MockGetBranch: func(_ context.Context, _, _, branch string, _ int) (*github.Branch, *github.Response, error) {
							calls = append(calls, branch)
							switch {
							case branch == tc.mock.errFor:
								return nil, &github.Response{Response: &http.Response{StatusCode: http.StatusInternalServerError}}, errors.New("boom")
							case tc.mock.missing[branch]:
								// Real GetBranch returns a bare fmt.Errorf + non-nil resp.StatusCode=404, not a *github.ErrorResponse.
								return nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, fmt.Errorf("unexpected status code: 404 Not Found")
							case tc.mock.renamed[branch] != "":
								// 200 with a different name — GetBranch followed a rename redirect.
								return &github.Branch{Name: github.Ptr(tc.mock.renamed[branch])}, fake.GenerateEmptyResponse(), nil
							case tc.mock.exists[branch]:
								return &github.Branch{Name: github.Ptr(branch)}, fake.GenerateEmptyResponse(), nil
							default:
								return nil, nil, errors.New("unexpected branch in mock: " + branch)
							}
						},
					},
				},
			}

			gotSkipped, err := filterMissingBranchProtectionRules(ctx, gh, org, repo, tc.rules, tc.protectedSet)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantSkipped, gotSkipped); diff != "" {
				t.Errorf("skipped: -want, +got:\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantRules, tc.rules); diff != "" {
				t.Errorf("rules after filter: -want, +got:\n%s", diff)
			}
			sort.Strings(calls)
			if diff := cmp.Diff(tc.wantCalls, calls); diff != "" {
				t.Errorf("GetBranch calls: -want, +got:\n%s", diff)
			}
		})
	}
}

// The single condition names every unapplied part in a fixed order, and is False with no message when nothing is unapplied.
func TestSetBranchProtectionPartialCondition(t *testing.T) {
	alice := branchProtectionActorRef{branch: "main", field: fieldBypassUsers, actor: "alice"}
	someApp := branchProtectionActorRef{branch: "main", field: fieldBypassApps, actor: "some-app"}
	someTeam := branchProtectionActorRef{branch: "main", field: fieldRestrictionTeams, actor: "some-team"}

	cases := map[string]struct {
		report      branchProtectionReport
		wantStatus  corev1.ConditionStatus
		wantReason  xpv2.ConditionReason
		wantMessage string
	}{
		"OnlyMissingBranches": {
			report:      branchProtectionReport{missingBranches: []string{"develop", "release"}},
			wantStatus:  corev1.ConditionTrue,
			wantReason:  "NotFullyApplied",
			wantMessage: "branches do not exist in repo: develop, release",
		},
		"OnlyEnforcedActors": {
			report:      branchProtectionReport{enforcedActors: []branchProtectionActorRef{alice, someTeam}},
			wantStatus:  corev1.ConditionTrue,
			wantReason:  "NotFullyApplied",
			wantMessage: "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice, main/restrictionTeams:some-team",
		},
		"OnlyRememberedApps": {
			report:      branchProtectionReport{rememberedApps: []branchProtectionActorRef{someApp}},
			wantStatus:  corev1.ConditionTrue,
			wantReason:  "NotFullyApplied",
			wantMessage: "actors GitHub did not store on the last push (apps need contents write): main/bypassApps:some-app",
		},
		"OnlyForcePushKept": {
			report:      branchProtectionReport{forcePushKept: []string{"main", "release"}},
			wantStatus:  corev1.ConditionTrue,
			wantReason:  "NotFullyApplied",
			wantMessage: "force pushes stay enabled because a per-actor force-push allowance is set in the GitHub UI, which the REST API cannot change: main, release",
		},
		"AllParts": {
			report: branchProtectionReport{
				missingBranches: []string{"develop"},
				enforcedActors:  []branchProtectionActorRef{alice},
				rememberedApps:  []branchProtectionActorRef{someApp},
				forcePushKept:   []string{"main"},
			},
			wantStatus: corev1.ConditionTrue,
			wantReason: "NotFullyApplied",
			wantMessage: "branches do not exist in repo: develop; " +
				"actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice; " +
				"actors GitHub did not store on the last push (apps need contents write): main/bypassApps:some-app; " +
				"force pushes stay enabled because a per-actor force-push allowance is set in the GitHub UI, which the REST API cannot change: main",
		},
		"NothingUnapplied": {
			report:      branchProtectionReport{},
			wantStatus:  corev1.ConditionFalse,
			wantReason:  "FullyApplied",
			wantMessage: "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := &v1alpha1.Repository{}
			setBranchProtectionPartialCondition(cr, tc.report)

			if len(cr.Status.Conditions) != 1 {
				t.Fatalf("conditions = %d, want 1", len(cr.Status.Conditions))
			}
			c := cr.Status.Conditions[0]
			if c.Type != "BranchProtectionPartial" {
				t.Errorf("Type = %q, want %q", c.Type, "BranchProtectionPartial")
			}
			if c.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", c.Status, tc.wantStatus)
			}
			if c.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", c.Reason, tc.wantReason)
			}
			if c.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", c.Message, tc.wantMessage)
			}
		})
	}
}

// An identical report must not append a duplicate, or every reconcile grows status.conditions.
func TestSetBranchProtectionPartialConditionIdempotent(t *testing.T) {
	cr := &v1alpha1.Repository{}
	setBranchProtectionPartialCondition(cr, branchProtectionReport{missingBranches: []string{"develop"}})
	setBranchProtectionPartialCondition(cr, branchProtectionReport{missingBranches: []string{"develop"}})

	count := 0
	for _, c := range cr.Status.Conditions {
		if c.Type == "BranchProtectionPartial" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("BranchProtectionPartial conditions = %d, want 1", count)
	}
}

// Only a declared-off force push that GitHub stores on is reported, sorted for a stable message.
func TestForcePushKeptBranches(t *testing.T) {
	bpr := func(branch string, afp bool) v1alpha1.BranchProtectionRule {
		return v1alpha1.BranchProtectionRule{Branch: branch, AllowForcePushes: &afp}
	}

	cases := map[string]struct {
		reason string
		cr     map[string]v1alpha1.BranchProtectionRule
		gh     map[string]v1alpha1.BranchProtectionRule
		want   []string
	}{
		"DeclaredOffStoredOn": {
			reason: "CR wants force pushes off but GitHub keeps them on: branch listed",
			cr:     map[string]v1alpha1.BranchProtectionRule{"main": bpr("main", false)},
			gh:     map[string]v1alpha1.BranchProtectionRule{"main": bpr("main", true)},
			want:   []string{"main"},
		},
		"DeclaredOn": {
			reason: "CR explicitly wants force pushes on: not listed",
			cr:     map[string]v1alpha1.BranchProtectionRule{"main": bpr("main", true)},
			gh:     map[string]v1alpha1.BranchProtectionRule{"main": bpr("main", true)},
		},
		"DeclaredOffStoredOff": {
			reason: "GitHub stored what the CR wanted: not listed",
			cr:     map[string]v1alpha1.BranchProtectionRule{"main": bpr("main", false)},
			gh:     map[string]v1alpha1.BranchProtectionRule{"main": bpr("main", false)},
		},
		"BranchAbsentFromStored": {
			reason: "a branch GitHub has no protection for is not a kept force push: not listed",
			cr:     map[string]v1alpha1.BranchProtectionRule{"main": bpr("main", false)},
			gh:     map[string]v1alpha1.BranchProtectionRule{},
		},
		"SortedAcrossBranches": {
			reason: "several kept branches are listed sorted",
			cr:     map[string]v1alpha1.BranchProtectionRule{"release": bpr("release", false), "main": bpr("main", false)},
			gh:     map[string]v1alpha1.BranchProtectionRule{"release": bpr("release", true), "main": bpr("main", true)},
			want:   []string{"main", "release"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := forcePushKeptBranches(tc.cr, tc.gh)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// Every declared actor GitHub did not store is reported as "branch/field:actor", and no stored one is.
func TestDetectUnappliedBranchProtectionActors(t *testing.T) {
	rule := func(bypass, dismissal, restriction []string) map[string]v1alpha1.BranchProtectionRule {
		return map[string]v1alpha1.BranchProtectionRule{
			"main": {
				Branch: "main",
				RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{
					BypassPullRequestAllowances: &v1alpha1.BypassPullRequestAllowancesRequest{Users: bypass},
					DismissalRestrictions:       &v1alpha1.DismissalRestrictionsRequest{Users: &dismissal},
				},
				BranchProtectionRestrictions: &v1alpha1.BranchProtectionRestrictions{Users: restriction},
			},
		}
	}

	cases := map[string]struct {
		reason   string
		declared map[string]v1alpha1.BranchProtectionRule
		stored   map[string]v1alpha1.BranchProtectionRule
		want     []branchProtectionActorRef
	}{
		"DismissalOnly": {
			reason:   "only a dismissal actor dropped: it alone is named",
			declared: rule(nil, []string{"alice"}, nil),
			stored:   rule(nil, nil, nil),
			want:     []branchProtectionActorRef{{branch: "main", field: fieldDismissalUsers, actor: "alice"}},
		},
		"BypassAndRestriction": {
			reason:   "bypass + restriction dropped: both named, entries sorted",
			declared: rule([]string{"bob"}, nil, []string{"carol"}),
			stored:   rule(nil, nil, nil),
			want: []branchProtectionActorRef{
				{branch: "main", field: fieldBypassUsers, actor: "bob"},
				{branch: "main", field: fieldRestrictionUsers, actor: "carol"},
			},
		},
		"AllApplied": {
			reason:   "every declared actor stored: nothing reported",
			declared: rule([]string{"bob"}, []string{"alice"}, nil),
			stored:   rule([]string{"bob"}, []string{"alice"}, nil),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := detectUnappliedBranchProtectionActors(tc.declared, tc.stored)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(branchProtectionActorRef{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func withBypassUser(login string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		a := r.Spec.ForProvider.BranchProtectionRules[0].RequiredPullRequestReviews.BypassPullRequestAllowances
		a.Users = append(a.Users, login)
	}
}

func withDismissalUser(login string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		d := r.Spec.ForProvider.BranchProtectionRules[0].RequiredPullRequestReviews.DismissalRestrictions
		users := append(*d.Users, login)
		d.Users = &users
	}
}

// withSecondBranchRule copies the main rule to branch and adds bypassUser to the copy only.
func withSecondBranchRule(branch, bypassUser string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		rule := r.Spec.ForProvider.BranchProtectionRules[0].DeepCopy()
		rule.Branch = branch
		allowances := rule.RequiredPullRequestReviews.BypassPullRequestAllowances
		allowances.Users = append(allowances.Users, bypassUser)
		r.Spec.ForProvider.BranchProtectionRules = append(r.Spec.ForProvider.BranchProtectionRules, *rule)
	}
}

func withRestrictionTeam(slug, role string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		restr := r.Spec.ForProvider.BranchProtectionRules[0].BranchProtectionRestrictions
		restr.Teams = append(restr.Teams, slug)
		r.Spec.ForProvider.Permissions.Teams = append(r.Spec.ForProvider.Permissions.Teams, v1alpha1.RepositoryTeam{Team: slug, Role: role})
	}
}

// withRestrictionTeamActor declares the team on the rule only, not in the repo's team permissions.
func withRestrictionTeamActor(slug string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		restr := r.Spec.ForProvider.BranchProtectionRules[0].BranchProtectionRestrictions
		restr.Teams = append(restr.Teams, slug)
	}
}

func withBypassTeam(slug string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		a := r.Spec.ForProvider.BranchProtectionRules[0].RequiredPullRequestReviews.BypassPullRequestAllowances
		a.Teams = append(a.Teams, slug)
	}
}

// withOnlyBypassTeam replaces every declared bypass actor with slug.
func withOnlyBypassTeam(slug string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		rpr := r.Spec.ForProvider.BranchProtectionRules[0].RequiredPullRequestReviews
		rpr.BypassPullRequestAllowances = &v1alpha1.BypassPullRequestAllowancesRequest{Teams: []string{slug}}
	}
}

// withOnlyBypassApp replaces every declared bypass actor with slug.
func withOnlyBypassApp(slug string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		rpr := r.Spec.ForProvider.BranchProtectionRules[0].RequiredPullRequestReviews
		rpr.BypassPullRequestAllowances = &v1alpha1.BypassPullRequestAllowancesRequest{Apps: []string{slug}}
	}
}

func withRequiredApprovingReviewCount(n int) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.BranchProtectionRules[0].RequiredPullRequestReviews.RequiredApprovingReviewCount = n
	}
}

// A no-write actor must not count as drift; a with-write one must.
func TestObserveBranchProtectionEnforcedActors(t *testing.T) {
	errBoom := errors.New("boom")

	cases := map[string]struct {
		reason         string
		mods           []repositoryModifier
		repoTeams      []*github.Team
		extraBranch    string
		permission     string
		userPerms      map[string]string
		probeErr       error
		teamPerms      map[string]bool
		teamProbeErr   error
		storedNoBypass bool
		wantUpToDate   bool
		wantErr        error
		wantStatus     corev1.ConditionStatus
		wantMessage    string
		wantUserProbes int
		wantTeamProbes int
	}{
		"UserReadOnlyEnforced": {
			reason:         "a read-only bypass user is dropped by GitHub: up to date, condition names it",
			mods:           []repositoryModifier{withBypassUser("alice")},
			permission:     "read",
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice",
			wantUserProbes: 1,
		},
		"UserWithWriteIsDrift": {
			reason:         "a bypass user with write would be stored: not up to date, not reported as enforced",
			mods:           []repositoryModifier{withBypassUser("alice")},
			permission:     "write",
			wantUpToDate:   false,
			wantStatus:     corev1.ConditionFalse,
			wantUserProbes: 1,
		},
		"UserNotCollaboratorEnforced": {
			reason:         "a user with no repo access (404) is dropped by GitHub: up to date",
			mods:           []repositoryModifier{withBypassUser("alice")},
			probeErr:       fake.Generate404Response(),
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice",
			wantUserProbes: 1,
		},
		"UserProbeErrorPropagates": {
			reason:         "a non-404 probe failure must surface, not be read as enforced",
			mods:           []repositoryModifier{withBypassUser("alice")},
			probeErr:       errBoom,
			wantErr:        errBoom,
			wantUserProbes: 1,
		},
		"UserProbedOncePerObserve": {
			reason:         "the same user missing from two fields is probed once",
			mods:           []repositoryModifier{withBypassUser("alice"), withDismissalUser("alice")},
			permission:     "read",
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice, main/dismissalUsers:alice",
			wantUserProbes: 1,
		},
		"ReadAndWriteUserMissing": {
			reason:         "an enforced user must not mask a with-write user missing from the same rule",
			mods:           []repositoryModifier{withBypassUser("alice"), withBypassUser("bob")},
			userPerms:      map[string]string{"alice": "read", "bob": "write"},
			wantUpToDate:   false,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice",
			wantUserProbes: 2,
		},
		"EnforcedOnOneBranchDoesNotMaskOther": {
			reason:         "an enforced user on main must not mask a with-write user missing on another branch",
			mods:           []repositoryModifier{withSecondBranchRule("develop", "bob"), withBypassUser("alice")},
			extraBranch:    "develop",
			userPerms:      map[string]string{"alice": "read", "bob": "write"},
			wantUpToDate:   false,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice",
			wantUserProbes: 2,
		},
		"TeamPullEnforced": {
			reason:         "a restriction team with pull is dropped by GitHub: up to date, condition names it",
			mods:           []repositoryModifier{withRestrictionTeam("some-team", "pull")},
			repoTeams:      []*github.Team{{Slug: github.Ptr("some-team"), Permission: github.Ptr("pull")}},
			teamPerms:      map[string]bool{"pull": true},
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/restrictionTeams:some-team",
			wantTeamProbes: 1,
		},
		"OnlyBypassTeamEnforced": {
			reason:         "GitHub omits the bypass object once its only team is dropped: up to date",
			mods:           []repositoryModifier{withOnlyBypassTeam("some-team")},
			teamPerms:      map[string]bool{"pull": true},
			storedNoBypass: true,
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassTeams:some-team",
			wantTeamProbes: 1,
		},
		"TeamPushIsDrift": {
			reason:         "a restriction team with push would be stored: not up to date",
			mods:           []repositoryModifier{withRestrictionTeam("some-team", "push")},
			repoTeams:      []*github.Team{{Slug: github.Ptr("some-team"), Permission: github.Ptr("push")}},
			teamPerms:      map[string]bool{"pull": true, "push": true},
			wantUpToDate:   false,
			wantStatus:     corev1.ConditionFalse,
			wantTeamProbes: 1,
		},
		"TeamNotOnRepoEnforced": {
			reason:         "a team with no repo access (404) is dropped by GitHub: up to date",
			mods:           []repositoryModifier{withRestrictionTeamActor("some-team")},
			teamProbeErr:   fake.Generate404Response(),
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/restrictionTeams:some-team",
			wantTeamProbes: 1,
		},
		"TeamProbedOncePerObserve": {
			reason:         "the same team missing from two fields is probed once",
			mods:           []repositoryModifier{withRestrictionTeam("some-team", "pull"), withBypassTeam("some-team")},
			repoTeams:      []*github.Team{{Slug: github.Ptr("some-team"), Permission: github.Ptr("pull")}},
			teamPerms:      map[string]bool{"pull": true},
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassTeams:some-team, main/restrictionTeams:some-team",
			wantTeamProbes: 1,
		},
		"InheritedTeamWriteIsDrift": {
			reason:         "a team inheriting push from its parent is absent from the repo team list but stored by GitHub: not up to date",
			mods:           []repositoryModifier{withRestrictionTeamActor("child-team")},
			teamPerms:      map[string]bool{"pull": true, "push": true},
			wantUpToDate:   false,
			wantStatus:     corev1.ConditionFalse,
			wantTeamProbes: 1,
		},
		"TeamMaintainIsDrift": {
			reason:         "maintain grants write, so GitHub stores a maintain team: not up to date, not reported as enforced",
			mods:           []repositoryModifier{withRestrictionTeamActor("some-team")},
			teamPerms:      map[string]bool{"maintain": true},
			wantUpToDate:   false,
			wantStatus:     corev1.ConditionFalse,
			wantTeamProbes: 1,
		},
		"EnforcedDoesNotMaskOtherDrift": {
			reason:         "stripping an enforced actor must leave unrelated rule drift visible",
			mods:           []repositoryModifier{withBypassUser("alice"), withRequiredApprovingReviewCount(2)},
			permission:     "read",
			wantUpToDate:   false,
			wantStatus:     corev1.ConditionTrue,
			wantMessage:    "actors lack write access on the repo, so GitHub drops them from branch protection: main/bypassUsers:alice",
			wantUserProbes: 1,
		},
		"NoMissingActorsNoProbe": {
			reason:         "every declared actor stored: no permission probe at steady state",
			wantUpToDate:   true,
			wantStatus:     corev1.ConditionFalse,
			wantUserProbes: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			userProbes := 0
			teamProbes := 0
			gh := &ghclient.Client{
				Services: &ghclient.Services{
					Rulesets: upToDateRulesets(),
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							return githubRepository(), nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return append(githubTeams(), tc.repoTeams...), fake.GenerateEmptyResponse(), nil
						},
						MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
							return githubWebhooks(), fake.GenerateEmptyResponse(), nil
						},
						MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
							branches := githubBranches()
							if tc.extraBranch != "" {
								branches = append(branches, &github.Branch{Name: github.Ptr(tc.extraBranch), Protected: github.Ptr(true)})
							}
							return branches, fake.GenerateEmptyResponse(), nil
						},
						MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
							stored := githubProtectedBranch()
							if tc.storedNoBypass {
								stored.RequiredPullRequestReviews.BypassPullRequestAllowances = nil
							}
							return stored, fake.GenerateEmptyResponse(), nil
						},
						MockGetPermissionLevel: func(ctx context.Context, owner, repo, user string) (*github.RepositoryPermissionLevel, *github.Response, error) {
							userProbes++
							if tc.probeErr != nil {
								return nil, fake.GenerateEmptyResponse(), tc.probeErr
							}
							permission := tc.permission
							if p, ok := tc.userPerms[user]; ok {
								permission = p
							}
							return &github.RepositoryPermissionLevel{Permission: github.Ptr(permission)}, fake.GenerateEmptyResponse(), nil
						},
					},
					Teams: &fake.MockTeamsClient{
						MockIsTeamRepoBySlug: func(ctx context.Context, org, slug, owner, repo string) (*github.Repository, *github.Response, error) {
							teamProbes++
							if tc.teamProbeErr != nil {
								return nil, fake.GenerateEmptyResponse(), tc.teamProbeErr
							}
							return &github.Repository{Permissions: repoPermissions(tc.teamPerms)}, fake.GenerateEmptyResponse(), nil
						},
					},
				},
			}

			cr := repository(tc.mods...)
			got, err := (&external{github: gh}).Observe(context.Background(), cr)
			if diff := cmp.Diff(tc.wantErr, err, test.EquateErrors()); diff != "" {
				t.Fatalf("%s: Observe(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if userProbes != tc.wantUserProbes {
				t.Errorf("%s: GetPermissionLevel calls = %d, want %d", tc.reason, userProbes, tc.wantUserProbes)
			}
			if teamProbes != tc.wantTeamProbes {
				t.Errorf("%s: IsTeamRepoBySlug calls = %d, want %d", tc.reason, teamProbes, tc.wantTeamProbes)
			}
			if tc.wantErr != nil {
				return
			}
			if got.ResourceUpToDate != tc.wantUpToDate {
				t.Errorf("%s: ResourceUpToDate = %v, want %v", tc.reason, got.ResourceUpToDate, tc.wantUpToDate)
			}
			cond := cr.GetCondition(typeBranchProtectionPartial)
			if cond.Status != tc.wantStatus || cond.Message != tc.wantMessage {
				t.Errorf("%s: condition = (%v, %q), want (%v, %q)", tc.reason, cond.Status, cond.Message, tc.wantStatus, tc.wantMessage)
			}
		})
	}
}

// Stripping must copy, not mutate, and an emptied list or bypass object must equal GitHub's nil.
func TestWithoutBranchProtectionActors(t *testing.T) {
	bypassRule := func(bp *v1alpha1.BypassPullRequestAllowancesRequest) map[string]v1alpha1.BranchProtectionRule {
		return map[string]v1alpha1.BranchProtectionRule{
			"main": {
				Branch:                     "main",
				RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{BypassPullRequestAllowances: bp},
			},
		}
	}

	cases := map[string]struct {
		reason   string
		declared func() map[string]v1alpha1.BranchProtectionRule
		drop     []branchProtectionActorRef
		want     map[string]v1alpha1.BranchProtectionRule
	}{
		"ListsEmptiedBecomeNil": {
			reason: "emptied lists become nil; kept actors stay",
			declared: func() map[string]v1alpha1.BranchProtectionRule {
				return map[string]v1alpha1.BranchProtectionRule{
					"main": {
						Branch: "main",
						RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{
							BypassPullRequestAllowances: &v1alpha1.BypassPullRequestAllowancesRequest{Users: []string{"alice", "bob"}},
							DismissalRestrictions:       &v1alpha1.DismissalRestrictionsRequest{Users: &[]string{"alice"}},
						},
						BranchProtectionRestrictions: &v1alpha1.BranchProtectionRestrictions{Teams: []string{"some-team"}},
					},
				}
			},
			drop: []branchProtectionActorRef{
				{branch: "main", field: fieldBypassUsers, actor: "alice"},
				{branch: "main", field: fieldDismissalUsers, actor: "alice"},
				{branch: "main", field: fieldRestrictionTeams, actor: "some-team"},
			},
			want: map[string]v1alpha1.BranchProtectionRule{
				"main": {
					Branch: "main",
					RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{
						BypassPullRequestAllowances: &v1alpha1.BypassPullRequestAllowancesRequest{Users: []string{"bob"}},
						DismissalRestrictions:       &v1alpha1.DismissalRestrictionsRequest{},
					},
					BranchProtectionRestrictions: &v1alpha1.BranchProtectionRestrictions{},
				},
			},
		},
		"BypassEmptiedBecomesNil": {
			reason: "a bypass object whose only actor is dropped becomes nil, as GitHub omits it",
			declared: func() map[string]v1alpha1.BranchProtectionRule {
				return bypassRule(&v1alpha1.BypassPullRequestAllowancesRequest{Teams: []string{"some-team"}})
			},
			drop: []branchProtectionActorRef{{branch: "main", field: fieldBypassTeams, actor: "some-team"}},
			want: bypassRule(nil),
		},
		"BypassWithKeptActorStays": {
			reason: "a bypass object that still holds an actor is kept",
			declared: func() map[string]v1alpha1.BranchProtectionRule {
				return bypassRule(&v1alpha1.BypassPullRequestAllowancesRequest{Users: []string{"alice"}, Teams: []string{"some-team"}})
			},
			drop: []branchProtectionActorRef{{branch: "main", field: fieldBypassTeams, actor: "some-team"}},
			want: bypassRule(&v1alpha1.BypassPullRequestAllowancesRequest{Users: []string{"alice"}}),
		},
		"DeclaredEmptyBypassUnchanged": {
			reason: "a bypass object declared empty is left as declared when nothing is dropped",
			declared: func() map[string]v1alpha1.BranchProtectionRule {
				return bypassRule(&v1alpha1.BypassPullRequestAllowancesRequest{})
			},
			want: bypassRule(&v1alpha1.BypassPullRequestAllowancesRequest{}),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := tc.declared()
			got := withoutBranchProtectionActors(in, tc.drop)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.declared(), in); diff != "" {
				t.Errorf("%s: declared rules must not be mutated: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func withBypassApp(slug string) repositoryModifier {
	return func(r *v1alpha1.Repository) {
		a := r.Spec.ForProvider.BranchProtectionRules[0].RequiredPullRequestReviews.BypassPullRequestAllowances
		a.Apps = append(a.Apps, slug)
	}
}

func withoutBranchProtectionRules() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		r.Spec.ForProvider.BranchProtectionRules = nil
	}
}

// declaredRuleHash is the hash Update records for the CR's main rule.
func declaredRuleHash(cr *v1alpha1.Repository) string {
	return ruleHash(getBPRMapFromCr(cr.Spec.ForProvider.BranchProtectionRules)[bpr1branch])
}

// Only declared apps and a disabled force push missing from the push echo are recorded.
func TestUnappliedItems(t *testing.T) {
	rule := func(afp *bool, apps ...string) v1alpha1.BranchProtectionRule {
		return v1alpha1.BranchProtectionRule{
			Branch:           "main",
			AllowForcePushes: afp,
			RequiredPullRequestReviews: &v1alpha1.RequiredPullRequestReviews{
				BypassPullRequestAllowances: &v1alpha1.BypassPullRequestAllowancesRequest{Apps: apps},
			},
		}
	}
	on, off := github.Ptr(true), github.Ptr(false)

	cases := map[string]struct {
		reason   string
		declared v1alpha1.BranchProtectionRule
		echoed   v1alpha1.BranchProtectionRule
		want     []string
	}{
		"AppMissing": {
			reason:   "a declared app absent from the echo was dropped by GitHub",
			declared: rule(off, "some-app"),
			echoed:   rule(off),
			want:     []string{"bypassApps:some-app"},
		},
		"AppPresent": {
			reason:   "a declared app in the echo was applied",
			declared: rule(off, "some-app"),
			echoed:   rule(off, "some-app"),
		},
		"ForcePushKeptOn": {
			reason:   "force pushes declared off but echoed on were not applied",
			declared: rule(off),
			echoed:   rule(on),
			want:     []string{itemAllowForcePushes},
		},
		"ForcePushWantedOn": {
			reason:   "force pushes declared on and echoed on were applied",
			declared: rule(on),
			echoed:   rule(on),
		},
		"ForcePushUnsetKeptOn": {
			reason:   "an unset force push is sent as off, so echoed on was not applied",
			declared: rule(nil),
			echoed:   rule(on),
			want:     []string{itemAllowForcePushes},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := unappliedItems(tc.declared, tc.echoed)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: unappliedItems(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// A record must stop matching once its rule changes, or stale memory would mask drift.
func TestRuleHash(t *testing.T) {
	rule := v1alpha1.BranchProtectionRule{Branch: "main", AllowForcePushes: github.Ptr(false)}
	changed := rule
	changed.AllowForcePushes = github.Ptr(true)

	if ruleHash(rule) != ruleHash(*rule.DeepCopy()) {
		t.Errorf("equal rules must hash equally")
	}
	if ruleHash(rule) == ruleHash(changed) {
		t.Errorf("a changed field must change the hash")
	}
	if got := ruleHash(rule); len(got) != 16 || strings.Trim(got, "0123456789abcdef") != "" {
		t.Errorf("hash = %q, want 16 lowercase hex characters", got)
	}
}

// A current record hides only what GitHub did not apply; stale or orphaned records are dropped.
func TestObserveRememberedUnappliedBranchProtection(t *testing.T) {
	cases := map[string]struct {
		reason          string
		mods            []repositoryModifier
		recordBranch    string
		recordItems     []string
		staleHash       bool
		storedForcePush bool
		storedBypassApp string
		storedNoBypass  bool
		userPermission  string
		wantUpToDate    bool
		wantRecordKept  bool
		wantMessage     string
	}{
		"RememberedAppUpToDate": {
			reason:         "an app recorded as dropped for the unchanged rule is not drift",
			mods:           []repositoryModifier{withBypassApp("some-app")},
			recordBranch:   bpr1branch,
			recordItems:    []string{"bypassApps:some-app"},
			wantUpToDate:   true,
			wantRecordKept: true,
			wantMessage:    "actors GitHub did not store on the last push (apps need contents write): main/bypassApps:some-app",
		},
		"RememberedOnlyBypassAppUpToDate": {
			reason:         "GitHub omits the bypass object once its only app is dropped: up to date",
			mods:           []repositoryModifier{withOnlyBypassApp("some-app")},
			recordBranch:   bpr1branch,
			recordItems:    []string{"bypassApps:some-app"},
			storedNoBypass: true,
			wantUpToDate:   true,
			wantRecordKept: true,
			wantMessage:    "actors GitHub did not store on the last push (apps need contents write): main/bypassApps:some-app",
		},
		"StaleHashIsDrift": {
			reason:       "a record for an older rule must not hide the missing app, and is dropped",
			mods:         []repositoryModifier{withBypassApp("some-app")},
			recordBranch: bpr1branch,
			recordItems:  []string{"bypassApps:some-app"},
			staleHash:    true,
			wantUpToDate: false,
		},
		"UndeclaredBranchDropped": {
			reason:       "a record for a branch no longer declared is dropped",
			recordBranch: "develop",
			recordItems:  []string{"bypassApps:some-app"},
			wantUpToDate: true,
		},
		"NoRulesDeclaredDropped": {
			reason:       "records are dropped once no branch protection is declared",
			mods:         []repositoryModifier{withoutBranchProtectionRules()},
			recordBranch: bpr1branch,
			recordItems:  []string{"bypassApps:some-app"},
			wantUpToDate: true,
		},
		"RememberedForcePushUpToDate": {
			reason:          "force pushes recorded as kept on for the unchanged rule are not drift",
			recordBranch:    bpr1branch,
			recordItems:     []string{itemAllowForcePushes},
			storedForcePush: true,
			wantUpToDate:    true,
			wantRecordKept:  true,
			wantMessage:     "force pushes stay enabled because a per-actor force-push allowance is set in the GitHub UI, which the REST API cannot change: main",
		},
		"RecordedAppNowStored": {
			reason:          "a recorded app GitHub now stores is not stripped, or the rule would differ with nothing to push",
			mods:            []repositoryModifier{withBypassApp("some-app")},
			recordBranch:    bpr1branch,
			recordItems:     []string{"bypassApps:some-app"},
			storedBypassApp: "some-app",
			wantUpToDate:    true,
			wantRecordKept:  true,
		},
		"RecordedUserIsStillDrift": {
			reason:         "a record naming a user is ignored, so a with-write user missing from GitHub stays drift",
			mods:           []repositoryModifier{withBypassUser("alice")},
			recordBranch:   bpr1branch,
			recordItems:    []string{"bypassUsers:alice"},
			userPermission: "write",
			wantUpToDate:   false,
			wantRecordKept: true,
		},
		"AppRecordDoesNotMaskForcePush": {
			reason:          "a record naming only an app leaves force pushes kept on visible as drift",
			mods:            []repositoryModifier{withBypassApp("some-app")},
			recordBranch:    bpr1branch,
			recordItems:     []string{"bypassApps:some-app"},
			storedForcePush: true,
			wantUpToDate:    false,
			wantRecordKept:  true,
			wantMessage:     "actors GitHub did not store on the last push (apps need contents write): main/bypassApps:some-app; force pushes stay enabled because a per-actor force-push allowance is set in the GitHub UI, which the REST API cannot change: main",
		},
		"RecordDoesNotMaskOtherDrift": {
			reason:         "a current record must leave unrelated rule drift visible",
			mods:           []repositoryModifier{withBypassApp("some-app"), withRequiredApprovingReviewCount(2)},
			recordBranch:   bpr1branch,
			recordItems:    []string{"bypassApps:some-app"},
			wantUpToDate:   false,
			wantRecordKept: true,
			wantMessage:    "actors GitHub did not store on the last push (apps need contents write): main/bypassApps:some-app",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stored := githubProtectedBranch()
			stored.AllowForcePushes.Enabled = tc.storedForcePush
			if tc.storedBypassApp != "" {
				allowances := stored.RequiredPullRequestReviews.BypassPullRequestAllowances
				allowances.Apps = append(allowances.Apps, &github.App{Slug: github.Ptr(tc.storedBypassApp)})
			}
			if tc.storedNoBypass {
				stored.RequiredPullRequestReviews.BypassPullRequestAllowances = nil
			}
			gh := &ghclient.Client{
				Services: &ghclient.Services{
					Rulesets: upToDateRulesets(),
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							return githubRepository(), nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return githubTeams(), fake.GenerateEmptyResponse(), nil
						},
						MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
							return githubWebhooks(), fake.GenerateEmptyResponse(), nil
						},
						MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
							return githubBranches(), fake.GenerateEmptyResponse(), nil
						},
						MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
							return stored, fake.GenerateEmptyResponse(), nil
						},
						MockGetPermissionLevel: func(ctx context.Context, owner, repo, user string) (*github.RepositoryPermissionLevel, *github.Response, error) {
							return &github.RepositoryPermissionLevel{Permission: github.Ptr(tc.userPermission)}, fake.GenerateEmptyResponse(), nil
						},
					},
				},
			}

			cr := repository(tc.mods...)
			record := v1alpha1.UnappliedBranchProtection{Branch: tc.recordBranch, RuleHash: declaredRuleHash(cr), Items: tc.recordItems}
			if tc.staleHash {
				record.RuleHash = "0000000000000000"
			}
			cr.Status.AtProvider.UnappliedBranchProtection = []v1alpha1.UnappliedBranchProtection{record}

			got, err := (&external{github: gh}).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("%s: Observe(...): unexpected error: %v", tc.reason, err)
			}
			if got.ResourceUpToDate != tc.wantUpToDate {
				t.Errorf("%s: ResourceUpToDate = %v, want %v", tc.reason, got.ResourceUpToDate, tc.wantUpToDate)
			}
			var wantRecords []v1alpha1.UnappliedBranchProtection
			if tc.wantRecordKept {
				wantRecords = []v1alpha1.UnappliedBranchProtection{record}
			}
			if diff := cmp.Diff(wantRecords, cr.Status.AtProvider.UnappliedBranchProtection, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: status records: -want, +got:\n%s", tc.reason, diff)
			}
			if msg := cr.GetCondition(typeBranchProtectionPartial).Message; msg != tc.wantMessage {
				t.Errorf("%s: condition message = %q, want %q", tc.reason, msg, tc.wantMessage)
			}
		})
	}
}

// A True condition must turn False once its cause is gone, or a stale report lingers.
func TestObserveBranchProtectionPartialClears(t *testing.T) {
	cases := map[string]struct {
		reason      string
		removeRules bool
	}{
		"ForcePushNowApplied": {
			reason: "GitHub now stores force pushes off as declared",
		},
		"RulesNoLongerDeclared": {
			reason:      "the CR no longer declares any branch protection",
			removeRules: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stored := githubProtectedBranch()
			gh := &ghclient.Client{
				Services: &ghclient.Services{
					Rulesets: upToDateRulesets(),
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							return githubRepository(), nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return githubTeams(), fake.GenerateEmptyResponse(), nil
						},
						MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
							return githubWebhooks(), fake.GenerateEmptyResponse(), nil
						},
						MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
							return githubBranches(), fake.GenerateEmptyResponse(), nil
						},
						MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
							return stored, fake.GenerateEmptyResponse(), nil
						},
					},
				},
			}
			cr := repository()

			stored.AllowForcePushes.Enabled = true
			if _, err := (&external{github: gh}).Observe(context.Background(), cr); err != nil {
				t.Fatalf("%s: first Observe(...): unexpected error: %v", tc.reason, err)
			}
			first := cr.GetCondition(typeBranchProtectionPartial)
			if first.Status != corev1.ConditionTrue || first.Reason != reasonNotFullyApplied || first.Message != "force pushes stay enabled because a per-actor force-push allowance is set in the GitHub UI, which the REST API cannot change: main" {
				t.Fatalf("%s: first Observe: condition = (%v, %v, %q), want (True, NotFullyApplied, force pushes message)", tc.reason, first.Status, first.Reason, first.Message)
			}

			if tc.removeRules {
				cr.Spec.ForProvider.BranchProtectionRules = nil
			} else {
				stored.AllowForcePushes.Enabled = false
			}
			if _, err := (&external{github: gh}).Observe(context.Background(), cr); err != nil {
				t.Fatalf("%s: second Observe(...): unexpected error: %v", tc.reason, err)
			}
			second := cr.GetCondition(typeBranchProtectionPartial)
			if second.Status != corev1.ConditionFalse || second.Reason != reasonFullyApplied || second.Message != "" {
				t.Errorf("%s: second Observe: condition = (%v, %v, %q), want (False, FullyApplied, empty)", tc.reason, second.Status, second.Reason, second.Message)
			}
		})
	}
}

// Update must record what the push echo left out, or Observe would see drift forever.
func TestUpdateRecordsUnappliedBranchProtection(t *testing.T) {
	cases := map[string]struct {
		reason      string
		echo        func() *github.Protection
		wantItems   []string
		wantRecords bool
	}{
		"AppDroppedForcePushKept": {
			reason: "an echo without the app and with force pushes on is recorded",
			echo: func() *github.Protection {
				p := githubProtectedBranch()
				p.AllowForcePushes.Enabled = true
				return p
			},
			wantItems:   []string{itemAllowForcePushes, "bypassApps:some-app"},
			wantRecords: true,
		},
		"AllApplied": {
			reason: "an echo with everything applied leaves no record and drops the old one",
			echo: func() *github.Protection {
				p := githubProtectedBranch()
				allowances := p.RequiredPullRequestReviews.BypassPullRequestAllowances
				allowances.Apps = append(allowances.Apps, &github.App{Slug: github.Ptr("some-app")})
				return p
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repoClient := &fake.MockRepositoriesClient{
				MockGet: func(ctx context.Context, owner, r string) (*github.Repository, *github.Response, error) {
					return githubRepository(), fake.GenerateEmptyResponse(), nil
				},
				MockEdit: func(ctx context.Context, owner, r string, rr *github.Repository) (*github.Repository, *github.Response, error) {
					return rr, fake.GenerateEmptyResponse(), nil
				},
				MockListCollaborators: func(ctx context.Context, owner, r string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
					return githubCollaborators(), fake.GenerateEmptyResponse(), nil
				},
				MockListTeams: func(ctx context.Context, owner, r string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
					return githubTeams(), fake.GenerateEmptyResponse(), nil
				},
				MockListBranches: func(ctx context.Context, owner, r string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
					return githubBranches(), fake.GenerateEmptyResponse(), nil
				},
				MockGetBranchProtection: func(ctx context.Context, owner, r, branch string) (*github.Protection, *github.Response, error) {
					return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
				},
				MockUpdateBranchProtection: func(ctx context.Context, owner, r, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error) {
					return tc.echo(), fake.GenerateEmptyResponse(), nil
				},
				MockOptionalSignaturesOnProtectedBranch: func(ctx context.Context, owner, r, branch string) (*github.Response, error) {
					return fake.GenerateEmptyResponse(), nil
				},
				MockReplaceAllTopics: func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
					return topics, fake.GenerateEmptyResponse(), nil
				},
			}

			cr := repository(withBypassApp("some-app"))
			cr.Spec.ForProvider.Webhooks = nil
			cr.Spec.ForProvider.RepositoryRules = nil
			cr.Status.AtProvider.UnappliedBranchProtection = []v1alpha1.UnappliedBranchProtection{
				{Branch: bpr1branch, RuleHash: "0000000000000000", Items: []string{"bypassApps:other-app"}},
			}

			e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repoClient}}}
			if _, err := e.Update(context.Background(), cr); err != nil {
				t.Fatalf("%s: Update(...): unexpected error: %v", tc.reason, err)
			}

			var want []v1alpha1.UnappliedBranchProtection
			if tc.wantRecords {
				want = []v1alpha1.UnappliedBranchProtection{{Branch: bpr1branch, RuleHash: declaredRuleHash(cr), Items: tc.wantItems}}
			}
			if diff := cmp.Diff(want, cr.Status.AtProvider.UnappliedBranchProtection, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s: status records: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// Create must not record: the reconciler re-reads the CR after Create, so the record would be lost anyway.
func TestCreateLeavesUnappliedBranchProtectionUnset(t *testing.T) {
	pushes := 0
	repoClient := &fake.MockRepositoriesClient{
		MockCreate: func(ctx context.Context, owner string, r *github.Repository) (*github.Repository, *github.Response, error) {
			return r, fake.GenerateEmptyResponse(), nil
		},
		MockAddCollaborator: func(ctx context.Context, owner, r, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), nil
		},
		MockListBranches: func(ctx context.Context, owner, r string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
			return githubBranches(), fake.GenerateEmptyResponse(), nil
		},
		MockUpdateBranchProtection: func(ctx context.Context, owner, r, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error) {
			pushes++
			echo := githubProtectedBranch()
			echo.AllowForcePushes.Enabled = true
			return echo, fake.GenerateEmptyResponse(), nil
		},
		MockOptionalSignaturesOnProtectedBranch: func(ctx context.Context, owner, r, branch string) (*github.Response, error) {
			return fake.GenerateEmptyResponse(), nil
		},
		MockReplaceAllTopics: func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
			return topics, fake.GenerateEmptyResponse(), nil
		},
	}
	teamsClient := &fake.MockTeamsClient{
		MockAddTeamRepoBySlug: func(ctx context.Context, org, slug, owner, r string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
			return fake.GenerateEmptyResponse(), nil
		},
	}

	cr := repository(withBypassApp("some-app"))
	cr.Spec.ForProvider.Webhooks = nil
	cr.Spec.ForProvider.RepositoryRules = nil

	e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repoClient, Teams: teamsClient}}}
	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("Create(...): unexpected error: %v", err)
	}
	if pushes != 1 {
		t.Fatalf("UpdateBranchProtection calls = %d, want 1", pushes)
	}
	if got := cr.Status.AtProvider.UnappliedBranchProtection; got != nil {
		t.Errorf("status records = %v, want nil", got)
	}
}

// upToDateRepositories fakes GitHub holding the fixture repository in its declared state, plus the given open invitations.
func upToDateRepositories(invitations []*github.RepositoryInvitation) *fake.MockRepositoriesClient {
	return &fake.MockRepositoriesClient{
		MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
			return githubRepository(), nil, nil
		},
		MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
			return githubCollaborators(), fake.GenerateEmptyResponse(), nil
		},
		MockListInvitations: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.RepositoryInvitation, *github.Response, error) {
			return invitations, fake.GenerateEmptyResponse(), nil
		},
		MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
			return githubTeams(), fake.GenerateEmptyResponse(), nil
		},
		MockListHooks: func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
			return githubWebhooks(), fake.GenerateEmptyResponse(), nil
		},
		MockListBranches: func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
			return githubBranches(), fake.GenerateEmptyResponse(), nil
		},
		MockGetBranchProtection: func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
			return githubProtectedBranch(), fake.GenerateEmptyResponse(), nil
		},
	}
}

// upToDateRulesets fakes GitHub holding the fixture repository's ruleset in its declared state.
func upToDateRulesets() *fake.MockRulesetsClient {
	return &fake.MockRulesetsClient{
		MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return githubRuleset(), fake.GenerateEmptyResponse(), nil
		},
		MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
			return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
		},
	}
}

func clientFor(repos *fake.MockRepositoriesClient, rs *fake.MockRulesetsClient) *ghclient.Client {
	return &ghclient.Client{Services: &ghclient.Services{Repositories: repos, Rulesets: rs}}
}

// Observe publishes the collaborators gauge from the CollaboratorPartial condition: 1 while an invitee is pending, 0 once none is.
func TestObservePublishesCollaboratorsUnreconcilable(t *testing.T) {
	metrics := telemetry.NewForTest()
	gauge := metrics.RepositoryUnreconcilableForTest()

	pending := repository(withExtraUser("pending-user", "pull"))
	pending.Spec.ForProvider.Org = "acme"
	invitations := []*github.RepositoryInvitation{{Invitee: &github.User{Login: github.Ptr("pending-user")}}}
	e := external{github: clientFor(upToDateRepositories(invitations), upToDateRulesets()), metrics: metrics}
	if _, err := e.Observe(context.Background(), pending); err != nil {
		t.Fatalf("Observe(pending): %v", err)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionCollaborators)); got != 1 {
		t.Errorf("unreconcilable{dimension=collaborators} with a pending invitee = %v, want 1", got)
	}

	clean := repository()
	clean.Spec.ForProvider.Org = "acme"
	e = external{github: clientFor(upToDateRepositories(nil), upToDateRulesets()), metrics: metrics}
	if _, err := e.Observe(context.Background(), clean); err != nil {
		t.Fatalf("Observe(clean): %v", err)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionCollaborators)); got != 0 {
		t.Errorf("unreconcilable{dimension=collaborators} without a pending invitee = %v, want 0", got)
	}
}

// A deleted repository's series disappear instead of sticking at their last value.
func TestDeleteForgetsRepositoryUnreconcilable(t *testing.T) {
	metrics := telemetry.NewForTest()
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionCollaborators, true)
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionBranchProtection, false)
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionArchived, false)

	cr := repository()
	cr.Spec.ForProvider.Org = "acme"
	cr.Spec.ForProvider.ForceDelete = github.Ptr(true)
	gh := &ghclient.Client{
		Services: &ghclient.Services{
			Repositories: &fake.MockRepositoriesClient{
				MockDelete: func(ctx context.Context, owner, repo string) (*github.Response, error) {
					return fake.GenerateEmptyResponse(), nil
				},
			},
		},
	}
	e := external{github: gh, metrics: metrics}
	if _, err := e.Delete(context.Background(), cr); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if got := testutil.CollectAndCount(metrics.RepositoryUnreconcilableForTest()); got != 0 {
		t.Errorf("series after Delete = %d, want 0", got)
	}
}

// Observe publishes the branch_protection gauge from the BranchProtectionPartial condition.
func TestObservePublishesBranchProtectionUnreconcilable(t *testing.T) {
	metrics := telemetry.NewForTest()
	gauge := metrics.RepositoryUnreconcilableForTest()

	stored := githubProtectedBranch()
	stored.AllowForcePushes.Enabled = true
	repos := upToDateRepositories(nil)
	repos.MockGetBranchProtection = func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
		return stored, fake.GenerateEmptyResponse(), nil
	}
	cr := repository()
	cr.Spec.ForProvider.Org = "acme"

	e := external{github: clientFor(repos, upToDateRulesets()), metrics: metrics}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionBranchProtection)); got != 1 {
		t.Errorf("unreconcilable{dimension=branch_protection} with force pushes kept = %v, want 1", got)
	}
}

// An archived repository publishes archived=1 and clears the collaborator, branch protection and settings state it no longer reconciles.
func TestObserveArchivedPublishesUnreconcilable(t *testing.T) {
	metrics := telemetry.NewForTest()
	gauge := metrics.RepositoryUnreconcilableForTest()

	repos := upToDateRepositories(nil)
	repos.MockGet = func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
		r := githubRepository()
		r.Archived = github.Ptr(true)
		return r, nil, nil
	}
	cr := repository(withArchived(true))
	cr.Spec.ForProvider.Org = "acme"
	cr.SetConditions(xpv2.Condition{Type: typeCollaboratorPartial, Status: corev1.ConditionTrue, Reason: reasonPendingInvitation})
	cr.SetConditions(xpv2.Condition{Type: typeBranchProtectionPartial, Status: corev1.ConditionTrue, Reason: reasonNotFullyApplied})
	cr.Status.AtProvider.UnappliedBranchProtection = []v1alpha1.UnappliedBranchProtection{{Branch: "main", Items: []string{"allowForcePushes"}}}
	cr.SetConditions(xpv2.Condition{Type: typeSettingsPartial, Status: corev1.ConditionTrue, Reason: reasonNotFullyApplied})
	cr.Status.AtProvider.UnappliedSettings = []v1alpha1.UnappliedSetting{{Field: "hasWiki", Declared: "true"}}

	e := external{github: clientFor(repos, upToDateRulesets()), metrics: metrics}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionArchived)); got != 1 {
		t.Errorf("unreconcilable{dimension=archived} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionCollaborators)); got != 0 {
		t.Errorf("unreconcilable{dimension=collaborators} = %v, want 0", got)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionBranchProtection)); got != 0 {
		t.Errorf("unreconcilable{dimension=branch_protection} = %v, want 0", got)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionSettings)); got != 0 {
		t.Errorf("unreconcilable{dimension=settings} = %v, want 0", got)
	}
	if got := cr.GetCondition(typeCollaboratorPartial).Status; got != corev1.ConditionFalse {
		t.Errorf("CollaboratorPartial = %v, want False", got)
	}
	if got := cr.GetCondition(typeBranchProtectionPartial).Status; got != corev1.ConditionFalse {
		t.Errorf("BranchProtectionPartial = %v, want False", got)
	}
	if cr.Status.AtProvider.UnappliedBranchProtection != nil {
		t.Errorf("UnappliedBranchProtection = %v, want nil", cr.Status.AtProvider.UnappliedBranchProtection)
	}
	if got := cr.GetCondition(typeSettingsPartial).Status; got != corev1.ConditionFalse {
		t.Errorf("SettingsPartial = %v, want False", got)
	}
	if cr.Status.AtProvider.UnappliedSettings != nil {
		t.Errorf("UnappliedSettings = %v, want nil", cr.Status.AtProvider.UnappliedSettings)
	}
}

// A repository GitHub no longer has leaves no series behind.
func TestObserveMissingRepositoryForgetsUnreconcilable(t *testing.T) {
	metrics := telemetry.NewForTest()
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionCollaborators, true)
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionBranchProtection, false)
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionArchived, false)

	repos := &fake.MockRepositoriesClient{
		MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
			return nil, nil, fake.Generate404Response()
		},
	}
	cr := repository()
	cr.Spec.ForProvider.Org = "acme"

	e := external{github: clientFor(repos, upToDateRulesets()), metrics: metrics}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if got := testutil.CollectAndCount(metrics.RepositoryUnreconcilableForTest()); got != 0 {
		t.Errorf("series after a 404 = %d, want 0", got)
	}
}

// fakeFinalizer stands in for the runtime's API finalizer and records whether RemoveFinalizer was called.
type fakeFinalizer struct {
	removeErr error
	removed   bool
}

func (f *fakeFinalizer) AddFinalizer(ctx context.Context, obj resource.Object) error {
	return nil
}

func (f *fakeFinalizer) RemoveFinalizer(ctx context.Context, obj resource.Object) error {
	f.removed = true
	return f.removeErr
}

func seededUnreconcilable() *telemetry.RateLimitMetrics {
	metrics := telemetry.NewForTest()
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionCollaborators, true)
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionBranchProtection, false)
	metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionArchived, false)
	return metrics
}

// Removing the finalizer forgets the repository's series, so an orphaned repository stops firing alerts.
func TestForgettingFinalizerForgetsOnRemove(t *testing.T) {
	metrics := seededUnreconcilable()
	cr := repository()
	cr.Spec.ForProvider.Org = "acme"
	f := &forgettingFinalizer{inner: &fakeFinalizer{}, metrics: metrics}

	if err := f.RemoveFinalizer(context.Background(), cr); err != nil {
		t.Fatalf("RemoveFinalizer: %v", err)
	}

	if got := testutil.CollectAndCount(metrics.RepositoryUnreconcilableForTest()); got != 0 {
		t.Errorf("series after RemoveFinalizer = %d, want 0", got)
	}
}

// A failed finalizer removal keeps the series, because the resource is still managed.
func TestForgettingFinalizerKeepsSeriesOnRemoveError(t *testing.T) {
	metrics := seededUnreconcilable()
	cr := repository()
	cr.Spec.ForProvider.Org = "acme"
	removeErr := errors.New("conflict")
	f := &forgettingFinalizer{inner: &fakeFinalizer{removeErr: removeErr}, metrics: metrics}

	err := f.RemoveFinalizer(context.Background(), cr)

	if !errors.Is(err, removeErr) {
		t.Errorf("RemoveFinalizer error = %v, want %v", err, removeErr)
	}
	if got := testutil.CollectAndCount(metrics.RepositoryUnreconcilableForTest()); got != 3 {
		t.Errorf("series after failed RemoveFinalizer = %d, want 3", got)
	}
}

// Objects other than a Repository are only delegated.
func TestForgettingFinalizerDelegatesNonRepository(t *testing.T) {
	metrics := seededUnreconcilable()
	inner := &fakeFinalizer{}
	f := &forgettingFinalizer{inner: inner, metrics: metrics}

	if err := f.RemoveFinalizer(context.Background(), &v1alpha1.Team{}); err != nil {
		t.Fatalf("RemoveFinalizer: %v", err)
	}

	if !inner.removed {
		t.Errorf("inner RemoveFinalizer was not called")
	}
	if got := testutil.CollectAndCount(metrics.RepositoryUnreconcilableForTest()); got != 3 {
		t.Errorf("series after RemoveFinalizer on a Team = %d, want 3", got)
	}
}

// A setting is recorded only when the push set it and GitHub echoed another value.
func TestUnappliedSettings(t *testing.T) {
	cases := map[string]struct {
		req    *github.Repository
		echoed *github.Repository
		want   []v1alpha1.UnappliedSetting
	}{
		"WikiRefused": {
			req:    &github.Repository{HasWiki: github.Ptr(true)},
			echoed: &github.Repository{HasWiki: github.Ptr(false)},
			want:   []v1alpha1.UnappliedSetting{{Field: "hasWiki", Declared: "true"}},
		},
		"AllApplied": {
			req:    &github.Repository{HasWiki: github.Ptr(true), HasIssues: github.Ptr(false), Description: github.Ptr("widgets")},
			echoed: &github.Repository{HasWiki: github.Ptr(true), HasIssues: github.Ptr(false), Description: github.Ptr("widgets")},
		},
		"TwoRefusedSorted": {
			req:    &github.Repository{HasWiki: github.Ptr(true), HasProjects: github.Ptr(true)},
			echoed: &github.Repository{HasWiki: github.Ptr(false), HasProjects: github.Ptr(false)},
			want: []v1alpha1.UnappliedSetting{
				{Field: "hasProjects", Declared: "true"},
				{Field: "hasWiki", Declared: "true"},
			},
		},
		"UnsetNotRecorded": {
			req:    &github.Repository{},
			echoed: &github.Repository{HasWiki: github.Ptr(true)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := unappliedSettings(tc.req, tc.echoed)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unappliedSettings(...): -want, +got:\n%s", diff)
			}
		})
	}
}

// A recorded refusal masks only its own field, and only while the field is still declared with the refused value.
func TestObserveRememberedUnappliedSettings(t *testing.T) {
	refusedWiki := []v1alpha1.UnappliedSetting{{Field: "hasWiki", Declared: "true"}}

	cases := map[string]struct {
		records       []v1alpha1.UnappliedSetting
		mods          []repositoryModifier
		wantUpToDate  bool
		wantRecords   []v1alpha1.UnappliedSetting
		wantCondition corev1.ConditionStatus
		wantMessage   string
		wantGauge     float64
	}{
		"RefusalRemembered": {
			records:       refusedWiki,
			mods:          []repositoryModifier{withHasWiki(true)},
			wantUpToDate:  true,
			wantRecords:   refusedWiki,
			wantCondition: corev1.ConditionTrue,
			wantMessage:   "settings GitHub did not apply on the last push (not available on this plan or repository type): hasWiki=true",
			wantGauge:     1,
		},
		"SpecChanged": {
			records:       refusedWiki,
			mods:          []repositoryModifier{withHasWiki(false)},
			wantUpToDate:  true,
			wantCondition: corev1.ConditionFalse,
		},
		"OtherFieldDrifts": {
			records:       refusedWiki,
			mods:          []repositoryModifier{withHasWiki(true), withHasIssues(true)},
			wantUpToDate:  false,
			wantRecords:   refusedWiki,
			wantCondition: corev1.ConditionTrue,
			wantMessage:   "settings GitHub did not apply on the last push (not available on this plan or repository type): hasWiki=true",
			wantGauge:     1,
		},
		"Unmanaged": {
			records:       refusedWiki,
			wantUpToDate:  true,
			wantCondition: corev1.ConditionFalse,
		},
		"NoRecordDrift": {
			mods:          []repositoryModifier{withHasWiki(true)},
			wantUpToDate:  false,
			wantCondition: corev1.ConditionFalse,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics := telemetry.NewForTest()
			cr := repository(tc.mods...)
			cr.Spec.ForProvider.Org = "acme"
			cr.Status.AtProvider.UnappliedSettings = tc.records

			e := external{github: clientFor(upToDateRepositories(nil), upToDateRulesets()), metrics: metrics}
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}

			if got.ResourceUpToDate != tc.wantUpToDate {
				t.Errorf("ResourceUpToDate = %v, want %v", got.ResourceUpToDate, tc.wantUpToDate)
			}
			if diff := cmp.Diff(tc.wantRecords, cr.Status.AtProvider.UnappliedSettings, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("status records: -want, +got:\n%s", diff)
			}
			condition := cr.GetCondition(typeSettingsPartial)
			if condition.Status != tc.wantCondition {
				t.Errorf("SettingsPartial = %v, want %v", condition.Status, tc.wantCondition)
			}
			if condition.Message != tc.wantMessage {
				t.Errorf("SettingsPartial message = %q, want %q", condition.Message, tc.wantMessage)
			}
			gauge := metrics.RepositoryUnreconcilableForTest()
			if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionSettings)); got != tc.wantGauge {
				t.Errorf("unreconcilable{dimension=settings} = %v, want %v", got, tc.wantGauge)
			}
		})
	}
}

// Update replaces the settings record with what the Edit echo left out, so a refusal is remembered and a stale one dropped.
func TestUpdateRecordsUnappliedSettings(t *testing.T) {
	cases := map[string]struct {
		echo func(req *github.Repository) *github.Repository
		want []v1alpha1.UnappliedSetting
	}{
		"WikiRefused": {
			echo: func(req *github.Repository) *github.Repository {
				echoed := *req
				echoed.HasWiki = github.Ptr(false)
				return &echoed
			},
			want: []v1alpha1.UnappliedSetting{{Field: "hasWiki", Declared: "true"}},
		},
		"AllApplied": {
			echo: func(req *github.Repository) *github.Repository {
				return req
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repoClient := &fake.MockRepositoriesClient{
				MockGet: func(ctx context.Context, owner, r string) (*github.Repository, *github.Response, error) {
					return githubRepository(), fake.GenerateEmptyResponse(), nil
				},
				MockEdit: func(ctx context.Context, owner, r string, rr *github.Repository) (*github.Repository, *github.Response, error) {
					return tc.echo(rr), fake.GenerateEmptyResponse(), nil
				},
				MockListCollaborators: func(ctx context.Context, owner, r string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
					return nil, fake.GenerateEmptyResponse(), nil
				},
				MockListTeams: func(ctx context.Context, owner, r string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
					return nil, fake.GenerateEmptyResponse(), nil
				},
			}

			cr := &v1alpha1.Repository{}
			meta.SetExternalName(cr, repo)
			cr.Spec.ForProvider.HasWiki = github.Ptr(true)
			cr.Status.AtProvider.UnappliedSettings = []v1alpha1.UnappliedSetting{{Field: "hasProjects", Declared: "true"}}

			e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repoClient}}}
			if _, err := e.Update(context.Background(), cr); err != nil {
				t.Fatalf("Update: %v", err)
			}

			if diff := cmp.Diff(tc.want, cr.Status.AtProvider.UnappliedSettings, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("status records: -want, +got:\n%s", diff)
			}
		})
	}
}

func flipped(b *bool) *bool {
	value := !*b
	return &value
}

func changed(s *string) *string {
	value := *s + "-changed"
	return &value
}

// settingChanges alters one pushed setting each, the way GitHub keeping its own value would.
var settingChanges = []struct {
	field  string
	change func(r *github.Repository)
}{
	{"description", func(r *github.Repository) { r.Description = changed(r.Description) }},
	{"private", func(r *github.Repository) { r.Private = flipped(r.Private) }},
	{"isTemplate", func(r *github.Repository) { r.IsTemplate = flipped(r.IsTemplate) }},
	{"defaultBranch", func(r *github.Repository) { r.DefaultBranch = changed(r.DefaultBranch) }},
	{"allowMergeCommit", func(r *github.Repository) { r.AllowMergeCommit = flipped(r.AllowMergeCommit) }},
	{"allowSquashMerge", func(r *github.Repository) { r.AllowSquashMerge = flipped(r.AllowSquashMerge) }},
	{"allowRebaseMerge", func(r *github.Repository) { r.AllowRebaseMerge = flipped(r.AllowRebaseMerge) }},
	{"allowAutoMerge", func(r *github.Repository) { r.AllowAutoMerge = flipped(r.AllowAutoMerge) }},
	{"allowUpdateBranch", func(r *github.Repository) { r.AllowUpdateBranch = flipped(r.AllowUpdateBranch) }},
	{"deleteBranchOnMerge", func(r *github.Repository) { r.DeleteBranchOnMerge = flipped(r.DeleteBranchOnMerge) }},
	{"hasIssues", func(r *github.Repository) { r.HasIssues = flipped(r.HasIssues) }},
	{"hasProjects", func(r *github.Repository) { r.HasProjects = flipped(r.HasProjects) }},
	{"hasWiki", func(r *github.Repository) { r.HasWiki = flipped(r.HasWiki) }},
	{"hasDiscussions", func(r *github.Repository) { r.HasDiscussions = flipped(r.HasDiscussions) }},
	{"mergeCommitTitle", func(r *github.Repository) { r.MergeCommitTitle = changed(r.MergeCommitTitle) }},
	{"mergeCommitMessage", func(r *github.Repository) { r.MergeCommitMessage = changed(r.MergeCommitMessage) }},
	{"squashMergeCommitTitle", func(r *github.Repository) { r.SquashMergeCommitTitle = changed(r.SquashMergeCommitTitle) }},
	{"squashMergeCommitMessage", func(r *github.Repository) { r.SquashMergeCommitMessage = changed(r.SquashMergeCommitMessage) }},
}

// withAllSettingsDeclared declares every pushed setting with a value the fixture GitHub repository does not hold.
func withAllSettingsDeclared() repositoryModifier {
	return func(r *v1alpha1.Repository) {
		fp := &r.Spec.ForProvider
		fp.Description = "widgets"
		fp.Private = github.Ptr(false)
		fp.IsTemplate = github.Ptr(true)
		fp.DefaultBranch = github.Ptr("trunk")
		fp.AllowMergeCommit = github.Ptr(true)
		fp.AllowSquashMerge = github.Ptr(true)
		fp.AllowRebaseMerge = github.Ptr(true)
		fp.AllowAutoMerge = github.Ptr(true)
		fp.AllowUpdateBranch = github.Ptr(true)
		fp.DeleteBranchOnMerge = github.Ptr(true)
		fp.HasIssues = github.Ptr(true)
		fp.HasProjects = github.Ptr(true)
		fp.HasWiki = github.Ptr(true)
		fp.HasDiscussions = github.Ptr(true)
		fp.MergeCommitTitle = github.Ptr("PR_TITLE")
		fp.MergeCommitMessage = github.Ptr("PR_BODY")
		fp.SquashMergeCommitTitle = github.Ptr("PR_TITLE")
		fp.SquashMergeCommitMessage = github.Ptr("COMMIT_MESSAGES")
	}
}

// allSettingsRecords is what Update records when GitHub keeps every setting withAllSettingsDeclared pushes.
func allSettingsRecords() []v1alpha1.UnappliedSetting {
	return []v1alpha1.UnappliedSetting{
		{Field: "allowAutoMerge", Declared: "true"},
		{Field: "allowMergeCommit", Declared: "true"},
		{Field: "allowRebaseMerge", Declared: "true"},
		{Field: "allowSquashMerge", Declared: "true"},
		{Field: "allowUpdateBranch", Declared: "true"},
		{Field: "defaultBranch", Declared: "trunk"},
		{Field: "deleteBranchOnMerge", Declared: "true"},
		{Field: "description", Declared: "widgets"},
		{Field: "hasDiscussions", Declared: "true"},
		{Field: "hasIssues", Declared: "true"},
		{Field: "hasProjects", Declared: "true"},
		{Field: "hasWiki", Declared: "true"},
		{Field: "isTemplate", Declared: "true"},
		{Field: "mergeCommitMessage", Declared: "PR_BODY"},
		{Field: "mergeCommitTitle", Declared: "PR_TITLE"},
		{Field: "private", Declared: "false"},
		{Field: "squashMergeCommitMessage", Declared: "COMMIT_MESSAGES"},
		{Field: "squashMergeCommitTitle", Declared: "PR_TITLE"},
	}
}

func withoutRecord(records []v1alpha1.UnappliedSetting, field string) []v1alpha1.UnappliedSetting {
	var kept []v1alpha1.UnappliedSetting
	for _, record := range records {
		if record.Field != field {
			kept = append(kept, record)
		}
	}
	return kept
}

// settingsOnlyRepository declares every pushed setting and nothing Observe reads through other API calls.
func settingsOnlyRepository() *v1alpha1.Repository {
	cr := repository(withAllSettingsDeclared())
	cr.Spec.ForProvider.Webhooks = nil
	cr.Spec.ForProvider.BranchProtectionRules = nil
	cr.Spec.ForProvider.RepositoryRules = nil
	return cr
}

// What Update records from a refusing echo is exactly what Observe then stops comparing, field by field.
func TestSettingsRoundTrip(t *testing.T) {
	var held *github.Repository
	repos := upToDateRepositories(nil)
	repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
		echoed := *req
		for _, c := range settingChanges {
			c.change(&echoed)
		}
		echoed.Topics = githubRepository().Topics
		held = &echoed
		return &echoed, fake.GenerateEmptyResponse(), nil
	}
	repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
		return topics, fake.GenerateEmptyResponse(), nil
	}
	cr := settingsOnlyRepository()
	e := external{github: clientFor(repos, upToDateRulesets())}

	if _, err := e.Update(context.Background(), cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if diff := cmp.Diff(allSettingsRecords(), cr.Status.AtProvider.UnappliedSettings); diff != "" {
		t.Fatalf("records after Update: -want, +got:\n%s", diff)
	}

	repos.MockGet = func(ctx context.Context, owner, r string) (*github.Repository, *github.Response, error) {
		return held, fake.GenerateEmptyResponse(), nil
	}
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("Observe with every setting refused: ResourceUpToDate = false, want true")
	}
	if diff := cmp.Diff(allSettingsRecords(), cr.Status.AtProvider.UnappliedSettings); diff != "" {
		t.Errorf("records after Observe: -want, +got:\n%s", diff)
	}

	// GitHub already holds the newly declared hasWiki, so only the record goes.
	cr.Spec.ForProvider.HasWiki = flipped(cr.Spec.ForProvider.HasWiki)
	got, err = e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe after hasWiki change: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("Observe after hasWiki change: ResourceUpToDate = false, want true")
	}
	wantRecords := withoutRecord(allSettingsRecords(), "hasWiki")
	if diff := cmp.Diff(wantRecords, cr.Status.AtProvider.UnappliedSettings); diff != "" {
		t.Errorf("records after hasWiki change: -want, +got:\n%s", diff)
	}

	cr.Spec.ForProvider.DefaultBranch = github.Ptr("release")
	got, err = e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe after defaultBranch change: %v", err)
	}
	if got.ResourceUpToDate {
		t.Errorf("Observe after defaultBranch change: ResourceUpToDate = true, want false")
	}
	wantRecords = withoutRecord(wantRecords, "defaultBranch")
	if diff := cmp.Diff(wantRecords, cr.Status.AtProvider.UnappliedSettings); diff != "" {
		t.Errorf("records after defaultBranch change: -want, +got:\n%s", diff)
	}
}

// Each setting is compared against its own echoed field: refusing one records that one only.
func TestUpdateRecordsOnlyTheRefusedSetting(t *testing.T) {
	for _, c := range settingChanges {
		t.Run(c.field, func(t *testing.T) {
			repos := upToDateRepositories(nil)
			repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
				echoed := *req
				c.change(&echoed)
				return &echoed, fake.GenerateEmptyResponse(), nil
			}
			repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
				return topics, fake.GenerateEmptyResponse(), nil
			}
			cr := settingsOnlyRepository()
			e := external{github: clientFor(repos, upToDateRulesets())}

			if _, err := e.Update(context.Background(), cr); err != nil {
				t.Fatalf("Update: %v", err)
			}

			var want []v1alpha1.UnappliedSetting
			for _, record := range allSettingsRecords() {
				if record.Field == c.field {
					want = append(want, record)
				}
			}
			if diff := cmp.Diff(want, cr.Status.AtProvider.UnappliedSettings); diff != "" {
				t.Errorf("records: -want, +got:\n%s", diff)
			}
		})
	}
}

// Unset settings are neither pushed nor compared; unset visibility means private, and a fork's visibility is left alone.
func TestEditRequestSettings(t *testing.T) {
	cases := map[string]struct {
		fork bool
		want []pushedSetting
	}{
		"NotFork": {
			want: []pushedSetting{
				{field: "description", requested: "", echoed: "desc"},
				{field: "private", requested: "true", echoed: "true"},
				{field: "isTemplate", requested: "false", echoed: "false"},
			},
		},
		"Fork": {
			fork: true,
			want: []pushedSetting{
				{field: "description", requested: "", echoed: "desc"},
				{field: "isTemplate", requested: "false", echoed: "false"},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gh := githubRepository()
			gh.Fork = github.Ptr(tc.fork)
			cr := &v1alpha1.Repository{}

			got := pushedSettings(editRequest(cr, gh, repo), gh)

			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(pushedSetting{})); diff != "" {
				t.Errorf("pushedSettings(editRequest(...)): -want, +got:\n%s", diff)
			}
		})
	}
}

// Observe lists the repository's own rulesets, with includes_parents=false.
func TestObserveListsOnlyOwnRulesets(t *testing.T) {
	var got *github.RepositoryListRulesetsOptions
	rs := upToDateRulesets()
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		got = opts
		return githubRuleset(), fake.GenerateEmptyResponse(), nil
	}
	var getIncludesParents bool
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		getIncludesParents = getIncludesParents || includesParents
		return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
	}

	if _, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository()); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.IncludesParents == nil || *got.IncludesParents {
		t.Errorf("GetAllRulesets IncludesParents = %v, want false sent explicitly", got.IncludesParents)
	}
	if getIncludesParents {
		t.Errorf("GetRuleset includesParents = true, want false like the list")
	}
}

// Rulesets are listed across all pages, each request with includes_parents=false.
func TestGetRepositoryRulesListsAllPages(t *testing.T) {
	pages := [][]*rulesets.Ruleset{
		{{Name: "a"}, {Name: "b"}},
		{{Name: "c"}},
		{{Name: "d"}, {Name: "e"}},
	}
	var requested []int
	rs := &fake.MockRulesetsClient{
		MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			requested = append(requested, opts.Page)
			if len(requested) > len(pages) {
				t.Errorf("GetAllRulesets called %d times, want %d", len(requested), len(pages))
				return nil, fake.GenerateEmptyResponse(), errors.New("too many calls")
			}
			if opts.IncludesParents == nil || *opts.IncludesParents || opts.PerPage != 100 {
				t.Errorf("GetAllRulesets page %d: IncludesParents = %v, PerPage = %d; want false, 100", opts.Page, opts.IncludesParents, opts.PerPage)
			}
			i := max(opts.Page, 1) - 1
			resp := fake.GenerateEmptyResponse()
			if i+1 < len(pages) {
				resp.NextPage = i + 2
			}
			return pages[i], resp, nil
		},
	}

	got, err := getRepositoryRules(context.Background(), clientFor(nil, rs), "org", "repo")
	if err != nil {
		t.Fatalf("getRepositoryRules: %v", err)
	}
	names := make([]string, 0, len(got))
	for _, r := range got {
		names = append(names, r.Name)
	}
	if diff := cmp.Diff([]string{"a", "b", "c", "d", "e"}, names); diff != "" {
		t.Errorf("getRepositoryRules names: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff([]int{0, 2, 3}, requested); diff != "" {
		t.Errorf("GetAllRulesets pages requested: -want, +got:\n%s", diff)
	}
}

// The ruleset sent on create and update holds every declared rule. "rules" and
// "bypass_actors" are always sent, as [] when empty, so an update can clear them.
// A push ruleset is sent "conditions": {}.
func TestCrRepoRulesToRulesConfig(t *testing.T) {
	ruleset := func(m ...func(*v1alpha1.RepositoryRuleset)) v1alpha1.RepositoryRuleset {
		r := v1alpha1.RepositoryRuleset{
			Name:        rr1name,
			Target:      github.Ptr(rr1target),
			Enforcement: github.Ptr(rr1enforcement),
			Conditions:  &v1alpha1.RulesetConditions{RefName: &v1alpha1.RulesetRefName{Include: []string{"refs/heads/main"}, Exclude: []string{}}},
			Rules: &v1alpha1.Rules{
				Creation:              github.Ptr(false),
				Deletion:              github.Ptr(false),
				Update:                github.Ptr(false),
				RequiredLinearHistory: github.Ptr(false),
				RequiredSignatures:    github.Ptr(false),
				NonFastForward:        github.Ptr(false),
			},
		}
		for _, f := range m {
			f(&r)
		}
		return r
	}
	allRules := func(r *v1alpha1.RepositoryRuleset) {
		r.BypassActors = []*v1alpha1.RulesetByPassActors{{ActorID: &rr1actorId, ActorType: github.Ptr(rr1actorType), BypassMode: github.Ptr(rr1bypassMode)}}
		r.Rules = &v1alpha1.Rules{
			Creation:              github.Ptr(true),
			Deletion:              github.Ptr(true),
			Update:                github.Ptr(true),
			RequiredLinearHistory: github.Ptr(true),
			RequiredSignatures:    github.Ptr(true),
			NonFastForward:        github.Ptr(true),
			RequiredDeployments:   &v1alpha1.RulesRequiredDeployments{Environments: []string{"prod"}},
			PullRequest: &v1alpha1.RulesPullRequest{
				DismissStaleReviewsOnPush:      github.Ptr(true),
				RequireCodeOwnerReview:         github.Ptr(true),
				RequireLastPushApproval:        github.Ptr(true),
				RequiredApprovingReviewCount:   github.Ptr(1),
				RequiredReviewThreadResolution: github.Ptr(true),
			},
			RequiredStatusChecks: &v1alpha1.RulesRequiredStatusChecks{
				StrictRequiredStatusChecksPolicy: github.Ptr(true),
				RequiredStatusChecks:             []*v1alpha1.RulesRequiredStatusChecksParameters{{Context: "ci"}},
			},
		}
	}

	const mainOnly = `{"ref_name":{"include":["refs/heads/main"],"exclude":[]}}`
	cases := map[string]struct {
		reason         string
		rule           v1alpha1.RepositoryRuleset
		wantRuleTypes  []string
		wantBypass     int
		wantConditions string // exact JSON of the "conditions" key; "": omitted
	}{
		"NoRuleEnabled": {
			reason:         "a ruleset with every rule off and nil bypass actors must send both as [], so an update clears them",
			rule:           ruleset(),
			wantRuleTypes:  []string{},
			wantConditions: mainOnly,
		},
		"NoRulesBlock": {
			reason:         "a ruleset declaring no rules at all is one with every rule off",
			rule:           ruleset(func(r *v1alpha1.RepositoryRuleset) { r.Rules = nil }),
			wantRuleTypes:  []string{},
			wantConditions: mainOnly,
		},
		"EmptyBypassActors": {
			reason:         "an empty bypass actor list must be sent as [], so an update clears the actors",
			rule:           ruleset(func(r *v1alpha1.RepositoryRuleset) { r.BypassActors = []*v1alpha1.RulesetByPassActors{} }),
			wantRuleTypes:  []string{},
			wantConditions: mainOnly,
		},
		"UpdateOnly": {
			reason:         "an update rule on its own must be sent",
			rule:           ruleset(func(r *v1alpha1.RepositoryRuleset) { r.Rules.Update = github.Ptr(true) }),
			wantRuleTypes:  []string{"update"},
			wantConditions: mainOnly,
		},
		"AllRules": {
			reason:         "every modelled rule and the bypass actors must be sent",
			rule:           ruleset(allRules),
			wantRuleTypes:  []string{"creation", "deletion", "non_fast_forward", "pull_request", "required_deployments", "required_linear_history", "required_signatures", "required_status_checks", "update"},
			wantBypass:     1,
			wantConditions: mainOnly,
		},
		"Push": {
			reason:         "a push ruleset must be sent with empty conditions, never a ref_name, even when the CR declares a refName",
			rule:           ruleset(func(r *v1alpha1.RepositoryRuleset) { r.Target = github.Ptr("push") }),
			wantRuleTypes:  []string{},
			wantConditions: `{}`,
		},
		"ConditionsWithoutRefName": {
			reason:         "conditions without a refName must not panic and send empty include and exclude lists, which GitHub requires",
			rule:           ruleset(func(r *v1alpha1.RepositoryRuleset) { r.Conditions = &v1alpha1.RulesetConditions{} }),
			wantRuleTypes:  []string{},
			wantConditions: `{"ref_name":{"include":[],"exclude":[]}}`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rs, err := crRepoRulesToRulesConfig(crRulesets(t, []v1alpha1.RepositoryRuleset{tc.rule})[tc.rule.Name])
			if err != nil {
				t.Fatalf("crRepoRulesToRulesConfig: %v", err)
			}
			body, err := json.Marshal(rs)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			var actors []json.RawMessage
			raw, ok := fields["bypass_actors"]
			if !ok {
				t.Errorf("%s\nbypass_actors omitted, want it sent; body %s", tc.reason, body)
			} else if err := json.Unmarshal(raw, &actors); err != nil || actors == nil || len(actors) != tc.wantBypass {
				t.Errorf("%s\nbypass_actors = %s, want a list of %d", tc.reason, raw, tc.wantBypass)
			}

			if raw, ok := fields["conditions"]; ok != (tc.wantConditions != "") {
				t.Errorf("%s\nconditions present = %v, want %v; body %s", tc.reason, ok, tc.wantConditions != "", body)
			} else if ok && string(raw) != tc.wantConditions {
				t.Errorf("%s\nconditions = %s, want %s", tc.reason, raw, tc.wantConditions)
			}

			raw, ok = fields["rules"]
			if !ok {
				t.Fatalf("%s\nrules omitted, want it sent; body %s", tc.reason, body)
			}
			var rules []struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &rules); err != nil || rules == nil {
				t.Fatalf("%s\nrules = %s, want a list", tc.reason, raw)
			}
			got := make([]string, 0, len(rules))
			for _, r := range rules {
				got = append(got, r.Type)
			}
			if diff := cmp.Diff(tc.wantRuleTypes, got, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
				t.Errorf("%s\nrule types: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// A ruleset as declared, with unset pointers and no conditions, is sent exactly as the
// same ruleset after getRepositoryRulesMapFromCr, so any caller of crRepoRulesToRulesConfig
// sends the same request and none can panic on an unset field. The declared ruleset stays
// as it is, since it is the live CR's spec.
func TestCrRepoRulesToRulesConfigNormalises(t *testing.T) {
	send := func(t *testing.T, r v1alpha1.RepositoryRuleset) string {
		t.Helper()
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("crRepoRulesToRulesConfig panicked: %v", p)
			}
		}()
		rs, err := crRepoRulesToRulesConfig(r)
		if err != nil {
			t.Fatalf("crRepoRulesToRulesConfig: %v", err)
		}
		body, err := json.Marshal(rs)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(body)
	}
	cases := map[string]v1alpha1.RepositoryRuleset{
		"NameOnly": {Name: "raw"},
		"Push":     {Name: "raw", Target: github.Ptr("push")},
		"EveryRuleUnset": {
			Name:         "raw",
			BypassActors: []*v1alpha1.RulesetByPassActors{{ActorID: github.Ptr(int64(9)), ActorType: github.Ptr("Team")}, {ActorType: github.Ptr("OrganizationAdmin")}},
			Rules: &v1alpha1.Rules{
				Update:                               github.Ptr(true),
				RequiredDeployments:                  &v1alpha1.RulesRequiredDeployments{},
				PullRequest:                          &v1alpha1.RulesPullRequest{},
				RequiredStatusChecks:                 &v1alpha1.RulesRequiredStatusChecks{RequiredStatusChecks: []*v1alpha1.RulesRequiredStatusChecksParameters{{Context: "b"}, {Context: "a"}}},
				CommitMessagePattern:                 &v1alpha1.RulesPattern{Operator: "contains", Pattern: "fix"},
				CodeScanning:                         &v1alpha1.RulesCodeScanning{CodeScanningTools: []*v1alpha1.RulesCodeScanningTool{{Tool: "ZAP", AlertsThreshold: "all", SecurityAlertsThreshold: "all"}, {Tool: "CodeQL", AlertsThreshold: "all", SecurityAlertsThreshold: "all"}}},
				CodeCoverage:                         &v1alpha1.RulesCodeCoverage{MinimumCoverage: github.Ptr("80.0")},
				RequireSecretScanningAlertResolution: &v1alpha1.RulesSecretScanningAlertResolution{},
				CopilotCodeReview:                    &v1alpha1.RulesCopilotCodeReview{},
				FileExtensionRestriction:             &v1alpha1.RulesFileExtensionRestriction{RestrictedFileExtensions: []string{"*.exe", "*.bin"}},
				FilePathRestriction:                  &v1alpha1.RulesFilePathRestriction{RestrictedFilePaths: []string{"secrets/", "keys/"}},
				MaxFileSize:                          &v1alpha1.RulesMaxFileSize{MaxFileSize: 10},
			},
		},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			declared := raw.DeepCopy()
			want := send(t, crRulesets(t, []v1alpha1.RepositoryRuleset{raw})[raw.Name])

			if got := send(t, raw); got != want {
				t.Errorf("request from the declared ruleset:\n got %s\nwant %s", got, want)
			}
			if diff := cmp.Diff(declared, &raw); diff != "" {
				t.Errorf("declared ruleset changed: -before, +after:\n%s", diff)
			}
		})
	}
}

// unmanagedRuleTypes names exactly the unmodelled rule types, including types GitHub adds later.
func TestUnmanagedRuleTypes(t *testing.T) {
	// decode builds the rules from GitHub's wire format, so each case also pins that the
	// type name GitHub sends reaches the field the guard checks.
	decode := func(types ...string) []*rulesets.Rule {
		wire := make([]map[string]string, len(types))
		for i, typ := range types {
			wire[i] = map[string]string{"type": typ}
		}
		body, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var rules []*rulesets.Rule
		if err := json.Unmarshal(body, &rules); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		return rules
	}

	type testCase struct {
		reason string
		rules  []*rulesets.Rule
		want   []string
	}
	cases := map[string]testCase{
		"NoRules": {
			reason: "a ruleset without rules holds nothing an update could drop",
		},
		"ModelledOnly": {
			reason: "rules the provider models survive an update",
			rules: decode("creation", "update", "deletion", "required_linear_history", "required_deployments", "required_signatures", "pull_request", "required_status_checks", "non_fast_forward",
				"merge_queue", "commit_message_pattern", "commit_author_email_pattern", "committer_email_pattern", "branch_name_pattern", "tag_name_pattern",
				"code_scanning", "code_quality", "code_coverage", "copilot_code_review", "file_extension_restriction", "file_path_restriction", "max_file_path_length", "max_file_size",
				"license_compliance_scanning", "require_secret_scanning_alert_resolution"),
		},
		"SortedWithModelled": {
			reason: "the condition lists the unmanaged types sorted, whatever order GitHub sends them in",
			rules:  decode("repository_transfer", "creation", "repository_create"),
			want:   []string{"repository_create", "repository_transfer"},
		},
	}
	for _, typ := range []string{
		"repository_create", "repository_delete", "repository_name", "repository_transfer", "repository_visibility",
		"workflows",
	} {
		cases[typ] = testCase{
			reason: "an update would drop this rule",
			rules:  decode(typ),
			want:   []string{typ},
		}
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, unmanagedRuleTypes(tc.rules)); diff != "" {
				t.Errorf("%s\nunmanagedRuleTypes(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

const secondRulesetID int64 = 789

// withSecondRuleset declares a second ruleset, test-ruleset-2, identical to the first.
func withSecondRuleset() repositoryModifier {
	return func(cr *v1alpha1.Repository) {
		second := (*cr.Spec.ForProvider.RepositoryRules)[0]
		second.Name = "test-ruleset-2"
		*cr.Spec.ForProvider.RepositoryRules = append(*cr.Spec.ForProvider.RepositoryRules, second)
	}
}

// twoRulesets fakes GitHub holding test-ruleset-1 and test-ruleset-2 as declared by
// withSecondRuleset, each with its change applied.
func twoRulesets(first, second func(*rulesets.Ruleset)) *fake.MockRulesetsClient {
	held := func(id int64) *rulesets.Ruleset {
		r := githubRuleset()[0]
		if id == secondRulesetID {
			r.ID = github.Ptr(secondRulesetID)
			r.Name = "test-ruleset-2"
			second(r)
		} else {
			first(r)
		}
		return r
	}
	return &fake.MockRulesetsClient{
		MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return []*rulesets.Ruleset{held(rr1Id), held(secondRulesetID)}, fake.GenerateEmptyResponse(), nil
		},
		MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
			return held(rulesetID), fake.GenerateEmptyResponse(), nil
		},
	}
}

// withRuleTypes adds rules of the given types to a ruleset.
func withRuleTypes(types ...string) func(*rulesets.Ruleset) {
	return func(r *rulesets.Ruleset) {
		for _, typ := range types {
			r.Rules = append(r.Rules, &rulesets.Rule{Type: typ})
		}
	}
}

// withoutCreation drops the creation rule the CR declares, so the ruleset drifts.
func withoutCreation(r *rulesets.Ruleset) {
	r.Rules = slices.DeleteFunc(r.Rules, func(rule *rulesets.Rule) bool { return rule.Type == "creation" })
}

// An update would drop rules of a type the provider does not model, including types
// GitHub adds later, so a managed ruleset holding one is left as it is and reported on
// RulesetsPartial and the ruleset_parameters gauge. The other rulesets still decide
// ResourceUpToDate: one ruleset edited in the GitHub UI must not freeze the repository.
func TestObserveRulesetWithUnmanagedRuleTypesIsLeftAsItIs(t *testing.T) {
	cases := map[string]struct {
		types    []string
		second   func(*rulesets.Ruleset)
		upToDate bool
		want     string
	}{
		"RepositoryTarget": {
			types:    []string{"repository_create"},
			second:   func(*rulesets.Ruleset) {},
			upToDate: true,
			want:     "ruleset test-ruleset-1 has rule types this provider does not manage (repository_create); it is left as it is until they are removed on GitHub or modelled by the provider",
		},
		"OrganizationLevel": {
			types:    []string{"workflows", "repository_name"},
			second:   func(*rulesets.Ruleset) {},
			upToDate: true,
			want:     "ruleset test-ruleset-1 has rule types this provider does not manage (repository_name, workflows); it is left as it is until they are removed on GitHub or modelled by the provider",
		},
		"OtherRulesetDrifts": {
			types:    []string{"repository_create"},
			second:   withoutCreation,
			upToDate: false,
			want:     "ruleset test-ruleset-1 has rule types this provider does not manage (repository_create); it is left as it is until they are removed on GitHub or modelled by the provider",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics := telemetry.NewForTest()
			gauge := metrics.RepositoryUnreconcilableForTest()
			cr := repository(withSecondRuleset())
			cr.Spec.ForProvider.Org = "acme"
			e := external{github: clientFor(upToDateRepositories(nil), twoRulesets(withRuleTypes(tc.types...), tc.second)), metrics: metrics}

			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.upToDate {
				t.Errorf("ResourceUpToDate = %v, want %v: test-ruleset-2 alone decides it", got.ResourceUpToDate, tc.upToDate)
			}
			condition := cr.GetCondition(typeRulesetsPartial)
			if condition.Status != corev1.ConditionTrue || condition.Reason != reasonUnmanagedRuleTypes || condition.Message != tc.want {
				t.Errorf("RulesetsPartial = %v/%v %q, want True/%v %q", condition.Status, condition.Reason, condition.Message, reasonUnmanagedRuleTypes, tc.want)
			}
			for dimension, want := range map[string]float64{
				telemetry.DimensionRulesetParameters: 1,
				telemetry.DimensionRulesetsForbidden: 0,
				telemetry.DimensionRulesetSpec:       0,
			} {
				if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, dimension)); got != want {
					t.Errorf("unreconcilable{dimension=%s} = %v, want %v", dimension, got, want)
				}
			}
		})
	}
}

// Update leaves a ruleset holding an unmodelled rule type as it is and still writes the
// rest: the drifting ruleset is updated and the topics are replaced.
func TestUpdateLeavesRulesetWithUnmanagedRuleTypes(t *testing.T) {
	rs := twoRulesets(withRuleTypes("repository_create"), withoutCreation)
	var updated []int64
	rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		updated = append(updated, rulesetID)
		return &r, fake.GenerateEmptyResponse(), nil
	}
	rs.MockCreateRuleset = func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("CreateRuleset called for %q; both declared rulesets exist on GitHub", r.Name)
		return &r, fake.GenerateEmptyResponse(), nil
	}
	rs.MockDeleteRuleset = func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
		t.Errorf("DeleteRuleset called for %d; both rulesets are declared", rulesetID)
		return fake.GenerateEmptyResponse(), nil
	}
	repos := upToDateRepositories(nil)
	repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
		return req, fake.GenerateEmptyResponse(), nil
	}
	topicWrites := 0
	repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
		topicWrites++
		return topics, fake.GenerateEmptyResponse(), nil
	}
	cr := settingsOnlyRepository()
	cr.Spec.ForProvider.RepositoryRules = repository(withSecondRuleset()).Spec.ForProvider.RepositoryRules

	if _, err := (&external{github: clientFor(repos, rs)}).Update(context.Background(), cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if diff := cmp.Diff([]int64{secondRulesetID}, updated); diff != "" {
		t.Errorf("UpdateRuleset IDs: -want, +got:\n%s", diff)
	}
	if topicWrites != 1 {
		t.Errorf("ReplaceAllTopics calls = %d, want 1", topicWrites)
	}
}

// Reading the rulesets leaves the caller's CR map as it is: the names of the rulesets
// left as they are come back in report.skipped, and Observe and Update drop them from
// the compare and the diff themselves.
func TestGetRepositoryRulesWithConfigReportsSkippedRulesets(t *testing.T) {
	rules := *repository(withSecondRuleset()).Spec.ForProvider.RepositoryRules
	crMap, err := getRepositoryRulesMapFromCr(rules)
	if err != nil {
		t.Fatalf("getRepositoryRulesMapFromCr: %v", err)
	}
	want, _ := getRepositoryRulesMapFromCr(rules)
	gh := clientFor(upToDateRepositories(nil), twoRulesets(func(*rulesets.Ruleset) {}, withRuleTypes("repository_create")))
	ghRulesets, err := getRepositoryRules(context.Background(), gh, "acme", repo)
	if err != nil {
		t.Fatalf("getRepositoryRules: %v", err)
	}

	ghMap, report, err := getRepositoryRulesWithConfig(context.Background(), gh, "acme", repo, ghRulesets, crMap)
	if err != nil {
		t.Fatalf("getRepositoryRulesWithConfig: %v", err)
	}
	if diff := cmp.Diff(want, crMap); diff != "" {
		t.Errorf("crRulesets after the call: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff([]string{"test-ruleset-2"}, report.skipped); diff != "" {
		t.Errorf("report.skipped: -want, +got:\n%s", diff)
	}
	if _, ok := ghMap["test-ruleset-2"]; ok {
		t.Errorf("returned map holds test-ruleset-2; a skipped ruleset is left out of it")
	}
}

// Reading the rulesets leaves the caller's CR map as it is when GitHub returns
// requireExtraApprovalForUnattributedChanges for a ruleset whose CR leaves it unset: the
// ruleset is named in report.extraApprovalReturned, and Observe and Update set the
// desired true themselves.
func TestGetRepositoryRulesWithConfigReportsExtraApprovalReturned(t *testing.T) {
	rules := *repository(withPullRequestRule(1)).Spec.ForProvider.RepositoryRules
	crMap := crRulesets(t, rules)
	want := crRulesets(t, rules)
	rs := upToDateRulesets()
	rulesetHolding(t, rs, func(m *rulesets.ModelledRules) {
		m.PullRequest = &rulesets.PullRequestRuleParameters{RequireExtraApprovalForUnattributedChanges: github.Ptr(false)}
	})
	gh := clientFor(upToDateRepositories(nil), rs)
	ghRulesets, err := getRepositoryRules(context.Background(), gh, "acme", repo)
	if err != nil {
		t.Fatalf("getRepositoryRules: %v", err)
	}

	_, report, err := getRepositoryRulesWithConfig(context.Background(), gh, "acme", repo, ghRulesets, crMap)
	if err != nil {
		t.Fatalf("getRepositoryRulesWithConfig: %v", err)
	}
	if diff := cmp.Diff(want, crMap); diff != "" {
		t.Errorf("crRulesets after the call: -want, +got:\n%s", diff)
	}
	if diff := cmp.Diff([]string{rr1name}, report.extraApprovalReturned); diff != "" {
		t.Errorf("report.extraApprovalReturned: -want, +got:\n%s", diff)
	}
}

// applyRulesetReport turns the CR's rulesets into the desired state: a ruleset left as it
// is drops out of the compare and the diff, and a ruleset whose CR leaves
// requireExtraApprovalForUnattributedChanges unset while GitHub returns it wants GitHub's
// default, true, so a false on GitHub is drift. A ruleset the report does not name keeps
// the field unset, and the update omits it.
func TestApplyRulesetReport(t *testing.T) {
	pullRequest := func(v *bool) *v1alpha1.Rules {
		return &v1alpha1.Rules{PullRequest: &v1alpha1.RulesPullRequest{RequireExtraApprovalForUnattributedChanges: v}}
	}
	crMap := map[string]v1alpha1.RepositoryRuleset{
		"skipped":  {Name: "skipped", Rules: pullRequest(nil)},
		"returned": {Name: "returned", Rules: pullRequest(nil)},
		"absent":   {Name: "absent", Rules: pullRequest(nil)},
	}
	want := map[string]v1alpha1.RepositoryRuleset{
		"returned": {Name: "returned", Rules: pullRequest(github.Ptr(true))},
		"absent":   {Name: "absent", Rules: pullRequest(nil)},
	}

	applyRulesetReport(crMap, rulesetReport{skipped: []string{"skipped"}, extraApprovalReturned: []string{"returned"}})
	if diff := cmp.Diff(want, crMap); diff != "" {
		t.Errorf("crRulesets: -want, +got:\n%s", diff)
	}
}

// Unmanaged rule types and unmanaged parameters on different rulesets are both named:
// the reason is UnmanagedRuleTypes, the rule-type sentences come first, and the
// ruleset_parameters gauge is 1.
func TestObserveUnmanagedRuleTypesAndParametersShareRulesetsPartial(t *testing.T) {
	withUnmanagedParameter := func(r *rulesets.Ruleset) {
		r.Rules = append(r.Rules, &rulesets.Rule{Type: "merge_queue", Parameters: json.RawMessage(`{"actor_controlled_merging":true}`)})
	}
	metrics := telemetry.NewForTest()
	cr := repository(withSecondRuleset())
	cr.Spec.ForProvider.Org = "acme"
	e := external{github: clientFor(upToDateRepositories(nil), twoRulesets(withUnmanagedParameter, withRuleTypes("repository_create"))), metrics: metrics}
	want := "ruleset test-ruleset-2 has rule types this provider does not manage (repository_create); it is left as it is until they are removed on GitHub or modelled by the provider; " +
		"ruleset test-ruleset-1: rule merge_queue has parameter actor_controlled_merging that this provider does not manage; GitHub resets it when the ruleset is next updated"

	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	condition := cr.GetCondition(typeRulesetsPartial)
	if condition.Status != corev1.ConditionTrue || condition.Reason != reasonUnmanagedRuleTypes || condition.Message != want {
		t.Errorf("RulesetsPartial = %v/%v %q, want True/%v %q", condition.Status, condition.Reason, condition.Message, reasonUnmanagedRuleTypes, want)
	}
	if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, telemetry.DimensionRulesetParameters)); got != 1 {
		t.Errorf("unreconcilable{dimension=ruleset_parameters} = %v, want 1", got)
	}
}

const githubOnlyRulesetID int64 = 456

// withGitHubOnlyRuleset adds a ruleset that exists on GitHub but is not named in the CR,
// holding a rule the provider does not model.
func withGitHubOnlyRuleset(rs *fake.MockRulesetsClient) {
	githubOnly := func() *rulesets.Ruleset {
		return &rulesets.Ruleset{
			ID:          github.Ptr(githubOnlyRulesetID),
			Name:        "github-only",
			Enforcement: rr1enforcement,
			Rules:       []*rulesets.Rule{{Type: "repository_create"}},
		}
	}
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		return append(githubRuleset(), githubOnly()), fake.GenerateEmptyResponse(), nil
	}
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		if rulesetID == githubOnlyRulesetID {
			return githubOnly(), fake.GenerateEmptyResponse(), nil
		}
		return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
	}
}

// The unmanaged-rule guard covers only rulesets named in the CR. A ruleset not named there
// is deleted, never updated, so no rule of it can be dropped; leaving it as it is would
// keep a ruleset the CR leaves out.
func TestObserveGitHubOnlyRulesetWithUnmanagedRuleIsSurplus(t *testing.T) {
	rs := upToDateRulesets()
	withGitHubOnlyRuleset(rs)

	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = true, want false: the GitHub-only ruleset is surplus and must be deleted")
	}
}

// Update deletes a ruleset that is not named in the CR even when it holds a rule the
// provider does not model, and never updates it: an update would drop that rule.
func TestUpdateRepositoryRulesDeletesGitHubOnlyRuleset(t *testing.T) {
	rs := upToDateRulesets()
	withGitHubOnlyRuleset(rs)
	var deleted []int64
	rs.MockDeleteRuleset = func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
		deleted = append(deleted, rulesetID)
		return fake.GenerateEmptyResponse(), nil
	}
	rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		if rulesetID == githubOnlyRulesetID {
			t.Errorf("UpdateRuleset called for the GitHub-only ruleset; it would drop the repository_create rule")
		}
		return &r, fake.GenerateEmptyResponse(), nil
	}

	if err := updateRepositoryRules(context.Background(), repository(), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
		t.Fatalf("updateRepositoryRules: %v", err)
	}
	if diff := cmp.Diff([]int64{githubOnlyRulesetID}, deleted); diff != "" {
		t.Errorf("DeleteRuleset IDs: -want, +got:\n%s", diff)
	}
}

const renamedRulesetID int64 = 790

// renamedRulesets fakes GitHub holding the fixture's ruleset under the name old-name,
// followed by kept copies named kept-0, kept-1, ... as declared by withKeptRulesets.
// It logs every ruleset write in the order it is sent; createErr answers CreateRuleset.
func renamedRulesets(kept int, createErr error) (*fake.MockRulesetsClient, *[]string) {
	held := githubRuleset()
	held[0].ID = github.Ptr(renamedRulesetID)
	held[0].Name = "old-name"
	for i := range kept {
		r := githubRuleset()[0]
		r.ID = github.Ptr(int64(1000 + i))
		r.Name = fmt.Sprintf("kept-%d", i)
		held = append(held, r)
	}
	var writes []string
	rs := &fake.MockRulesetsClient{
		MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return held, fake.GenerateEmptyResponse(), nil
		},
		MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
			for _, r := range held {
				if *r.ID == rulesetID {
					return r, fake.GenerateEmptyResponse(), nil
				}
			}
			return nil, nil, fmt.Errorf("ruleset %d not held", rulesetID)
		},
		MockCreateRuleset: func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			writes = append(writes, "create "+r.Name)
			if createErr != nil {
				return nil, nil, createErr
			}
			return &r, fake.GenerateEmptyResponse(), nil
		},
		MockUpdateRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			writes = append(writes, fmt.Sprintf("update %d", rulesetID))
			return &r, fake.GenerateEmptyResponse(), nil
		},
		MockDeleteRuleset: func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
			writes = append(writes, fmt.Sprintf("delete %d", rulesetID))
			return fake.GenerateEmptyResponse(), nil
		},
	}
	return rs, &writes
}

// withKeptRulesets declares kept-0, kept-1, ... identical to the fixture's ruleset.
func withKeptRulesets(kept int) repositoryModifier {
	return func(cr *v1alpha1.Repository) {
		first := (*cr.Spec.ForProvider.RepositoryRules)[0]
		for i := range kept {
			r := first
			r.Name = fmt.Sprintf("kept-%d", i)
			*cr.Spec.ForProvider.RepositoryRules = append(*cr.Spec.ForProvider.RepositoryRules, r)
		}
	}
}

// A renamed ruleset is created before the old one is deleted, so the branch stays
// protected while the create is in flight. Only when the creates would take the
// repository over GitHub's cap of 75 rulesets do the surplus ones go first.
func TestUpdateRepositoryRulesCreatesBeforeDeleting(t *testing.T) {
	createNew := "create " + rr1name
	deleteOld := fmt.Sprintf("delete %d", renamedRulesetID)
	cases := map[string]struct {
		kept int
		want []string
	}{
		"Rename":         {kept: 0, want: []string{createNew, deleteOld}},
		"ReachesCap":     {kept: 73, want: []string{createNew, deleteOld}},
		"WouldExceedCap": {kept: 74, want: []string{deleteOld, createNew}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rs, writes := renamedRulesets(tc.kept, nil)
			cr := repository(withKeptRulesets(tc.kept))
			if err := updateRepositoryRules(context.Background(), cr, clientFor(upToDateRepositories(nil), rs), repo); err != nil {
				t.Fatalf("updateRepositoryRules: %v", err)
			}
			if diff := cmp.Diff(tc.want, *writes); diff != "" {
				t.Errorf("ruleset writes: -want, +got:\n%s", diff)
			}
		})
	}
}

// When GitHub refuses the renamed ruleset (422, e.g. a rule the plan does not allow),
// the old ruleset stays on GitHub and keeps protecting the branch until the spec is fixed.
func TestUpdateRepositoryRulesFailedCreateKeepsOldRuleset(t *testing.T) {
	refused := &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}, Message: "Validation Failed"}
	rs, writes := renamedRulesets(0, refused)

	err := updateRepositoryRules(context.Background(), repository(), clientFor(upToDateRepositories(nil), rs), repo)
	if !errors.Is(err, refused) {
		t.Errorf("updateRepositoryRules error = %v, want the 422 from CreateRuleset", err)
	}
	if diff := cmp.Diff([]string{"create " + rr1name}, *writes); diff != "" {
		t.Errorf("ruleset writes: -want, +got:\n%s", diff)
	}
}

// An empty repositoryRules list survives the provider's write-back as [], so the
// rulesets on GitHub still get deleted.
func TestRepositoryRulesEmptyListSurvivesWriteBack(t *testing.T) {
	encode := func(cr *v1alpha1.Repository) string {
		b, err := json.Marshal(cr)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		return string(b)
	}

	set := &v1alpha1.Repository{}
	set.Spec.ForProvider.RepositoryRules = &[]v1alpha1.RepositoryRuleset{}
	if got := encode(set); !strings.Contains(got, `"repositoryRules":[]`) {
		t.Errorf("set and empty: %s, want \"repositoryRules\":[]", got)
	}
	if got := encode(&v1alpha1.Repository{}); strings.Contains(got, `"repositoryRules"`) {
		t.Errorf("unset: %s, want no repositoryRules key", got)
	}

	// stored is the object as the API server holds it. The cache hands out deep copies.
	var stored v1alpha1.Repository
	if err := json.Unmarshal([]byte(`{"spec":{"forProvider":{"repositoryRules":[]}}}`), &stored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got := encode(stored.DeepCopy()); !strings.Contains(got, `"repositoryRules":[]`) {
		t.Errorf("written back: %s, want \"repositoryRules\":[]", got)
	}
}

// An empty repositoryRules list deletes every ruleset on GitHub.
func TestEmptyRepositoryRulesDeletesEveryRuleset(t *testing.T) {
	rs := upToDateRulesets()
	withGitHubOnlyRuleset(rs)
	var deleted []int64
	rs.MockDeleteRuleset = func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
		deleted = append(deleted, rulesetID)
		return fake.GenerateEmptyResponse(), nil
	}
	rs.MockCreateRuleset = func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("CreateRuleset called for %q; the CR declares no rulesets", r.Name)
		return &r, fake.GenerateEmptyResponse(), nil
	}
	rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("UpdateRuleset called for %d; the CR declares no rulesets", rulesetID)
		return &r, fake.GenerateEmptyResponse(), nil
	}

	cr := repository()
	cr.Spec.ForProvider.RepositoryRules = &[]v1alpha1.RepositoryRuleset{}
	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = true, want false: both rulesets on GitHub must be deleted")
	}

	repos := upToDateRepositories(nil)
	repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
		return req, fake.GenerateEmptyResponse(), nil
	}
	repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
		return topics, fake.GenerateEmptyResponse(), nil
	}
	cr = settingsOnlyRepository()
	cr.Spec.ForProvider.RepositoryRules = &[]v1alpha1.RepositoryRuleset{}
	if _, err := (&external{github: clientFor(repos, rs)}).Update(context.Background(), cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if diff := cmp.Diff([]int64{rr1Id, githubOnlyRulesetID}, deleted, cmpopts.SortSlices(func(a, b int64) bool { return a < b })); diff != "" {
		t.Errorf("DeleteRuleset IDs: -want, +got:\n%s", diff)
	}
}

// With repositoryRules unset, Observe and Update make no ruleset call.
func TestUnsetRepositoryRulesMakesNoRulesetCall(t *testing.T) {
	unexpected := errors.New("unexpected ruleset call")
	rs := &fake.MockRulesetsClient{
		MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			t.Errorf("GetAllRulesets called")
			return nil, nil, unexpected
		},
		MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
			t.Errorf("GetRuleset called")
			return nil, nil, unexpected
		},
		MockCreateRuleset: func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			t.Errorf("CreateRuleset called")
			return nil, nil, unexpected
		},
		MockUpdateRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			t.Errorf("UpdateRuleset called")
			return nil, nil, unexpected
		},
		MockDeleteRuleset: func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
			t.Errorf("DeleteRuleset called")
			return nil, unexpected
		},
	}

	cr := repository()
	cr.Spec.ForProvider.RepositoryRules = nil
	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: everything else matches GitHub")
	}

	repos := upToDateRepositories(nil)
	repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
		return req, fake.GenerateEmptyResponse(), nil
	}
	repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
		return topics, fake.GenerateEmptyResponse(), nil
	}
	cr = settingsOnlyRepository()
	if _, err := (&external{github: clientFor(repos, rs)}).Update(context.Background(), cr); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// modelledRuleCase describes one modelled rule.
type modelledRuleCase struct {
	// cr declares the rule, with only its required fields unless the case name says otherwise.
	cr func(*v1alpha1.Rules)
	// sent is the rule as the request carries it, with defaults filled in.
	sent func(*rulesets.ModelledRules)
	// sentNew, when set, is the rule as the request carries it while GitHub does not hold
	// the rule yet; sent applies otherwise.
	sentNew func(*rulesets.ModelledRules)
	// onGitHub lists forms GitHub may return for the declared rule.
	onGitHub []func(*rulesets.ModelledRules)
	// differs holds, per field, a change to onGitHub[0] that is real drift.
	differs map[string]func(*rulesets.ModelledRules)
}

func modelledRuleCases() map[string]modelledRuleCase {
	mergeQueue := func() *rulesets.MergeQueueRuleParameters {
		return &rulesets.MergeQueueRuleParameters{
			CheckResponseTimeoutMinutes:  60,
			GroupingStrategy:             "ALLGREEN",
			MaxEntriesToBuild:            8,
			MaxEntriesToMerge:            4,
			MergeMethod:                  "SQUASH",
			MinEntriesToMerge:            2,
			MinEntriesToMergeWaitMinutes: 5,
		}
	}
	codeQL := func() *rulesets.CodeScanningTool {
		return &rulesets.CodeScanningTool{Tool: "CodeQL", AlertsThreshold: "all", SecurityAlertsThreshold: "critical"}
	}
	zap := func() *rulesets.CodeScanningTool {
		return &rulesets.CodeScanningTool{Tool: "zap", AlertsThreshold: "errors", SecurityAlertsThreshold: "high_or_higher"}
	}

	// pullRequestDefaults is the pull_request rule GitHub returns when only required parameters are set.
	pullRequestDefaults := func() *rulesets.PullRequestRuleParameters {
		return &rulesets.PullRequestRuleParameters{
			AllowedMergeMethods:  []string{"merge", "squash", "rebase"},
			DismissalRestriction: &rulesets.DismissalRestriction{AllowedActors: []*rulesets.Actor{}},
			RequiredReviewers:    []*rulesets.RequiredReviewer{},

			RequireExtraApprovalForUnattributedChanges: github.Ptr(true),
		}
	}
	// pullRequestSent is pullRequestDefaults as the provider sends it, merge methods sorted.
	pullRequestSent := func() *rulesets.PullRequestRuleParameters {
		p := pullRequestDefaults()
		p.AllowedMergeMethods = []string{"merge", "rebase", "squash"}
		return p
	}
	reviewer := func(id int64, approvals int, patterns ...string) *rulesets.RequiredReviewer {
		return &rulesets.RequiredReviewer{FilePatterns: patterns, MinimumApprovals: approvals, Reviewer: rulesets.Actor{ID: json.Number(strconv.FormatInt(id, 10)), Type: "Team"}}
	}
	// pullRequestLists is the rule of the "pull_request with lists" case, lists in sent order.
	pullRequestLists := func() *rulesets.PullRequestRuleParameters {
		return &rulesets.PullRequestRuleParameters{
			AllowedMergeMethods: []string{"merge", "squash"},
			DismissalRestriction: &rulesets.DismissalRestriction{Enabled: true, AllowedActors: []*rulesets.Actor{
				{ID: "2", Type: "Team"}, {ID: "9", Type: "Team"}, {ID: "3", Type: "User"},
			}},
			RequiredReviewers: []*rulesets.RequiredReviewer{reviewer(4, 2, "*"), reviewer(7, 1, "b/**", "!b/x/**", "a/**")},

			RequireExtraApprovalForUnattributedChanges: github.Ptr(false),
		}
	}
	statusChecks := func() *rulesets.RequiredStatusChecksRuleParameters {
		return &rulesets.RequiredStatusChecksRuleParameters{RequiredStatusChecks: []*rulesets.StatusCheck{{Context: "ci"}}}
	}
	coverage := func(minimum, drop float64) *rulesets.CodeCoverageRuleParameters {
		return &rulesets.CodeCoverageRuleParameters{MinimumCoverage: &minimum, MaxCoverageDrop: &drop}
	}

	cases := map[string]modelledRuleCase{
		"pull_request": {
			cr:   func(r *v1alpha1.Rules) { r.PullRequest = &v1alpha1.RulesPullRequest{} },
			sent: func(r *rulesets.ModelledRules) { r.PullRequest = pullRequestSent() },
			sentNew: func(r *rulesets.ModelledRules) {
				r.PullRequest = pullRequestSent()
				r.PullRequest.RequireExtraApprovalForUnattributedChanges = nil
			},
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) { r.PullRequest = pullRequestDefaults() }},
			differs: map[string]func(*rulesets.ModelledRules){
				"allowedMergeMethods":  func(r *rulesets.ModelledRules) { r.PullRequest.AllowedMergeMethods = []string{"squash"} },
				"dismissalRestriction": func(r *rulesets.ModelledRules) { r.PullRequest.DismissalRestriction.Enabled = true },
				"dismissalRestriction.allowedActors": func(r *rulesets.ModelledRules) {
					r.PullRequest.DismissalRestriction.AllowedActors = []*rulesets.Actor{{ID: "3", Type: "User"}}
				},
				"requireExtraApprovalForUnattributedChanges": func(r *rulesets.ModelledRules) {
					r.PullRequest.RequireExtraApprovalForUnattributedChanges = github.Ptr(false)
				},
				"requiredReviewers": func(r *rulesets.ModelledRules) {
					r.PullRequest.RequiredReviewers = []*rulesets.RequiredReviewer{reviewer(4, 1, "*")}
				},
				"dismissStaleReviewsOnPush":      func(r *rulesets.ModelledRules) { r.PullRequest.DismissStaleReviewsOnPush = true },
				"requireCodeOwnerReview":         func(r *rulesets.ModelledRules) { r.PullRequest.RequireCodeOwnerReview = true },
				"requireLastPushApproval":        func(r *rulesets.ModelledRules) { r.PullRequest.RequireLastPushApproval = true },
				"requiredApprovingReviewCount":   func(r *rulesets.ModelledRules) { r.PullRequest.RequiredApprovingReviewCount = 1 },
				"requiredReviewThreadResolution": func(r *rulesets.ModelledRules) { r.PullRequest.RequiredReviewThreadResolution = true },
			},
		},
		"pull_request with lists": {
			cr: func(r *v1alpha1.Rules) {
				r.PullRequest = &v1alpha1.RulesPullRequest{
					AllowedMergeMethods: []v1alpha1.RulesMergeMethod{"squash", "merge"},
					DismissalRestriction: &v1alpha1.RulesDismissalRestriction{Enabled: true, AllowedActors: []*v1alpha1.RulesDismissalActor{
						{ID: 3, Type: "User"}, {ID: 9, Type: "Team"}, {ID: 2, Type: "Team"},
					}},
					RequireExtraApprovalForUnattributedChanges: github.Ptr(false),
					RequiredReviewers: []*v1alpha1.RulesRequiredReviewer{
						{FilePatterns: []string{"b/**", "!b/x/**", "a/**"}, MinimumApprovals: 1, Reviewer: v1alpha1.RulesReviewer{ID: 7, Type: "Team"}},
						{FilePatterns: []string{"*"}, MinimumApprovals: 2, Reviewer: v1alpha1.RulesReviewer{ID: 4, Type: "Team"}},
					},
				}
			},
			sent: func(r *rulesets.ModelledRules) { r.PullRequest = pullRequestLists() },
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) { r.PullRequest = pullRequestLists() },
				func(r *rulesets.ModelledRules) {
					p := pullRequestLists()
					p.AllowedMergeMethods = []string{"squash", "merge"}
					p.DismissalRestriction.AllowedActors = []*rulesets.Actor{{ID: "3", Type: "User"}, {ID: "9", Type: "Team"}, {ID: "2", Type: "Team"}}
					p.RequiredReviewers = []*rulesets.RequiredReviewer{reviewer(7, 1, "b/**", "!b/x/**", "a/**"), reviewer(4, 2, "*")}
					r.PullRequest = p
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"allowed actor id": func(r *rulesets.ModelledRules) { r.PullRequest.DismissalRestriction.AllowedActors[0].ID = "10" },
				"allowed actor type": func(r *rulesets.ModelledRules) {
					r.PullRequest.DismissalRestriction.AllowedActors[1].Type = "RepositoryRole"
				},
				"reviewer id":        func(r *rulesets.ModelledRules) { r.PullRequest.RequiredReviewers[0].Reviewer.ID = "5" },
				"reviewer approvals": func(r *rulesets.ModelledRules) { r.PullRequest.RequiredReviewers[0].MinimumApprovals = 1 },
				"reviewer patterns":  func(r *rulesets.ModelledRules) { r.PullRequest.RequiredReviewers[1].FilePatterns = []string{"a/**"} },
				"reviewer pattern order": func(r *rulesets.ModelledRules) {
					r.PullRequest.RequiredReviewers[1].FilePatterns = []string{"a/**", "b/**", "!b/x/**"}
				},
				"reviewer pattern duplicated": func(r *rulesets.ModelledRules) {
					r.PullRequest.RequiredReviewers[1].FilePatterns = []string{"b/**", "!b/x/**", "a/**", "a/**"}
				},
				"extra merge method": func(r *rulesets.ModelledRules) {
					r.PullRequest.AllowedMergeMethods = []string{"merge", "rebase", "squash"}
				},
				"extra allowed actor": func(r *rulesets.ModelledRules) {
					r.PullRequest.DismissalRestriction.AllowedActors = append(r.PullRequest.DismissalRestriction.AllowedActors, &rulesets.Actor{ID: "1", Type: "User"})
				},
				"no required reviewer": func(r *rulesets.ModelledRules) { r.PullRequest.RequiredReviewers = []*rulesets.RequiredReviewer{} },
			},
		},
		"required_status_checks": {
			cr: func(r *v1alpha1.Rules) {
				r.RequiredStatusChecks = &v1alpha1.RulesRequiredStatusChecks{RequiredStatusChecks: []*v1alpha1.RulesRequiredStatusChecksParameters{{Context: "ci"}}}
			},
			sent:     func(r *rulesets.ModelledRules) { r.RequiredStatusChecks = statusChecks() },
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) { r.RequiredStatusChecks = statusChecks() }},
			differs: map[string]func(*rulesets.ModelledRules){
				"doNotEnforceOnCreate":             func(r *rulesets.ModelledRules) { r.RequiredStatusChecks.DoNotEnforceOnCreate = true },
				"strictRequiredStatusChecksPolicy": func(r *rulesets.ModelledRules) { r.RequiredStatusChecks.StrictRequiredStatusChecksPolicy = true },
				"context":                          func(r *rulesets.ModelledRules) { r.RequiredStatusChecks.RequiredStatusChecks[0].Context = "lint" },
			},
		},
		"required_status_checks with doNotEnforceOnCreate": {
			cr: func(r *v1alpha1.Rules) {
				r.RequiredStatusChecks = &v1alpha1.RulesRequiredStatusChecks{
					DoNotEnforceOnCreate: github.Ptr(true),
					RequiredStatusChecks: []*v1alpha1.RulesRequiredStatusChecksParameters{{Context: "ci"}},
				}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.RequiredStatusChecks = statusChecks()
				r.RequiredStatusChecks.DoNotEnforceOnCreate = true
			},
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) {
				r.RequiredStatusChecks = statusChecks()
				r.RequiredStatusChecks.DoNotEnforceOnCreate = true
			}},
			differs: map[string]func(*rulesets.ModelledRules){
				"doNotEnforceOnCreate": func(r *rulesets.ModelledRules) { r.RequiredStatusChecks.DoNotEnforceOnCreate = false },
			},
		},
		"required_deployments": {
			cr: func(r *v1alpha1.Rules) { r.RequiredDeployments = &v1alpha1.RulesRequiredDeployments{} },
			sent: func(r *rulesets.ModelledRules) {
				r.RequiredDeployments = &rulesets.RequiredDeploymentsRuleParameters{RequiredDeploymentEnvironments: []string{}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.RequiredDeployments = &rulesets.RequiredDeploymentsRuleParameters{RequiredDeploymentEnvironments: []string{}}
				},
				func(r *rulesets.ModelledRules) { r.RequiredDeployments = &rulesets.RequiredDeploymentsRuleParameters{} },
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"environments": func(r *rulesets.ModelledRules) {
					r.RequiredDeployments.RequiredDeploymentEnvironments = []string{"prod"}
				},
			},
		},
		"code_quality": {
			cr: func(r *v1alpha1.Rules) { r.CodeQuality = &v1alpha1.RulesCodeQuality{Severity: "errors"} },
			sent: func(r *rulesets.ModelledRules) {
				r.CodeQuality = &rulesets.CodeQualityRuleParameters{Severity: "errors"}
			},
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) {
				r.CodeQuality = &rulesets.CodeQualityRuleParameters{Severity: "errors"}
			}},
			differs: map[string]func(*rulesets.ModelledRules){
				"severity": func(r *rulesets.ModelledRules) { r.CodeQuality.Severity = "all" },
			},
		},
		"code_coverage": {
			cr: func(r *v1alpha1.Rules) {
				r.CodeCoverage = &v1alpha1.RulesCodeCoverage{MinimumCoverage: github.Ptr("80.0"), MaxCoverageDrop: github.Ptr("2.5")}
			},
			sent:     func(r *rulesets.ModelledRules) { r.CodeCoverage = coverage(80, 2.5) },
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) { r.CodeCoverage = coverage(80, 2.5) }},
			differs: map[string]func(*rulesets.ModelledRules){
				"minimumCoverage":       func(r *rulesets.ModelledRules) { r.CodeCoverage.MinimumCoverage = github.Ptr(79.5) },
				"maxCoverageDrop":       func(r *rulesets.ModelledRules) { r.CodeCoverage.MaxCoverageDrop = github.Ptr(3.0) },
				"maxCoverageDrop unset": func(r *rulesets.ModelledRules) { r.CodeCoverage.MaxCoverageDrop = nil },
			},
		},
		"code_coverage without parameters": {
			cr:       func(r *v1alpha1.Rules) { r.CodeCoverage = &v1alpha1.RulesCodeCoverage{} },
			sent:     func(r *rulesets.ModelledRules) { r.CodeCoverage = &rulesets.CodeCoverageRuleParameters{} },
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) { r.CodeCoverage = &rulesets.CodeCoverageRuleParameters{} }},
			differs: map[string]func(*rulesets.ModelledRules){
				"minimumCoverage": func(r *rulesets.ModelledRules) { r.CodeCoverage.MinimumCoverage = github.Ptr(50.0) },
			},
		},
		"license_compliance_scanning": {
			cr:       func(r *v1alpha1.Rules) { r.LicenseComplianceScanning = github.Ptr(true) },
			sent:     func(r *rulesets.ModelledRules) { r.LicenseComplianceScanning = &rulesets.EmptyRuleParameters{} },
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) { r.LicenseComplianceScanning = &rulesets.EmptyRuleParameters{} }},
		},
		"require_secret_scanning_alert_resolution": {
			cr: func(r *v1alpha1.Rules) {
				r.RequireSecretScanningAlertResolution = &v1alpha1.RulesSecretScanningAlertResolution{}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.RequireSecretScanningAlertResolution = &rulesets.RequireSecretScanningAlertResolutionRuleParameters{SecretTypes: []string{"provider_patterns"}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.RequireSecretScanningAlertResolution = &rulesets.RequireSecretScanningAlertResolutionRuleParameters{SecretTypes: []string{"provider_patterns"}}
				},
				func(r *rulesets.ModelledRules) {
					r.RequireSecretScanningAlertResolution = &rulesets.RequireSecretScanningAlertResolutionRuleParameters{}
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"secretTypes": func(r *rulesets.ModelledRules) {
					r.RequireSecretScanningAlertResolution.SecretTypes = []string{"generic_patterns", "custom_patterns"}
				},
			},
		},
		"merge_queue": {
			cr: func(r *v1alpha1.Rules) {
				r.MergeQueue = &v1alpha1.RulesMergeQueue{
					CheckResponseTimeoutMinutes:  60,
					GroupingStrategy:             "ALLGREEN",
					MaxEntriesToBuild:            8,
					MaxEntriesToMerge:            4,
					MergeMethod:                  "SQUASH",
					MinEntriesToMerge:            2,
					MinEntriesToMergeWaitMinutes: 5,
				}
			},
			sent:     func(r *rulesets.ModelledRules) { r.MergeQueue = mergeQueue() },
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) { r.MergeQueue = mergeQueue() }},
			differs: map[string]func(*rulesets.ModelledRules){
				"checkResponseTimeoutMinutes":  func(r *rulesets.ModelledRules) { r.MergeQueue.CheckResponseTimeoutMinutes = 61 },
				"groupingStrategy":             func(r *rulesets.ModelledRules) { r.MergeQueue.GroupingStrategy = "HEADGREEN" },
				"maxEntriesToBuild":            func(r *rulesets.ModelledRules) { r.MergeQueue.MaxEntriesToBuild = 9 },
				"maxEntriesToMerge":            func(r *rulesets.ModelledRules) { r.MergeQueue.MaxEntriesToMerge = 3 },
				"mergeMethod":                  func(r *rulesets.ModelledRules) { r.MergeQueue.MergeMethod = "REBASE" },
				"minEntriesToMerge":            func(r *rulesets.ModelledRules) { r.MergeQueue.MinEntriesToMerge = 1 },
				"minEntriesToMergeWaitMinutes": func(r *rulesets.ModelledRules) { r.MergeQueue.MinEntriesToMergeWaitMinutes = 6 },
			},
		},
		"code_scanning": {
			cr: func(r *v1alpha1.Rules) {
				r.CodeScanning = &v1alpha1.RulesCodeScanning{CodeScanningTools: []*v1alpha1.RulesCodeScanningTool{
					{Tool: "zap", AlertsThreshold: "errors", SecurityAlertsThreshold: "high_or_higher"},
					{Tool: "CodeQL", AlertsThreshold: "all", SecurityAlertsThreshold: "critical"},
				}}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.CodeScanning = &rulesets.CodeScanningRuleParameters{CodeScanningTools: []*rulesets.CodeScanningTool{codeQL(), zap()}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.CodeScanning = &rulesets.CodeScanningRuleParameters{CodeScanningTools: []*rulesets.CodeScanningTool{codeQL(), zap()}}
				},
				func(r *rulesets.ModelledRules) {
					r.CodeScanning = &rulesets.CodeScanningRuleParameters{CodeScanningTools: []*rulesets.CodeScanningTool{zap(), codeQL()}}
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"tool":                    func(r *rulesets.ModelledRules) { r.CodeScanning.CodeScanningTools[0].Tool = "Semgrep" },
				"alertsThreshold":         func(r *rulesets.ModelledRules) { r.CodeScanning.CodeScanningTools[0].AlertsThreshold = "none" },
				"securityAlertsThreshold": func(r *rulesets.ModelledRules) { r.CodeScanning.CodeScanningTools[0].SecurityAlertsThreshold = "none" },
			},
		},
		"copilot_code_review": {
			cr: func(r *v1alpha1.Rules) { r.CopilotCodeReview = &v1alpha1.RulesCopilotCodeReview{} },
			sent: func(r *rulesets.ModelledRules) {
				r.CopilotCodeReview = &rulesets.CopilotCodeReviewRuleParameters{}
			},
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) {
				r.CopilotCodeReview = &rulesets.CopilotCodeReviewRuleParameters{}
			}},
			differs: map[string]func(*rulesets.ModelledRules){
				"reviewOnPush":            func(r *rulesets.ModelledRules) { r.CopilotCodeReview.ReviewOnPush = true },
				"reviewDraftPullRequests": func(r *rulesets.ModelledRules) { r.CopilotCodeReview.ReviewDraftPullRequests = true },
			},
		},
		"file_extension_restriction": {
			cr: func(r *v1alpha1.Rules) {
				r.FileExtensionRestriction = &v1alpha1.RulesFileExtensionRestriction{RestrictedFileExtensions: []string{"*.zip", "*.exe"}}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.FileExtensionRestriction = &rulesets.FileExtensionRestrictionRuleParameters{RestrictedFileExtensions: []string{"*.exe", "*.zip"}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.FileExtensionRestriction = &rulesets.FileExtensionRestrictionRuleParameters{RestrictedFileExtensions: []string{"*.exe", "*.zip"}}
				},
				func(r *rulesets.ModelledRules) {
					r.FileExtensionRestriction = &rulesets.FileExtensionRestrictionRuleParameters{RestrictedFileExtensions: []string{"*.zip", "*.exe"}}
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"restrictedFileExtensions": func(r *rulesets.ModelledRules) {
					r.FileExtensionRestriction.RestrictedFileExtensions = []string{"*.exe"}
				},
			},
		},
		"file_path_restriction": {
			cr: func(r *v1alpha1.Rules) {
				r.FilePathRestriction = &v1alpha1.RulesFilePathRestriction{RestrictedFilePaths: []string{"secrets/", "keys/"}}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.FilePathRestriction = &rulesets.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"keys/", "secrets/"}, IgnoredFilePaths: []string{}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.FilePathRestriction = &rulesets.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"keys/", "secrets/"}, IgnoredFilePaths: []string{}}
				},
				func(r *rulesets.ModelledRules) {
					r.FilePathRestriction = &rulesets.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"secrets/", "keys/"}}
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"restrictedFilePaths": func(r *rulesets.ModelledRules) {
					r.FilePathRestriction.RestrictedFilePaths = []string{"keys/", "secrets/", "tokens/"}
				},
				"ignoredFilePaths": func(r *rulesets.ModelledRules) {
					r.FilePathRestriction.IgnoredFilePaths = []string{"keys/public/"}
				},
			},
		},
		"file_path_restriction with ignoredFilePaths": {
			cr: func(r *v1alpha1.Rules) {
				r.FilePathRestriction = &v1alpha1.RulesFilePathRestriction{RestrictedFilePaths: []string{"keys/"}, IgnoredFilePaths: []string{"keys/b/", "keys/a/"}}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.FilePathRestriction = &rulesets.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"keys/"}, IgnoredFilePaths: []string{"keys/a/", "keys/b/"}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.FilePathRestriction = &rulesets.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"keys/"}, IgnoredFilePaths: []string{"keys/a/", "keys/b/"}}
				},
				func(r *rulesets.ModelledRules) {
					r.FilePathRestriction = &rulesets.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"keys/"}, IgnoredFilePaths: []string{"keys/b/", "keys/a/"}}
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"ignoredFilePaths":       func(r *rulesets.ModelledRules) { r.FilePathRestriction.IgnoredFilePaths = []string{"keys/a/"} },
				"ignoredFilePaths empty": func(r *rulesets.ModelledRules) { r.FilePathRestriction.IgnoredFilePaths = []string{} },
			},
		},
		"max_file_path_length": {
			cr: func(r *v1alpha1.Rules) {
				r.MaxFilePathLength = &v1alpha1.RulesMaxFilePathLength{MaxFilePathLength: 255}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.MaxFilePathLength = &rulesets.MaxFilePathLengthRuleParameters{MaxFilePathLength: 255}
			},
			onGitHub: []func(*rulesets.ModelledRules){func(r *rulesets.ModelledRules) {
				r.MaxFilePathLength = &rulesets.MaxFilePathLengthRuleParameters{MaxFilePathLength: 255}
			}},
			differs: map[string]func(*rulesets.ModelledRules){
				"maxFilePathLength": func(r *rulesets.ModelledRules) { r.MaxFilePathLength.MaxFilePathLength = 256 },
			},
		},
		"max_file_size": {
			cr: func(r *v1alpha1.Rules) { r.MaxFileSize = &v1alpha1.RulesMaxFileSize{MaxFileSize: 100} },
			sent: func(r *rulesets.ModelledRules) {
				r.MaxFileSize = &rulesets.MaxFileSizeRuleParameters{MaxFileSize: 100, IgnoredFilePaths: []string{}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.MaxFileSize = &rulesets.MaxFileSizeRuleParameters{MaxFileSize: 100, IgnoredFilePaths: []string{}}
				},
				func(r *rulesets.ModelledRules) { r.MaxFileSize = &rulesets.MaxFileSizeRuleParameters{MaxFileSize: 100} },
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"maxFileSize":      func(r *rulesets.ModelledRules) { r.MaxFileSize.MaxFileSize = 50 },
				"ignoredFilePaths": func(r *rulesets.ModelledRules) { r.MaxFileSize.IgnoredFilePaths = []string{"assets/"} },
			},
		},
		"max_file_size with ignoredFilePaths": {
			cr: func(r *v1alpha1.Rules) {
				r.MaxFileSize = &v1alpha1.RulesMaxFileSize{MaxFileSize: 100, IgnoredFilePaths: []string{"media/", "assets/"}}
			},
			sent: func(r *rulesets.ModelledRules) {
				r.MaxFileSize = &rulesets.MaxFileSizeRuleParameters{MaxFileSize: 100, IgnoredFilePaths: []string{"assets/", "media/"}}
			},
			onGitHub: []func(*rulesets.ModelledRules){
				func(r *rulesets.ModelledRules) {
					r.MaxFileSize = &rulesets.MaxFileSizeRuleParameters{MaxFileSize: 100, IgnoredFilePaths: []string{"assets/", "media/"}}
				},
				func(r *rulesets.ModelledRules) {
					r.MaxFileSize = &rulesets.MaxFileSizeRuleParameters{MaxFileSize: 100, IgnoredFilePaths: []string{"media/", "assets/"}}
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"ignoredFilePaths": func(r *rulesets.ModelledRules) { r.MaxFileSize.IgnoredFilePaths = []string{"media/"} },
			},
		},
	}

	// The five pattern rules share one shape; each is reached through its own field.
	patterns := map[string]struct {
		cr func(*v1alpha1.Rules) **v1alpha1.RulesPattern
		gh func(*rulesets.ModelledRules) **rulesets.PatternRuleParameters
	}{
		"commit_message_pattern": {
			cr: func(r *v1alpha1.Rules) **v1alpha1.RulesPattern { return &r.CommitMessagePattern },
			gh: func(r *rulesets.ModelledRules) **rulesets.PatternRuleParameters { return &r.CommitMessagePattern },
		},
		"commit_author_email_pattern": {
			cr: func(r *v1alpha1.Rules) **v1alpha1.RulesPattern { return &r.CommitAuthorEmailPattern },
			gh: func(r *rulesets.ModelledRules) **rulesets.PatternRuleParameters {
				return &r.CommitAuthorEmailPattern
			},
		},
		"committer_email_pattern": {
			cr: func(r *v1alpha1.Rules) **v1alpha1.RulesPattern { return &r.CommitterEmailPattern },
			gh: func(r *rulesets.ModelledRules) **rulesets.PatternRuleParameters { return &r.CommitterEmailPattern },
		},
		"branch_name_pattern": {
			cr: func(r *v1alpha1.Rules) **v1alpha1.RulesPattern { return &r.BranchNamePattern },
			gh: func(r *rulesets.ModelledRules) **rulesets.PatternRuleParameters { return &r.BranchNamePattern },
		},
		"tag_name_pattern": {
			cr: func(r *v1alpha1.Rules) **v1alpha1.RulesPattern { return &r.TagNamePattern },
			gh: func(r *rulesets.ModelledRules) **rulesets.PatternRuleParameters { return &r.TagNamePattern },
		},
	}
	for typ, p := range patterns {
		withDefaults := func(r *rulesets.ModelledRules) {
			*p.gh(r) = &rulesets.PatternRuleParameters{Name: github.Ptr(""), Negate: github.Ptr(false), Operator: "starts_with", Pattern: "feat"}
		}
		cases[typ] = modelledRuleCase{
			cr: func(r *v1alpha1.Rules) {
				*p.cr(r) = &v1alpha1.RulesPattern{Operator: "starts_with", Pattern: "feat"}
			},
			sent: withDefaults,
			onGitHub: []func(*rulesets.ModelledRules){
				withDefaults,
				func(r *rulesets.ModelledRules) {
					*p.gh(r) = &rulesets.PatternRuleParameters{Operator: "starts_with", Pattern: "feat"}
				},
			},
			differs: map[string]func(*rulesets.ModelledRules){
				"name":     func(r *rulesets.ModelledRules) { (*p.gh(r)).Name = github.Ptr("conventional") },
				"negate":   func(r *rulesets.ModelledRules) { (*p.gh(r)).Negate = github.Ptr(true) },
				"operator": func(r *rulesets.ModelledRules) { (*p.gh(r)).Operator = "ends_with" },
				"pattern":  func(r *rulesets.ModelledRules) { (*p.gh(r)).Pattern = "fix" },
			},
		}
	}
	return cases
}

func withRules(f func(*v1alpha1.Rules)) repositoryModifier {
	return func(cr *v1alpha1.Repository) { f((*cr.Spec.ForProvider.RepositoryRules)[0].Rules) }
}

// rulesetHolding makes GitHub return the CR's ruleset with the given rule changes applied.
func rulesetHolding(t *testing.T, rs *fake.MockRulesetsClient, changes ...func(*rulesets.ModelledRules)) {
	t.Helper()
	rules := githubRules()
	for _, change := range changes {
		change(rules)
	}
	encoded, err := rules.Encode()
	if err != nil {
		t.Fatalf("encode rules: %v", err)
	}
	rulesetWithRules(rs, encoded)
}

// rulesetWithRules makes GitHub return the CR's ruleset holding exactly the given rules.
func rulesetWithRules(rs *fake.MockRulesetsClient, rules []*rulesets.Rule) {
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		r := githubRuleset()[0]
		r.Rules = rules
		return r, fake.GenerateEmptyResponse(), nil
	}
}

// sentRules runs updateRepositoryRules and returns the rules of its one UpdateRuleset call.
func sentRules(t *testing.T, rs *fake.MockRulesetsClient, cr *v1alpha1.Repository) []*rulesets.Rule {
	t.Helper()
	var sent [][]*rulesets.Rule
	rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		sent = append(sent, r.Rules)
		return &r, fake.GenerateEmptyResponse(), nil
	}
	if err := updateRepositoryRules(context.Background(), cr, clientFor(upToDateRepositories(nil), rs), repo); err != nil {
		t.Fatalf("updateRepositoryRules: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("UpdateRuleset called %d times, want 1", len(sent))
	}
	return sent[0]
}

// sentParameters returns the parameters of the rule of type typ in rules.
func sentParameters(t *testing.T, rules []*rulesets.Rule, typ string) string {
	t.Helper()
	types := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.Type == typ {
			return string(r.Parameters)
		}
		types = append(types, r.Type)
	}
	t.Fatalf("no %s rule sent, only %v", typ, types)
	return ""
}

// crRulesets is getRepositoryRulesMapFromCr for rulesets that must convert without error.
func crRulesets(t *testing.T, rules []v1alpha1.RepositoryRuleset) map[string]v1alpha1.RepositoryRuleset {
	t.Helper()
	m, err := getRepositoryRulesMapFromCr(rules)
	if err != nil {
		t.Fatalf("getRepositoryRulesMapFromCr: %v", err)
	}
	return m
}

// rulesetsWithoutWrites fails the test on any ruleset write and reports the ruleset as on GitHub.
func rulesetsWithoutWrites(t *testing.T) *fake.MockRulesetsClient {
	rs := upToDateRulesets()
	rs.MockCreateRuleset = func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("CreateRuleset called")
		return &r, fake.GenerateEmptyResponse(), nil
	}
	rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, id int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("UpdateRuleset called")
		return &r, fake.GenerateEmptyResponse(), nil
	}
	rs.MockDeleteRuleset = func(ctx context.Context, owner, repo string, id int64) (*github.Response, error) {
		t.Errorf("DeleteRuleset called")
		return fake.GenerateEmptyResponse(), nil
	}
	return rs
}

// updatedRules is sentRules decoded into the rules the provider models.
func updatedRules(t *testing.T, rs *fake.MockRulesetsClient, cr *v1alpha1.Repository) *rulesets.ModelledRules {
	t.Helper()
	got, err := rulesets.Decode(sentRules(t, rs, cr))
	if err != nil {
		t.Fatalf("decode sent rules: %v", err)
	}
	return got
}

// A declared rule is sent with every field, defaults filled in and sets sorted,
// alongside the rules already on the ruleset.
func TestUpdateSendsModelledRules(t *testing.T) {
	for typ, tc := range modelledRuleCases() {
		t.Run(typ, func(t *testing.T) {
			got := updatedRules(t, upToDateRulesets(), repository(withRules(tc.cr)))

			want := githubRules()
			if tc.sentNew != nil {
				tc.sentNew(want)
			} else {
				tc.sent(want)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("UpdateRuleset rules: -want, +got:\n%s", diff)
			}
		})
	}
}

// A CR setting only required fields is up to date against GitHub's defaults in any list
// order. A real difference is drift, and the update sends the CR's values.
func TestObserveModelledRules(t *testing.T) {
	for typ, tc := range modelledRuleCases() {
		t.Run(typ, func(t *testing.T) {
			cr := repository(withRules(tc.cr))
			for i, onGitHub := range tc.onGitHub {
				rs := upToDateRulesets()
				rulesetHolding(t, rs, onGitHub)
				got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr.DeepCopy())
				if err != nil {
					t.Fatalf("Observe: %v", err)
				}
				if !got.ResourceUpToDate {
					t.Errorf("GitHub form %d: ResourceUpToDate = false, want true: GitHub holds what the CR declares", i)
				}
			}
			for field, change := range tc.differs {
				rs := upToDateRulesets()
				rulesetHolding(t, rs, tc.onGitHub[0], change)
				got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr.DeepCopy())
				if err != nil {
					t.Fatalf("Observe: %v", err)
				}
				if got.ResourceUpToDate {
					t.Errorf("%s differs on GitHub: ResourceUpToDate = true, want false", field)
				}
				want := githubRules()
				tc.sent(want)
				if diff := cmp.Diff(want, updatedRules(t, rs, cr.DeepCopy())); diff != "" {
					t.Errorf("%s differs on GitHub: UpdateRuleset rules: -want, +got:\n%s", field, diff)
				}
			}
		})
	}
}

// A modelled rule held only on GitHub is drift, and the update removes it.
func TestModelledRuleOnlyOnGitHubIsRemoved(t *testing.T) {
	for typ, tc := range modelledRuleCases() {
		t.Run(typ, func(t *testing.T) {
			rs := upToDateRulesets()
			rulesetHolding(t, rs, tc.onGitHub[0])

			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = true, want false: GitHub holds %s, which the CR does not declare", typ)
			}

			sent := updatedRules(t, rs, repository())
			if diff := cmp.Diff(githubRules(), sent); diff != "" {
				t.Errorf("UpdateRuleset rules: -want, +got:\n%s", diff)
			}
		})
	}
}

// pullRequestOnGitHub is the CR's ruleset as GitHub returns it with a pull_request rule
// (approval count 2) and a merge_queue rule, each holding an unmodelled parameter.
func pullRequestOnGitHub() []*rulesets.Rule {
	return []*rulesets.Rule{
		{Type: "creation"},
		{Type: "deletion"},
		{Type: "update"},
		{Type: "required_linear_history"},
		{Type: "required_signatures"},
		{Type: "non_fast_forward"},
		{Type: "pull_request", Parameters: json.RawMessage(`{
			"allowed_merge_methods": ["merge", "squash", "rebase"],
			"dismiss_stale_reviews_on_push": true,
			"dismissal_restriction": {"allowed_actors": [], "enabled": false},
			"ignore_approvals_from_contributors": true,
			"require_code_owner_review": false,
			"require_extra_approval_for_unattributed_changes": true,
			"require_last_push_approval": false,
			"required_approving_review_count": 2,
			"required_review_thread_resolution": false,
			"required_reviewers": []
		}`)},
		{Type: "merge_queue", Parameters: json.RawMessage(`{
			"actor_controlled_merging": true,
			"check_response_timeout_minutes": 60,
			"grouping_strategy": "ALLGREEN",
			"max_entries_to_build": 5,
			"max_entries_to_merge": 5,
			"merge_method": "MERGE",
			"min_entries_to_merge": 1,
			"min_entries_to_merge_wait_minutes": 5
		}`)},
	}
}

// withMergeQueueRule declares the merge_queue rule pullRequestOnGitHub holds.
func withMergeQueueRule() repositoryModifier {
	return withRules(func(r *v1alpha1.Rules) {
		r.MergeQueue = &v1alpha1.RulesMergeQueue{
			CheckResponseTimeoutMinutes:  60,
			GroupingStrategy:             "ALLGREEN",
			MaxEntriesToBuild:            5,
			MaxEntriesToMerge:            5,
			MergeMethod:                  "MERGE",
			MinEntriesToMerge:            1,
			MinEntriesToMergeWaitMinutes: 5,
		}
	})
}

// withPullRequestRule declares a pull_request rule requiring approvingReviews approvals
// and dismissing stale reviews.
func withPullRequestRule(approvingReviews int) repositoryModifier {
	return withRules(func(r *v1alpha1.Rules) {
		r.PullRequest = &v1alpha1.RulesPullRequest{
			DismissStaleReviewsOnPush:    github.Ptr(true),
			RequiredApprovingReviewCount: github.Ptr(approvingReviews),
		}
	})
}

// requireExtraApprovalForUnattributedChanges is undocumented, so an update carries it
// only when the CR sets it or GitHub returns it. An unset field means GitHub's default,
// true: once GitHub returns the field, a false there is drift and the update sends true.
func TestRequireExtraApprovalForUnattributedChanges(t *testing.T) {
	const (
		withTrue  = `{"allowed_merge_methods":["merge","rebase","squash"],"dismiss_stale_reviews_on_push":true,"dismissal_restriction":{"allowed_actors":[],"enabled":false},"require_code_owner_review":false,"require_extra_approval_for_unattributed_changes":true,"require_last_push_approval":false,"required_approving_review_count":1,"required_review_thread_resolution":false,"required_reviewers":[]}`
		withFalse = `{"allowed_merge_methods":["merge","rebase","squash"],"dismiss_stale_reviews_on_push":true,"dismissal_restriction":{"allowed_actors":[],"enabled":false},"require_code_owner_review":false,"require_extra_approval_for_unattributed_changes":false,"require_last_push_approval":false,"required_approving_review_count":1,"required_review_thread_resolution":false,"required_reviewers":[]}`
		without   = `{"allowed_merge_methods":["merge","rebase","squash"],"dismiss_stale_reviews_on_push":true,"dismissal_restriction":{"allowed_actors":[],"enabled":false},"require_code_owner_review":false,"require_last_push_approval":false,"required_approving_review_count":1,"required_review_thread_resolution":false,"required_reviewers":[]}`
	)
	cases := map[string]struct {
		cr           *bool
		onGitHub     *bool
		wantUpToDate bool
		wantSent     string
	}{
		"CRTrueGitHubFalse":   {cr: github.Ptr(true), onGitHub: github.Ptr(false), wantSent: withTrue},
		"CRFalseGitHubAbsent": {cr: github.Ptr(false), wantSent: withFalse},
		"CRUnsetGitHubTrue":   {onGitHub: github.Ptr(true), wantUpToDate: true, wantSent: withTrue},
		"CRUnsetGitHubFalse":  {onGitHub: github.Ptr(false), wantSent: withTrue},
		"CRUnsetGitHubAbsent": {wantUpToDate: true, wantSent: without},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := repository(withRules(func(r *v1alpha1.Rules) {
				r.PullRequest = &v1alpha1.RulesPullRequest{
					DismissStaleReviewsOnPush:                  github.Ptr(true),
					RequiredApprovingReviewCount:               github.Ptr(1),
					RequireExtraApprovalForUnattributedChanges: tc.cr,
				}
			}))
			onGitHub := func(dismissStale bool) func(*rulesets.ModelledRules) {
				return func(m *rulesets.ModelledRules) {
					m.PullRequest = &rulesets.PullRequestRuleParameters{
						AllowedMergeMethods:          []string{"merge", "squash", "rebase"},
						DismissStaleReviewsOnPush:    dismissStale,
						DismissalRestriction:         &rulesets.DismissalRestriction{AllowedActors: []*rulesets.Actor{}},
						RequiredApprovingReviewCount: 1,
						RequiredReviewers:            []*rulesets.RequiredReviewer{},

						RequireExtraApprovalForUnattributedChanges: tc.onGitHub,
					}
				}
			}

			rs := upToDateRulesets()
			rulesetHolding(t, rs, onGitHub(true))
			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr.DeepCopy())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.wantUpToDate {
				t.Errorf("ResourceUpToDate = %v, want %v", got.ResourceUpToDate, tc.wantUpToDate)
			}

			// dismiss_stale_reviews_on_push differs too, so every case sends an update.
			rs = upToDateRulesets()
			rulesetHolding(t, rs, onGitHub(false))
			if got := sentParameters(t, sentRules(t, rs, cr.DeepCopy()), "pull_request"); got != tc.wantSent {
				t.Errorf("pull_request parameters sent:\nwant %s\n got %s", tc.wantSent, got)
			}
		})
	}
}

// A new ruleset with the field unset is created without it, by the update and with the
// repository, and GitHub applies its default, true.
func TestRequireExtraApprovalForUnattributedChangesUnsetOnCreate(t *testing.T) {
	want := `{"allowed_merge_methods":["merge","rebase","squash"],"dismiss_stale_reviews_on_push":false,"dismissal_restriction":{"allowed_actors":[],"enabled":false},"require_code_owner_review":false,"require_last_push_approval":false,"required_approving_review_count":0,"required_review_thread_resolution":false,"required_reviewers":[]}`
	var created []rulesets.Ruleset
	rs := upToDateRulesets()
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		return nil, fake.GenerateEmptyResponse(), nil
	}
	rs.MockCreateRuleset = func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		created = append(created, r)
		return &r, fake.GenerateEmptyResponse(), nil
	}
	withPullRequest := withRules(func(r *v1alpha1.Rules) { r.PullRequest = &v1alpha1.RulesPullRequest{} })

	if err := updateRepositoryRules(context.Background(), repository(withPullRequest), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
		t.Fatalf("updateRepositoryRules: %v", err)
	}
	repos := &fake.MockRepositoriesClient{
		MockCreate: func(ctx context.Context, owner string, r *github.Repository) (*github.Repository, *github.Response, error) {
			return r, fake.GenerateEmptyResponse(), nil
		},
		MockAddCollaborator: func(ctx context.Context, owner, r, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), nil
		},
		MockReplaceAllTopics: func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
			return topics, fake.GenerateEmptyResponse(), nil
		},
	}
	teams := &fake.MockTeamsClient{
		MockAddTeamRepoBySlug: func(ctx context.Context, org, slug, owner, r string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
			return fake.GenerateEmptyResponse(), nil
		},
	}
	cr := repository(withPullRequest)
	cr.Spec.ForProvider.Webhooks = nil
	cr.Spec.ForProvider.BranchProtectionRules = nil
	e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repos, Teams: teams, Rulesets: rs}}}
	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(created) != 2 {
		t.Fatalf("CreateRuleset calls = %d, want 2", len(created))
	}
	for i, path := range []string{"update", "create"} {
		if got := sentParameters(t, created[i].Rules, "pull_request"); got != want {
			t.Errorf("pull_request parameters sent by %s:\nwant %s\n got %s", path, want, got)
		}
	}
}

// An update sends only the parameters built from the CR, so it resets unmodelled
// parameters set on GitHub.
func TestUpdateSendsOnlyCRParameters(t *testing.T) {
	rs := upToDateRulesets()
	rulesetWithRules(rs, pullRequestOnGitHub())

	sent := sentRules(t, rs, repository(withPullRequestRule(1), withMergeQueueRule()))

	wantPR := `{"allowed_merge_methods":["merge","rebase","squash"],"dismiss_stale_reviews_on_push":true,"dismissal_restriction":{"allowed_actors":[],"enabled":false},"require_code_owner_review":false,"require_extra_approval_for_unattributed_changes":true,"require_last_push_approval":false,"required_approving_review_count":1,"required_review_thread_resolution":false,"required_reviewers":[]}`
	if got := sentParameters(t, sent, "pull_request"); got != wantPR {
		t.Errorf("pull_request parameters sent:\nwant %s\n got %s", wantPR, got)
	}
	wantMQ := `{"check_response_timeout_minutes":60,"grouping_strategy":"ALLGREEN","max_entries_to_build":5,"max_entries_to_merge":5,"merge_method":"MERGE","min_entries_to_merge":1,"min_entries_to_merge_wait_minutes":5}`
	if got := sentParameters(t, sent, "merge_queue"); got != wantMQ {
		t.Errorf("merge_queue parameters sent:\nwant %s\n got %s", wantMQ, got)
	}
}

// Observe compares modelled parameters only, so a CR matching them is up to date.
func TestObserveIgnoresUnmodelledParameters(t *testing.T) {
	rs := upToDateRulesets()
	rulesetWithRules(rs, pullRequestOnGitHub())

	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository(withPullRequestRule(2), withMergeQueueRule()))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: GitHub differs from the CR only in parameters the provider does not model")
	}
}

// The next update resets parameters the provider has no field for, so Observe reports
// them on RulesetsPartial and the ruleset_parameters gauge on every Observe. They are
// not drift: GitHub may return a new parameter on every read, and drift would update
// the ruleset on every poll. Once GitHub holds only modelled parameters, both clear.
func TestObserveUnmanagedParametersSetRulesetsPartial(t *testing.T) {
	metrics := telemetry.NewForTest()
	gauge := metrics.RepositoryUnreconcilableForTest()
	rs := upToDateRulesets()
	rulesetWithRules(rs, pullRequestOnGitHub())
	cr := repository(withPullRequestRule(2), withMergeQueueRule())
	cr.Spec.ForProvider.Org = "acme"
	e := external{github: clientFor(upToDateRepositories(nil), rs), metrics: metrics}
	want := "ruleset test-ruleset-1: rule merge_queue has parameter actor_controlled_merging that this provider does not manage; GitHub resets it when the ruleset is next updated; " +
		"ruleset test-ruleset-1: rule pull_request has parameter ignore_approvals_from_contributors that this provider does not manage; GitHub resets it when the ruleset is next updated"

	for observe := 1; observe <= 2; observe++ {
		got, err := e.Observe(context.Background(), cr)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if !got.ResourceUpToDate {
			t.Errorf("Observe %d: ResourceUpToDate = false, want true: unmanaged parameters are not drift", observe)
		}
		condition := cr.GetCondition(typeRulesetsPartial)
		if condition.Status != corev1.ConditionTrue || condition.Reason != reasonUnmanagedParameters || condition.Message != want {
			t.Errorf("Observe %d: RulesetsPartial = %v/%v %q, want True/%v %q", observe, condition.Status, condition.Reason, condition.Message, reasonUnmanagedParameters, want)
		}
		if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionRulesetParameters)); got != 1 {
			t.Errorf("Observe %d: unreconcilable{dimension=ruleset_parameters} = %v, want 1", observe, got)
		}
		if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionRulesetsForbidden)); got != 0 {
			t.Errorf("Observe %d: unreconcilable{dimension=rulesets_forbidden} = %v, want 0: GitHub lists the rulesets", observe, got)
		}
	}

	modelled := upToDateRulesets()
	rulesetHolding(t, modelled, func(m *rulesets.ModelledRules) {
		m.PullRequest = &rulesets.PullRequestRuleParameters{DismissStaleReviewsOnPush: true, RequiredApprovingReviewCount: 2}
	})
	cr.Spec.ForProvider = repository(withPullRequestRule(2)).Spec.ForProvider
	cr.Spec.ForProvider.Org = "acme"
	e = external{github: clientFor(upToDateRepositories(nil), modelled), metrics: metrics}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := cr.GetCondition(typeRulesetsPartial); got.Status != corev1.ConditionFalse || got.Message != "" {
		t.Errorf("RulesetsPartial with only modelled parameters = %v %q, want False with no message", got.Status, got.Message)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionRulesetParameters)); got != 0 {
		t.Errorf("unreconcilable{dimension=ruleset_parameters} with only modelled parameters = %v, want 0", got)
	}
}

// The next update resets a nested parameter as well, so RulesetsPartial names it by its
// path inside the rule's parameters.
func TestObserveNestedUnmanagedParameterNamesItsPath(t *testing.T) {
	rs := upToDateRulesets()
	rulesetWithRules(rs, append(pullRequestOnGitHub()[:6], &rulesets.Rule{Type: "pull_request", Parameters: json.RawMessage(`{
		"dismiss_stale_reviews_on_push": true,
		"dismissal_restriction": {"allowed_actors": [], "enabled": false, "foo": true},
		"required_approving_review_count": 2
	}`)}))
	cr := repository(withPullRequestRule(2))
	want := "ruleset test-ruleset-1: rule pull_request has parameter dismissal_restriction.foo that this provider does not manage; GitHub resets it when the ruleset is next updated"

	if _, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := cr.GetCondition(typeRulesetsPartial); got.Status != corev1.ConditionTrue || got.Reason != reasonUnmanagedParameters || got.Message != want {
		t.Errorf("RulesetsPartial = %v/%v %q, want True/%v %q", got.Status, got.Reason, got.Message, reasonUnmanagedParameters, want)
	}
}

// A surplus ruleset is deleted, never updated, so its parameters leave RulesetsPartial False.
func TestObserveSurplusRulesetParametersLeaveRulesetsPartialFalse(t *testing.T) {
	surplus := func() *rulesets.Ruleset {
		return &rulesets.Ruleset{
			ID:          github.Ptr(githubOnlyRulesetID),
			Name:        "github-only",
			Enforcement: rr1enforcement,
			Rules:       []*rulesets.Rule{{Type: "merge_queue", Parameters: json.RawMessage(`{"actor_controlled_merging":true}`)}},
		}
	}
	rs := upToDateRulesets()
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		return append(githubRuleset(), surplus()), fake.GenerateEmptyResponse(), nil
	}
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		if rulesetID == githubOnlyRulesetID {
			return surplus(), fake.GenerateEmptyResponse(), nil
		}
		return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
	}
	metrics := telemetry.NewForTest()
	cr := repository()
	cr.Spec.ForProvider.Org = "acme"

	if _, err := (&external{github: clientFor(upToDateRepositories(nil), rs), metrics: metrics}).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := cr.GetCondition(typeRulesetsPartial).Status; got != corev1.ConditionFalse {
		t.Errorf("RulesetsPartial = %v, want False", got)
	}
	if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, telemetry.DimensionRulesetParameters)); got != 0 {
		t.Errorf("unreconcilable{dimension=ruleset_parameters} = %v, want 0", got)
	}
}

// One sentence names a rule's unmanaged parameters, in the singular or the plural.
func TestUnmanagedParametersSentence(t *testing.T) {
	cases := map[string]struct {
		keys []string
		want string
	}{
		"One": {
			keys: []string{"a"},
			want: "ruleset main: rule merge_queue has parameter a that this provider does not manage; GitHub resets it when the ruleset is next updated",
		},
		"Several": {
			keys: []string{"a", "b"},
			want: "ruleset main: rule merge_queue has parameters a, b that this provider does not manage; GitHub resets them when the ruleset is next updated",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := unmanagedParametersSentence("main", "merge_queue", tc.keys); got != tc.want {
				t.Errorf("unmanagedParametersSentence:\nwant %s\n got %s", tc.want, got)
			}
		})
	}
}

// A required reviewer id that GitHub returns as a JSON string matches the CR's number.
func TestObserveRequiredReviewerIDAsString(t *testing.T) {
	rs := upToDateRulesets()
	rules := githubRuleset()[0].Rules
	rulesetWithRules(rs, append(rules, &rulesets.Rule{Type: "pull_request", Parameters: json.RawMessage(`{
		"allowed_merge_methods": ["merge", "squash", "rebase"],
		"dismiss_stale_reviews_on_push": false,
		"dismissal_restriction": {"allowed_actors": [], "enabled": false},
		"require_code_owner_review": false,
		"require_extra_approval_for_unattributed_changes": true,
		"require_last_push_approval": false,
		"required_approving_review_count": 0,
		"required_review_thread_resolution": false,
		"required_reviewers": [{"file_patterns": ["*.go"], "minimum_approvals": 1, "reviewer": {"id": "2002", "type": "Team"}}]
	}`)}))
	cr := repository(withRules(func(r *v1alpha1.Rules) {
		r.PullRequest = &v1alpha1.RulesPullRequest{RequiredReviewers: []*v1alpha1.RulesRequiredReviewer{
			{FilePatterns: []string{"*.go"}, MinimumApprovals: 1, Reviewer: v1alpha1.RulesReviewer{ID: 2002, Type: "Team"}},
		}}
	}))

	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: GitHub holds the declared reviewer, its id as a string")
	}
}

// withBypassActors replaces the CR ruleset's bypass actors.
func withBypassActors(actors ...*v1alpha1.RulesetByPassActors) repositoryModifier {
	return func(cr *v1alpha1.Repository) { (*cr.Spec.ForProvider.RepositoryRules)[0].BypassActors = actors }
}

// rulesetWithBypassActors makes GitHub return the CR's ruleset with the given bypass actors.
func rulesetWithBypassActors(rs *fake.MockRulesetsClient, actors ...*rulesets.BypassActor) {
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		r := githubRuleset()[0]
		r.BypassActors = actors
		return r, fake.GenerateEmptyResponse(), nil
	}
}

// OrganizationAdmin and DeployKey actors match GitHub's null actor_id with or without a
// CR actorId, and are sent as declared. Other actors match by ID, in any order.
func TestObserveBypassActorsWithoutID(t *testing.T) {
	team := &rulesets.BypassActor{ActorID: github.Ptr(rr1actorId), ActorType: github.Ptr("Team"), BypassMode: github.Ptr("always")}
	orgAdmin := &rulesets.BypassActor{ActorType: github.Ptr("OrganizationAdmin"), BypassMode: github.Ptr("always")}
	deployKey := &rulesets.BypassActor{ActorType: github.Ptr("DeployKey"), BypassMode: github.Ptr("exempt")}
	crActor := func(id *int64, typ, mode string) *v1alpha1.RulesetByPassActors {
		return &v1alpha1.RulesetByPassActors{ActorID: id, ActorType: github.Ptr(typ), BypassMode: github.Ptr(mode)}
	}

	cases := map[string]struct {
		cr       *v1alpha1.Repository
		upToDate bool
	}{
		"IDsDeclared": {
			cr:       repository(withBypassActors(crActor(github.Ptr(int64(1)), "OrganizationAdmin", "always"), crActor(github.Ptr(int64(7)), "DeployKey", "exempt"), crActor(github.Ptr(rr1actorId), "Team", "always"))),
			upToDate: true,
		},
		"IDsUnset": {
			cr:       repository(withBypassActors(crActor(github.Ptr(rr1actorId), "Team", "always"), crActor(nil, "DeployKey", "exempt"), crActor(nil, "OrganizationAdmin", "always"))),
			upToDate: true,
		},
		"TeamIDDiffers": {
			cr: repository(withBypassActors(crActor(nil, "OrganizationAdmin", "always"), crActor(nil, "DeployKey", "exempt"), crActor(github.Ptr(rr1actorId+1), "Team", "always"))),
		},
		"ModeDiffers": {
			cr: repository(withBypassActors(crActor(nil, "OrganizationAdmin", "pull_request"), crActor(nil, "DeployKey", "exempt"), crActor(github.Ptr(rr1actorId), "Team", "always"))),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			rulesetWithBypassActors(rs, orgAdmin, team, deployKey)

			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), tc.cr)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.upToDate {
				t.Errorf("ResourceUpToDate = %v, want %v", got.ResourceUpToDate, tc.upToDate)
			}
		})
	}

	withID := func(a *rulesets.BypassActor, id int64) *rulesets.BypassActor {
		c := *a
		c.ActorID = &id
		return &c
	}
	for name, want := range map[string][]*rulesets.BypassActor{
		"IDsDeclared": {withID(deployKey, 7), withID(orgAdmin, 1), team},
		"IDsUnset":    {deployKey, orgAdmin, team},
	} {
		sent, err := crRepoRulesToRulesConfig(crRulesets(t, *cases[name].cr.Spec.ForProvider.RepositoryRules)[rr1name])
		if err != nil {
			t.Fatalf("crRepoRulesToRulesConfig: %v", err)
		}
		if diff := cmp.Diff(want, sent.BypassActors); diff != "" {
			t.Errorf("%s: bypass actors sent: -want, +got:\n%s", name, diff)
		}
	}
}

// A bypass actor without a mode is sent as "always" and matches what GitHub stores.
// Listed again with mode "always", it is the same actor twice, which Observe reports
// on RulesetsPartial before it reads any ruleset.
func TestBypassActorModeDefaultAndDuplicates(t *testing.T) {
	cr := repository(withBypassActors(
		&v1alpha1.RulesetByPassActors{ActorID: github.Ptr(rr1actorId), ActorType: github.Ptr("Team")},
	))
	rs := upToDateRulesets()

	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: GitHub holds the team with bypass_mode always")
	}

	sent, err := crRepoRulesToRulesConfig(crRulesets(t, *cr.Spec.ForProvider.RepositoryRules)[rr1name])
	if err != nil {
		t.Fatalf("crRepoRulesToRulesConfig: %v", err)
	}
	want := []*rulesets.BypassActor{{ActorID: github.Ptr(rr1actorId), ActorType: github.Ptr("Team"), BypassMode: github.Ptr("always")}}
	if diff := cmp.Diff(want, sent.BypassActors); diff != "" {
		t.Errorf("bypass actors sent: -want, +got:\n%s", diff)
	}

	twice := repository(withBypassActors(
		&v1alpha1.RulesetByPassActors{ActorID: github.Ptr(rr1actorId), ActorType: github.Ptr("Team")},
		&v1alpha1.RulesetByPassActors{ActorID: github.Ptr(rr1actorId), ActorType: github.Ptr("Team"), BypassMode: github.Ptr("always")},
	))
	if _, err = (&external{github: clientFor(upToDateRepositories(nil), rulesetsWithoutCalls(t))}).Observe(context.Background(), twice); err != nil {
		t.Fatalf("Observe with the actor twice: %v", err)
	}
	wantMsg := fmt.Sprintf("ruleset %s lists bypass actor Team %d twice with bypassMode always", rr1name, rr1actorId)
	if c := twice.GetCondition(typeRulesetsPartial); c.Status != corev1.ConditionTrue || c.Reason != reasonInvalidRulesets || c.Message != wantMsg {
		t.Errorf("RulesetsPartial = %v/%v %q, want True/%v %q", c.Status, c.Reason, c.Message, reasonInvalidRulesets, wantMsg)
	}
}

// A status check's integration_id is always compared: GitHub keeps a check declared by
// context alone without an id, so an id on GitHub that the CR leaves unset is drift, and
// the update sends the check without one, which clears it.
func TestObserveStatusCheckIntegrationID(t *testing.T) {
	cases := map[string]struct {
		cr       *int64
		onGitHub *int64
		upToDate bool
		sent     string // parameters sent on update, when there is drift
	}{
		"UnsetAgainstUnset": {upToDate: true},
		"UnsetAgainstSet":   {onGitHub: github.Ptr(int64(15368)), sent: `{"do_not_enforce_on_create":false,"required_status_checks":[{"context":"ci"}],"strict_required_status_checks_policy":false}`},
		"SetEqual":          {cr: github.Ptr(int64(15368)), onGitHub: github.Ptr(int64(15368)), upToDate: true},
		"SetDiffers":        {cr: github.Ptr(int64(1)), onGitHub: github.Ptr(int64(15368)), sent: `{"do_not_enforce_on_create":false,"required_status_checks":[{"context":"ci","integration_id":1}],"strict_required_status_checks_policy":false}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			rulesetHolding(t, rs, func(r *rulesets.ModelledRules) {
				r.RequiredStatusChecks = &rulesets.RequiredStatusChecksRuleParameters{RequiredStatusChecks: []*rulesets.StatusCheck{
					{Context: "ci", IntegrationID: tc.onGitHub},
				}}
			})
			cr := repository(withRules(func(r *v1alpha1.Rules) {
				r.RequiredStatusChecks = &v1alpha1.RulesRequiredStatusChecks{RequiredStatusChecks: []*v1alpha1.RulesRequiredStatusChecksParameters{
					{Context: "ci", IntegrationID: tc.cr},
				}}
			}))
			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr.DeepCopy())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.upToDate {
				t.Errorf("ResourceUpToDate = %v, want %v", got.ResourceUpToDate, tc.upToDate)
			}
			if tc.upToDate {
				return
			}
			if got := sentParameters(t, sentRules(t, rs, cr), "required_status_checks"); got != tc.sent {
				t.Errorf("parameters sent = %s, want %s", got, tc.sent)
			}
		})
	}
}

// secretTypes defaults to provider_patterns, is always sent, and is compared in any order.
func TestSecretScanningAlertResolutionSecretTypes(t *testing.T) {
	onGitHub := func(types ...string) func(*rulesets.ModelledRules) {
		return func(r *rulesets.ModelledRules) {
			r.RequireSecretScanningAlertResolution = &rulesets.RequireSecretScanningAlertResolutionRuleParameters{SecretTypes: types}
		}
	}
	withTypes := func(types ...v1alpha1.RulesSecretType) repositoryModifier {
		return withRules(func(r *v1alpha1.Rules) {
			r.RequireSecretScanningAlertResolution = &v1alpha1.RulesSecretScanningAlertResolution{SecretTypes: types}
		})
	}
	cases := map[string]struct {
		cr       *v1alpha1.Repository
		onGitHub []string
		upToDate bool
		sent     string // parameters sent on update
	}{
		"UnsetAgainstDefault": {cr: repository(withTypes()), onGitHub: []string{"provider_patterns"}, upToDate: true, sent: `{"secret_types":["provider_patterns"]}`},
		"UnsetAgainstOther":   {cr: repository(withTypes()), onGitHub: []string{"generic_patterns", "custom_patterns"}, sent: `{"secret_types":["provider_patterns"]}`},
		"EmptyAgainstDefault": {cr: repository(withTypes([]v1alpha1.RulesSecretType{}...)), onGitHub: []string{"provider_patterns"}, upToDate: true, sent: `{"secret_types":["provider_patterns"]}`},
		"SetSameInOtherOrder": {cr: repository(withTypes("generic_patterns", "custom_patterns")), onGitHub: []string{"custom_patterns", "generic_patterns"}, upToDate: true, sent: `{"secret_types":["custom_patterns","generic_patterns"]}`},
		"SetDiffers":          {cr: repository(withTypes("custom_patterns")), onGitHub: []string{"provider_patterns"}, sent: `{"secret_types":["custom_patterns"]}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			rulesetHolding(t, rs, onGitHub(tc.onGitHub...))
			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), tc.cr.DeepCopy())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.upToDate {
				t.Errorf("ResourceUpToDate = %v, want %v", got.ResourceUpToDate, tc.upToDate)
			}

			// Force an update with another change, to see what is sent for the rule.
			cr := tc.cr.DeepCopy()
			(*cr.Spec.ForProvider.RepositoryRules)[0].Rules.Creation = github.Ptr(false)
			if got := sentParameters(t, sentRules(t, rs, cr), "require_secret_scanning_alert_resolution"); got != tc.sent {
				t.Errorf("parameters sent = %s, want %s", got, tc.sent)
			}
		})
	}
}

// update_allows_fetch_and_merge is sent whenever the update rule is on, false when unset.
// It is compared when GitHub returns it, which GitHub does on forks only.
func TestUpdateAllowsFetchAndMerge(t *testing.T) {
	withFetchAndMerge := func(v *bool) repositoryModifier {
		return withRules(func(r *v1alpha1.Rules) { r.UpdateAllowsFetchAndMerge = v })
	}
	cases := map[string]struct {
		cr       *bool
		onGitHub string // update parameters GitHub returns; "" for none
		upToDate bool
		sent     string
	}{
		"UnsetAgainstFalse":  {onGitHub: `{"update_allows_fetch_and_merge":false}`, upToDate: true, sent: `{"update_allows_fetch_and_merge":false}`},
		"UnsetAgainstAbsent": {upToDate: true, sent: `{"update_allows_fetch_and_merge":false}`},
		"TrueAgainstAbsent":  {cr: github.Ptr(true), upToDate: true, sent: `{"update_allows_fetch_and_merge":true}`},
		"UnsetAgainstTrue":   {onGitHub: `{"update_allows_fetch_and_merge":true}`, sent: `{"update_allows_fetch_and_merge":false}`},
		"FalseAgainstAbsent": {cr: github.Ptr(false), upToDate: true, sent: `{"update_allows_fetch_and_merge":false}`},
		"TrueAgainstFalse":   {cr: github.Ptr(true), onGitHub: `{"update_allows_fetch_and_merge":false}`, sent: `{"update_allows_fetch_and_merge":true}`},
		"TrueAgainstTrue":    {cr: github.Ptr(true), onGitHub: `{"update_allows_fetch_and_merge":true}`, upToDate: true, sent: `{"update_allows_fetch_and_merge":true}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rules := githubRuleset()[0].Rules
			for _, r := range rules {
				if r.Type == "update" && tc.onGitHub != "" {
					r.Parameters = json.RawMessage(tc.onGitHub)
				}
			}
			rs := upToDateRulesets()
			rulesetWithRules(rs, rules)
			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository(withFetchAndMerge(tc.cr)))
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate != tc.upToDate {
				t.Errorf("ResourceUpToDate = %v, want %v", got.ResourceUpToDate, tc.upToDate)
			}

			// Force an update with another change, to see what is sent for the update rule.
			cr := repository(withFetchAndMerge(tc.cr), withRules(func(r *v1alpha1.Rules) { r.Creation = github.Ptr(false) }))
			if got := sentParameters(t, sentRules(t, rs, cr), "update"); got != tc.sent {
				t.Errorf("update parameters sent = %s, want %s", got, tc.sent)
			}
		})
	}

	// With the update rule off, updateAllowsFetchAndMerge has no effect.
	cr := repository(withRules(func(r *v1alpha1.Rules) {
		r.Update = github.Ptr(false)
		r.UpdateAllowsFetchAndMerge = github.Ptr(true)
	}))
	rs := upToDateRulesets()
	rulesetHolding(t, rs, func(r *rulesets.ModelledRules) { r.Update = nil })
	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: updateAllowsFetchAndMerge without update means nothing")
	}
}

const inheritedRulesetID int64 = 789

// withInheritedRuleset adds to GitHub's list an organization ruleset named name that
// holds an unmodelled rule.
func withInheritedRuleset(t *testing.T, rs *fake.MockRulesetsClient, name string) {
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		own := githubRuleset()
		own[0].SourceType = github.Ptr("Repository")
		inherited := &rulesets.Ruleset{ID: github.Ptr(inheritedRulesetID), Name: name, SourceType: github.Ptr("Organization"), Enforcement: rr1enforcement}
		return append(own, inherited), fake.GenerateEmptyResponse(), nil
	}
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		if rulesetID == inheritedRulesetID {
			t.Errorf("GetRuleset called for the inherited ruleset; it is not the repository's to compare")
			return &rulesets.Ruleset{ID: github.Ptr(inheritedRulesetID), Name: name, Rules: []*rulesets.Rule{{Type: "workflows"}}}, fake.GenerateEmptyResponse(), nil
		}
		return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
	}
	rs.MockDeleteRuleset = func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
		t.Errorf("DeleteRuleset(%d) called; the only ruleset not in the CR is inherited and must be left alone", rulesetID)
		return fake.GenerateEmptyResponse(), nil
	}
}

// Observe and Update touch only the repository's own rulesets, even when an inherited
// one shares a name with a declared ruleset.
func TestInheritedRulesetIsNotManaged(t *testing.T) {
	for name, inheritedName := range map[string]string{"OtherName": "org-policy", "SameNameAsCR": rr1name} {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			withInheritedRuleset(t, rs, inheritedName)

			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = false, want true: only an inherited ruleset is not in the CR")
			}

			var updated []int64
			rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
				updated = append(updated, rulesetID)
				return &r, fake.GenerateEmptyResponse(), nil
			}
			// Change the CR ruleset so Update runs.
			cr := repository(withRules(func(r *v1alpha1.Rules) { r.Creation = github.Ptr(false) }))
			if err := updateRepositoryRules(context.Background(), cr, clientFor(upToDateRepositories(nil), rs), repo); err != nil {
				t.Fatalf("updateRepositoryRules: %v", err)
			}
			if diff := cmp.Diff([]int64{rr1Id}, updated); diff != "" {
				t.Errorf("UpdateRuleset IDs: -want, +got:\n%s", diff)
			}
		})
	}
}

// A CR with no rules and no bypass actors matches a ruleset holding none, and the
// update sends both as [] to clear a ruleset holding some.
func TestRulesetClearsRulesAndBypassActors(t *testing.T) {
	cleared := func(actors []*v1alpha1.RulesetByPassActors) *v1alpha1.Repository {
		return repository(func(cr *v1alpha1.Repository) {
			(*cr.Spec.ForProvider.RepositoryRules)[0].BypassActors = actors
			(*cr.Spec.ForProvider.RepositoryRules)[0].Rules = nil
		})
	}
	for name, actors := range map[string][]*v1alpha1.RulesetByPassActors{"EmptyList": {}, "Unset": nil} {
		t.Run(name, func(t *testing.T) {
			empty := upToDateRulesets()
			empty.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
				r := githubRuleset()[0]
				r.BypassActors, r.Rules = []*rulesets.BypassActor{}, []*rulesets.Rule{}
				return r, fake.GenerateEmptyResponse(), nil
			}
			got, err := (&external{github: clientFor(upToDateRepositories(nil), empty)}).Observe(context.Background(), cleared(actors))
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = false, want true: neither side holds rules or bypass actors")
			}

			rs := upToDateRulesets()
			got, err = (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cleared(actors))
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = true, want false: GitHub holds rules and a bypass actor the CR does not")
			}
			var body []byte
			rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
				body, _ = json.Marshal(r)
				return &r, fake.GenerateEmptyResponse(), nil
			}
			if err := updateRepositoryRules(context.Background(), cleared(actors), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
				t.Fatalf("updateRepositoryRules: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatalf("unmarshal %s: %v", body, err)
			}
			for _, key := range []string{"rules", "bypass_actors"} {
				if string(fields[key]) != "[]" {
					t.Errorf("UpdateRuleset %s = %s, want []; body %s", key, fields[key], body)
				}
			}
		})
	}
}

// A push ruleset is sent "conditions": {} and is up to date whatever refName the CR
// declares, because GitHub stores its conditions as null.
func TestPushRulesetHasNoConditions(t *testing.T) {
	for name, conditions := range map[string]*rulesets.Conditions{"Null": nil, "Empty": {}} {
		t.Run(name, func(t *testing.T) { testPushRulesetHasNoConditions(t, conditions) })
	}
}

func testPushRulesetHasNoConditions(t *testing.T, conditions *rulesets.Conditions) {
	push := func(cr *v1alpha1.Repository) {
		(*cr.Spec.ForProvider.RepositoryRules)[0].Target = github.Ptr("push")
	}
	rs := upToDateRulesets()
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		r := githubRuleset()
		r[0].Target = github.Ptr("push")
		return r, fake.GenerateEmptyResponse(), nil
	}
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		r := githubRuleset()[0]
		r.Target, r.Conditions = github.Ptr("push"), conditions
		return r, fake.GenerateEmptyResponse(), nil
	}

	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository(push))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: conditions do not apply to a push ruleset")
	}

	var body []byte
	rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		body, _ = json.Marshal(r)
		return &r, fake.GenerateEmptyResponse(), nil
	}
	if err := updateRepositoryRules(context.Background(), repository(push, withRules(func(r *v1alpha1.Rules) { r.Creation = github.Ptr(false) })), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
		t.Fatalf("updateRepositoryRules: %v", err)
	}
	if !strings.Contains(string(body), `"conditions":{}`) {
		t.Errorf("UpdateRuleset body = %s, want \"conditions\":{} for a push ruleset", body)
	}
}

// Changing a ruleset's target to push is drift, and the update sends "conditions": {}
// to clear the old ref_name. A new push ruleset is created with "conditions": {} too.
func TestRulesetTargetChangeToPush(t *testing.T) {
	push := func(cr *v1alpha1.Repository) {
		(*cr.Spec.ForProvider.RepositoryRules)[0].Target = github.Ptr("push")
	}
	type sentBody struct {
		Target     *string         `json:"target"`
		Conditions json.RawMessage `json:"conditions"`
	}
	decode := func(t *testing.T, r rulesets.Ruleset) sentBody {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got sentBody
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		return got
	}

	t.Run("Update", func(t *testing.T) {
		// GitHub holds the fixture ruleset: target branch with a ref_name condition.
		rs := upToDateRulesets()
		got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository(push))
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if got.ResourceUpToDate {
			t.Errorf("ResourceUpToDate = true, want false: the CR changes the target to push")
		}
		var sent []sentBody
		rs.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			sent = append(sent, decode(t, r))
			return &r, fake.GenerateEmptyResponse(), nil
		}
		if err := updateRepositoryRules(context.Background(), repository(push), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
			t.Fatalf("updateRepositoryRules: %v", err)
		}
		if len(sent) != 1 || sent[0].Target == nil || *sent[0].Target != rulesetTargetPush || string(sent[0].Conditions) != "{}" {
			t.Errorf("UpdateRuleset requests = %+v, want one with target push and conditions {}", sent)
		}
	})

	t.Run("Create", func(t *testing.T) {
		rs := upToDateRulesets()
		rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), nil
		}
		var sent []sentBody
		rs.MockCreateRuleset = func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			sent = append(sent, decode(t, r))
			return &r, fake.GenerateEmptyResponse(), nil
		}
		if err := updateRepositoryRules(context.Background(), repository(push), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
			t.Fatalf("updateRepositoryRules: %v", err)
		}
		if len(sent) != 1 || sent[0].Target == nil || *sent[0].Target != rulesetTargetPush || string(sent[0].Conditions) != "{}" {
			t.Errorf("CreateRuleset requests = %+v, want one with target push and conditions {}", sent)
		}
	})
}

// A CR with conditions: {} matches a ruleset GitHub returns with {} or empty ref lists.
func TestObserveConditionsWithoutRefName(t *testing.T) {
	cr := repository(func(cr *v1alpha1.Repository) {
		(*cr.Spec.ForProvider.RepositoryRules)[0].Conditions = &v1alpha1.RulesetConditions{}
	})
	for name, conditions := range map[string]*rulesets.Conditions{
		"Empty":      {},
		"EmptyLists": {RefName: &rulesets.RefName{Include: []string{}, Exclude: []string{}}},
	} {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
				r := githubRuleset()[0]
				r.Conditions = conditions
				return r, fake.GenerateEmptyResponse(), nil
			}
			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr.DeepCopy())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = false, want true: neither side targets a ref")
			}
		})
	}
}

// A failed ruleset list fails Observe instead of reading as "no rulesets".
func TestObserveFailsWhenRulesetListFails(t *testing.T) {
	errBoom := errors.New("boom")
	rs := upToDateRulesets()
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		return nil, nil, errBoom
	}
	_, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository())
	if diff := cmp.Diff(errBoom, err, test.EquateErrors()); diff != "" {
		t.Errorf("Observe(...): -want error, +got error:\n%s", diff)
	}
}

// rulesetsForbidden answers the ruleset list with GitHub's 403 for a private repository
// on a plan without rulesets, and fails the test on any other ruleset call.
func rulesetsForbidden(t *testing.T) *fake.MockRulesetsClient {
	rs := rulesetsWithoutWrites(t)
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		return nil, nil, &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusForbidden}, Message: planWithoutRulesets}
	}
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("GetRuleset called after the list answered 403")
		return nil, nil, errors.New("unexpected GetRuleset")
	}
	return rs
}

const planWithoutRulesets = "Upgrade to GitHub Pro or make this repository public to enable this feature."

// rulesetsWithoutCalls fails the test on any ruleset call, the list included.
func rulesetsWithoutCalls(t *testing.T) *fake.MockRulesetsClient {
	rs := rulesetsWithoutWrites(t)
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("GetAllRulesets called")
		return nil, fake.GenerateEmptyResponse(), nil
	}
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("GetRuleset called")
		return nil, fake.GenerateEmptyResponse(), nil
	}
	return rs
}

// withDuplicateRefPattern lists refs/heads/main twice in the first ruleset's include.
func withDuplicateRefPattern(cr *v1alpha1.Repository) {
	(*cr.Spec.ForProvider.RepositoryRules)[0].Conditions = &v1alpha1.RulesetConditions{RefName: &v1alpha1.RulesetRefName{
		Include: []string{"refs/heads/main", "refs/heads/main"},
		Exclude: []string{},
	}}
}

// GitHub refuses rulesets on a private repository whose plan has none. Observe reports
// that on RulesetsPartial and the rulesets gauge while the repository declares rulesets,
// and lets the other blocks decide ResourceUpToDate, so the rest of the repository keeps
// reconciling.
func TestObserveRulesetListForbidden(t *testing.T) {
	cases := map[string]struct {
		cr           *v1alpha1.Repository
		wantUpToDate bool
	}{
		"DeclaredRulesets": {
			cr:           repository(),
			wantUpToDate: true,
		},
		"TopicsDrift": {
			cr:           repository(withDifferentTopics()),
			wantUpToDate: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics := telemetry.NewForTest()
			tc.cr.Spec.ForProvider.Org = "acme"

			got, err := (&external{github: clientFor(upToDateRepositories(nil), rulesetsForbidden(t)), metrics: metrics}).Observe(context.Background(), tc.cr)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate != tc.wantUpToDate {
				t.Errorf("Observe = %+v, want ResourceExists true, ResourceUpToDate %v", got, tc.wantUpToDate)
			}
			condition := tc.cr.GetCondition(typeRulesetsPartial)
			if condition.Status != corev1.ConditionTrue || condition.Reason != reasonRulesetsForbidden {
				t.Errorf("RulesetsPartial = %v/%v, want True/%v", condition.Status, condition.Reason, reasonRulesetsForbidden)
			}
			if !strings.HasSuffix(condition.Message, ": "+planWithoutRulesets) {
				t.Errorf("RulesetsPartial message = %q, want GitHub's message %q", condition.Message, planWithoutRulesets)
			}
			if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, telemetry.DimensionRulesetsForbidden)); got != 1 {
				t.Errorf("unreconcilable{dimension=rulesets_forbidden} = %v, want 1", got)
			}
			if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, telemetry.DimensionRulesetParameters)); got != 0 {
				t.Errorf("unreconcilable{dimension=ruleset_parameters} = %v, want 0: nothing was fetched", got)
			}
		})
	}
}

// repositoryRules: [] asks for a repository without rulesets, which a plan refusing
// rulesets already gives. Observe reports RulesetsPartial False and the rulesets gauge 0
// on the 403, also after an earlier report, so a Free-plan private repository declaring
// no rulesets keeps no alert firing.
func TestObserveRulesetListForbiddenWithoutDeclaredRulesets(t *testing.T) {
	cases := map[string]struct {
		forbiddenBefore bool
	}{
		"FirstObserve":   {forbiddenBefore: false},
		"AfterForbidden": {forbiddenBefore: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics := telemetry.NewForTest()
			cr := repository(func(cr *v1alpha1.Repository) { cr.Spec.ForProvider.RepositoryRules = &[]v1alpha1.RepositoryRuleset{} })
			cr.Spec.ForProvider.Org = "acme"
			if tc.forbiddenBefore {
				metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionRulesetsForbidden, true)
				cr.SetConditions(xpv2.Condition{Type: typeRulesetsPartial, Status: corev1.ConditionTrue, Reason: reasonRulesetsForbidden})
			}

			got, err := (&external{github: clientFor(upToDateRepositories(nil), rulesetsForbidden(t)), metrics: metrics}).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceExists || !got.ResourceUpToDate {
				t.Errorf("Observe = %+v, want ResourceExists true, ResourceUpToDate true", got)
			}
			if condition := cr.GetCondition(typeRulesetsPartial); condition.Status != corev1.ConditionFalse || condition.Reason != reasonRulesetsReconciled || condition.Message != "" {
				t.Errorf("RulesetsPartial = %v/%v %q, want False/%v with no message", condition.Status, condition.Reason, condition.Message, reasonRulesetsReconciled)
			}
			if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, telemetry.DimensionRulesetsForbidden)); got != 0 {
				t.Errorf("unreconcilable{dimension=rulesets_forbidden} = %v, want 0", got)
			}
		})
	}
}

// Once GitHub lists the rulesets again, RulesetsPartial turns False and the gauge 0.
func TestObserveRulesetListAllowedClearsRulesetsPartial(t *testing.T) {
	metrics := telemetry.NewForTest()
	cr := repository()
	cr.Spec.ForProvider.Org = "acme"

	if _, err := (&external{github: clientFor(upToDateRepositories(nil), rulesetsForbidden(t)), metrics: metrics}).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe(403): %v", err)
	}
	if _, err := (&external{github: clientFor(upToDateRepositories(nil), upToDateRulesets()), metrics: metrics}).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe(200): %v", err)
	}

	if got := cr.GetCondition(typeRulesetsPartial); got.Status != corev1.ConditionFalse || got.Reason != reasonRulesetsReconciled || got.Message != "" {
		t.Errorf("RulesetsPartial = %v/%v %q, want False/%v with no message", got.Status, got.Reason, got.Message, reasonRulesetsReconciled)
	}
	if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, telemetry.DimensionRulesetsForbidden)); got != 0 {
		t.Errorf("unreconcilable{dimension=rulesets_forbidden} = %v, want 0", got)
	}
}

// A ruleset listing a ref pattern twice is a mistake in the spec. Observe reports it on
// RulesetsPartial and the ruleset_spec gauge without reading the rulesets, and lets the
// other blocks decide ResourceUpToDate, so the rest of the repository keeps reconciling.
func TestObserveInvalidRulesetsSetRulesetsPartial(t *testing.T) {
	cases := map[string]struct {
		cr           *v1alpha1.Repository
		wantUpToDate bool
	}{
		"OtherBlocksUpToDate": {
			cr:           repository(withDuplicateRefPattern),
			wantUpToDate: true,
		},
		"TopicsDrift": {
			cr:           repository(withDuplicateRefPattern, withDifferentTopics()),
			wantUpToDate: false,
		},
	}
	wantMsg := fmt.Sprintf("ruleset %s lists ref pattern refs/heads/main twice in include", rr1name)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics := telemetry.NewForTest()
			tc.cr.Spec.ForProvider.Org = "acme"

			got, err := (&external{github: clientFor(upToDateRepositories(nil), rulesetsWithoutCalls(t)), metrics: metrics}).Observe(context.Background(), tc.cr)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate != tc.wantUpToDate {
				t.Errorf("Observe = %+v, want ResourceExists true, ResourceUpToDate %v", got, tc.wantUpToDate)
			}
			condition := tc.cr.GetCondition(typeRulesetsPartial)
			if condition.Status != corev1.ConditionTrue || condition.Reason != reasonInvalidRulesets || condition.Message != wantMsg {
				t.Errorf("RulesetsPartial = %v/%v %q, want True/%v %q", condition.Status, condition.Reason, condition.Message, reasonInvalidRulesets, wantMsg)
			}
			gauge := metrics.RepositoryUnreconcilableForTest()
			if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionRulesetSpec)); got != 1 {
				t.Errorf("unreconcilable{dimension=ruleset_spec} = %v, want 1", got)
			}
			for _, dimension := range []string{telemetry.DimensionRulesetsForbidden, telemetry.DimensionRulesetParameters} {
				if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, dimension)); got != 0 {
					t.Errorf("unreconcilable{dimension=%s} = %v, want 0: nothing was read", dimension, got)
				}
			}
		})
	}
}

// Once the spec lists each ref pattern once, RulesetsPartial turns False and the
// ruleset_spec gauge 0.
func TestObserveFixedRulesetsClearRulesetsPartial(t *testing.T) {
	metrics := telemetry.NewForTest()
	cr := repository(withDuplicateRefPattern)
	cr.Spec.ForProvider.Org = "acme"

	if _, err := (&external{github: clientFor(upToDateRepositories(nil), rulesetsWithoutCalls(t)), metrics: metrics}).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe(invalid): %v", err)
	}
	cr.Spec.ForProvider.RepositoryRules = repository().Spec.ForProvider.RepositoryRules
	if _, err := (&external{github: clientFor(upToDateRepositories(nil), upToDateRulesets()), metrics: metrics}).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe(fixed): %v", err)
	}

	if got := cr.GetCondition(typeRulesetsPartial); got.Status != corev1.ConditionFalse || got.Reason != reasonRulesetsReconciled || got.Message != "" {
		t.Errorf("RulesetsPartial = %v/%v %q, want False/%v with no message", got.Status, got.Reason, got.Message, reasonRulesetsReconciled)
	}
	if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, telemetry.DimensionRulesetSpec)); got != 0 {
		t.Errorf("unreconcilable{dimension=ruleset_spec} = %v, want 0", got)
	}
}

// Any other failure of the ruleset list fails Observe, so a transient error is retried
// instead of being reported as a refusal: a 500, and a 403 that is GitHub's rate limit.
func TestObserveRulesetListErrorOtherThan403Fails(t *testing.T) {
	cases := map[string]error{
		"ServerError": &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusInternalServerError}, Message: "Server Error"},
		"RateLimit":   &github.RateLimitError{Response: &http.Response{StatusCode: http.StatusForbidden}, Message: "API rate limit exceeded"},
	}
	for name, listErr := range cases {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
				return nil, nil, listErr
			}
			cr := repository()

			_, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
			if !errors.Is(err, listErr) {
				t.Errorf("Observe(...) error = %#v, want the list error %#v", err, listErr)
			}
			if got := cr.GetCondition(typeRulesetsPartial).Status; got == corev1.ConditionTrue {
				t.Errorf("RulesetsPartial = True, want it unset after a %s", name)
			}
		})
	}
}

// A repository whose rulesets the provider leaves alone publishes rulesets=0,
// ruleset_parameters=0, ruleset_spec=0 and RulesetsPartial False, so an earlier report does not keep
// an alert firing:
// repositoryRules unset, or the CR being deleted. An archived repository lists its
// rulesets as any other does (TestArchivedRepositoryRulesetListErrors).
func TestObserveUnreadRulesetsClearRulesetsPartial(t *testing.T) {
	deleted := repository()
	now := metav1.Now()
	deleted.SetDeletionTimestamp(&now)
	cases := map[string]struct {
		repos *fake.MockRepositoriesClient
		cr    *v1alpha1.Repository
	}{
		"RepositoryRulesUnset": {
			repos: upToDateRepositories(nil),
			cr:    repository(func(cr *v1alpha1.Repository) { cr.Spec.ForProvider.RepositoryRules = nil }),
		},
		"Deleted": {
			repos: upToDateRepositories(nil),
			cr:    deleted,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics := telemetry.NewForTest()
			metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionRulesetsForbidden, true)
			metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionRulesetParameters, true)
			metrics.SetRepositoryUnreconcilable("acme", repo, telemetry.DimensionRulesetSpec, true)
			tc.cr.Spec.ForProvider.Org = "acme"
			tc.cr.SetConditions(xpv2.Condition{Type: typeRulesetsPartial, Status: corev1.ConditionTrue, Reason: reasonRulesetsForbidden})

			if _, err := (&external{github: clientFor(tc.repos, rulesetsForbidden(t)), metrics: metrics}).Observe(context.Background(), tc.cr); err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got := tc.cr.GetCondition(typeRulesetsPartial).Status; got != corev1.ConditionFalse {
				t.Errorf("RulesetsPartial = %v, want False", got)
			}
			for _, dimension := range []string{telemetry.DimensionRulesetsForbidden, telemetry.DimensionRulesetParameters, telemetry.DimensionRulesetSpec} {
				if got := testutil.ToFloat64(metrics.RepositoryUnreconcilableForTest().WithLabelValues("acme", repo, dimension)); got != 0 {
					t.Errorf("unreconcilable{dimension=%s} = %v, want 0", dimension, got)
				}
			}
		})
	}
}

// While GitHub answers the ruleset list with 403, Update leaves rulesets out and brings
// the rest of the repository to the CR, so the topics converge and Synced can turn True.
// The CR carries no RulesetsPartial here: Observe returns early on other drift, and the
// first reconcile after an upgrade has no condition yet, so Update decides from the list.
func TestUpdateSkipsRulesetsWhileForbidden(t *testing.T) {
	var replaced []string
	repos := upToDateRepositories(nil)
	repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
		return req, fake.GenerateEmptyResponse(), nil
	}
	repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
		replaced = topics
		return topics, fake.GenerateEmptyResponse(), nil
	}
	cr := repository(withDifferentTopics())
	cr.Spec.ForProvider.Webhooks = nil
	cr.Spec.ForProvider.BranchProtectionRules = nil

	if _, err := (&external{github: clientFor(repos, rulesetsForbidden(t))}).Update(context.Background(), cr); err != nil {
		t.Fatalf("Update while forbidden: %v", err)
	}
	if diff := cmp.Diff(cr.Spec.ForProvider.Topics, replaced); diff != "" {
		t.Errorf("topics sent: -want, +got:\n%s", diff)
	}
}

// A ruleset list that fails for any reason other than 403 fails the update, so a
// transient error never passes for a refused feature.
func TestUpdateRulesetListErrorFails(t *testing.T) {
	errBoom := errors.New("boom")
	rs := rulesetsWithoutWrites(t)
	rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
		return nil, nil, errBoom
	}
	repos := upToDateRepositories(nil)
	repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
		return req, fake.GenerateEmptyResponse(), nil
	}
	repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
		return topics, fake.GenerateEmptyResponse(), nil
	}
	cr := repository()
	cr.Spec.ForProvider.Webhooks = nil
	cr.Spec.ForProvider.BranchProtectionRules = nil

	_, err := (&external{github: clientFor(repos, rs)}).Update(context.Background(), cr)
	if diff := cmp.Diff(errBoom, err, test.EquateErrors()); diff != "" {
		t.Errorf("Update(...): -want error, +got error:\n%s", diff)
	}
}

// A failed GET of a declared ruleset fails Observe and the update, instead of making
// the ruleset look missing.
func TestRulesetGetErrorFails(t *testing.T) {
	errBoom := errors.New("boom")
	rs := upToDateRulesets()
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		return nil, nil, errBoom
	}
	rs.MockCreateRuleset = func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		t.Errorf("CreateRuleset called for %q after a failed GET", r.Name)
		return &r, fake.GenerateEmptyResponse(), nil
	}

	_, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository())
	if diff := cmp.Diff(errBoom, err, test.EquateErrors()); diff != "" {
		t.Errorf("Observe(...): -want error, +got error:\n%s", diff)
	}
	err = updateRepositoryRules(context.Background(), repository(), clientFor(upToDateRepositories(nil), rs), repo)
	if diff := cmp.Diff(errBoom, err, test.EquateErrors()); diff != "" {
		t.Errorf("updateRepositoryRules(...): -want error, +got error:\n%s", diff)
	}
}

// Observe fails on parameters it cannot decode, so it compares only what GitHub really holds.
func TestObserveRuleDecodeErrorFails(t *testing.T) {
	cases := map[string]*rulesets.Rule{
		"size not a number":       {Type: "max_file_size", Parameters: json.RawMessage(`{"max_file_size":"big"}`)},
		"reviewer id missing":     {Type: "pull_request", Parameters: json.RawMessage(`{"required_reviewers":[{"reviewer":{"type":"Team"}}]}`)},
		"reviewer id null":        {Type: "pull_request", Parameters: json.RawMessage(`{"required_reviewers":[{"reviewer":{"id":null,"type":"Team"}}]}`)},
		"actor id not an integer": {Type: "pull_request", Parameters: json.RawMessage(`{"dismissal_restriction":{"allowed_actors":[{"id":1.5,"type":"User"}]}}`)},
	}
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			rulesetWithRules(rs, append(githubRuleset()[0].Rules, rule))

			_, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository())
			if err == nil || !strings.Contains(err.Error(), "rule "+rule.Type) {
				t.Errorf("Observe(...) error = %v, want the %s decode error", err, rule.Type)
			}
		})
	}
}

// A declared ruleset missing on GitHub is created with the CR's rules, by the update
// and with the repository.
func TestDeclaredRulesetIsCreated(t *testing.T) {
	var created []rulesets.Ruleset
	createRuleset := func(ctx context.Context, owner, repo string, r rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
		created = append(created, r)
		return &r, fake.GenerateEmptyResponse(), nil
	}
	check := func(t *testing.T) {
		t.Helper()
		if len(created) != 1 {
			t.Fatalf("CreateRuleset calls = %d, want 1", len(created))
		}
		got, err := rulesets.Decode(created[0].Rules)
		if err != nil {
			t.Fatalf("decode created rules: %v", err)
		}
		if created[0].Name != rr1name {
			t.Errorf("created ruleset %q, want %q", created[0].Name, rr1name)
		}
		if diff := cmp.Diff(githubRules(), got); diff != "" {
			t.Errorf("created rules: -want, +got:\n%s", diff)
		}
	}

	t.Run("Update", func(t *testing.T) {
		created = nil
		rs := upToDateRulesets()
		rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), nil
		}
		rs.MockCreateRuleset = createRuleset
		if err := updateRepositoryRules(context.Background(), repository(), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
			t.Fatalf("updateRepositoryRules: %v", err)
		}
		check(t)
	})

	t.Run("Create", func(t *testing.T) {
		created = nil
		repos := &fake.MockRepositoriesClient{
			MockCreate: func(ctx context.Context, owner string, r *github.Repository) (*github.Repository, *github.Response, error) {
				return r, fake.GenerateEmptyResponse(), nil
			},
			MockAddCollaborator: func(ctx context.Context, owner, r, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
				return nil, fake.GenerateEmptyResponse(), nil
			},
			MockReplaceAllTopics: func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
				return topics, fake.GenerateEmptyResponse(), nil
			},
		}
		teams := &fake.MockTeamsClient{
			MockAddTeamRepoBySlug: func(ctx context.Context, org, slug, owner, r string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
				return fake.GenerateEmptyResponse(), nil
			},
		}
		cr := repository()
		cr.Spec.ForProvider.Webhooks = nil
		cr.Spec.ForProvider.BranchProtectionRules = nil
		e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repos, Teams: teams, Rulesets: &fake.MockRulesetsClient{MockCreateRuleset: createRuleset}}}}
		if _, err := e.Create(context.Background(), cr); err != nil {
			t.Fatalf("Create: %v", err)
		}
		check(t)
	})
}

// Refs compare as sets, for include and exclude alike.
func TestConditionsRefOrder(t *testing.T) {
	cr := repository(func(cr *v1alpha1.Repository) {
		(*cr.Spec.ForProvider.RepositoryRules)[0].Conditions = &v1alpha1.RulesetConditions{RefName: &v1alpha1.RulesetRefName{
			Include: []string{"refs/heads/a", "refs/heads/b"},
			Exclude: []string{"refs/heads/x", "refs/heads/y"},
		}}
	})
	rs := upToDateRulesets()
	rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
		r := githubRuleset()[0]
		r.Conditions = &rulesets.Conditions{RefName: &rulesets.RefName{Include: []string{"refs/heads/b", "refs/heads/a"}, Exclude: []string{"refs/heads/y", "refs/heads/x"}}}
		return r, fake.GenerateEmptyResponse(), nil
	}
	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: the same refs in another order")
	}
}

// GitHub stores OrganizationAdmin and DeployKey with a null ID, so two of them with the
// same mode are the same actor whatever their IDs: Observe reports the duplicate on
// RulesetsPartial, and Observe, Create and Update make no ruleset call.
func TestBypassActorsDuplicateIgnoredIDTypes(t *testing.T) {
	for _, typ := range []string{"OrganizationAdmin", "DeployKey"} {
		withDuplicate := withBypassActors(
			&v1alpha1.RulesetByPassActors{ActorID: github.Ptr(int64(1)), ActorType: github.Ptr(typ), BypassMode: github.Ptr("exempt")},
			&v1alpha1.RulesetByPassActors{ActorID: github.Ptr(int64(2)), ActorType: github.Ptr(typ), BypassMode: github.Ptr("exempt")},
		)
		wantMsg := fmt.Sprintf("ruleset %s lists bypass actor %s twice with bypassMode exempt", rr1name, typ)
		check := func(t *testing.T, err error) {
			t.Helper()
			if err != nil {
				t.Errorf("error = %v, want nil: the rest of the repository keeps reconciling", err)
			}
		}

		t.Run(typ+"/Observe", func(t *testing.T) {
			cr := repository(withDuplicate)
			got, err := (&external{github: clientFor(upToDateRepositories(nil), rulesetsWithoutCalls(t))}).Observe(context.Background(), cr)
			check(t, err)
			if !got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = false, want true: the other blocks are up to date")
			}
			if c := cr.GetCondition(typeRulesetsPartial); c.Status != corev1.ConditionTrue || c.Reason != reasonInvalidRulesets || !strings.Contains(c.Message, wantMsg) {
				t.Errorf("RulesetsPartial = %v/%v %q, want True/%v containing %q", c.Status, c.Reason, c.Message, reasonInvalidRulesets, wantMsg)
			}
		})
		t.Run(typ+"/Update", func(t *testing.T) {
			repos := upToDateRepositories(nil)
			repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
				return req, fake.GenerateEmptyResponse(), nil
			}
			repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
				return topics, fake.GenerateEmptyResponse(), nil
			}
			cr := settingsOnlyRepository()
			cr.Spec.ForProvider.RepositoryRules = repository(withDuplicate).Spec.ForProvider.RepositoryRules
			_, err := (&external{github: clientFor(repos, rulesetsWithoutCalls(t))}).Update(context.Background(), cr)
			check(t, err)
		})
		t.Run(typ+"/Create", func(t *testing.T) {
			repos := &fake.MockRepositoriesClient{
				MockCreate: func(ctx context.Context, owner string, r *github.Repository) (*github.Repository, *github.Response, error) {
					return r, fake.GenerateEmptyResponse(), nil
				},
				MockAddCollaborator: func(ctx context.Context, owner, r, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
					return nil, fake.GenerateEmptyResponse(), nil
				},
				MockReplaceAllTopics: func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
					return topics, fake.GenerateEmptyResponse(), nil
				},
			}
			teams := &fake.MockTeamsClient{
				MockAddTeamRepoBySlug: func(ctx context.Context, org, slug, owner, r string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
					return fake.GenerateEmptyResponse(), nil
				},
			}
			cr := repository(withDuplicate)
			cr.Spec.ForProvider.Webhooks = nil
			cr.Spec.ForProvider.BranchProtectionRules = nil
			e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repos, Teams: teams, Rulesets: rulesetsWithoutCalls(t)}}}
			_, err := e.Create(context.Background(), cr)
			check(t, err)
		})
	}
}

// A ref pattern listed twice in include or exclude is reported on RulesetsPartial with
// a message naming it, because GitHub stores each pattern once. Observe, Update and
// Create make no ruleset call and go on with the rest of the repository.
func TestRefPatternDuplicates(t *testing.T) {
	cases := map[string]v1alpha1.RulesetRefName{
		"include": {Include: []string{"refs/heads/main", "~DEFAULT_BRANCH", "refs/heads/main"}, Exclude: []string{"refs/heads/x"}},
		"exclude": {Include: []string{"refs/heads/main"}, Exclude: []string{"refs/heads/y", "refs/heads/x", "refs/heads/y"}},
	}
	for list, refName := range cases {
		withDuplicate := func(cr *v1alpha1.Repository) {
			(*cr.Spec.ForProvider.RepositoryRules)[0].Conditions = &v1alpha1.RulesetConditions{RefName: refName.DeepCopy()}
		}
		pattern := "refs/heads/main"
		if list == "exclude" {
			pattern = "refs/heads/y"
		}
		wantMsg := fmt.Sprintf("ruleset %s lists ref pattern %s twice in %s", rr1name, pattern, list)
		check := func(t *testing.T, err error) {
			t.Helper()
			if err != nil {
				t.Errorf("error = %v, want nil: the rest of the repository keeps reconciling", err)
			}
		}

		t.Run(list+"/Observe", func(t *testing.T) {
			cr := repository(withDuplicate)
			got, err := (&external{github: clientFor(upToDateRepositories(nil), rulesetsWithoutCalls(t))}).Observe(context.Background(), cr)
			check(t, err)
			if !got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = false, want true: the other blocks are up to date")
			}
			if c := cr.GetCondition(typeRulesetsPartial); c.Status != corev1.ConditionTrue || c.Reason != reasonInvalidRulesets || c.Message != wantMsg {
				t.Errorf("RulesetsPartial = %v/%v %q, want True/%v %q", c.Status, c.Reason, c.Message, reasonInvalidRulesets, wantMsg)
			}
		})
		t.Run(list+"/Update", func(t *testing.T) {
			repos := upToDateRepositories(nil)
			repos.MockEdit = func(ctx context.Context, owner, r string, req *github.Repository) (*github.Repository, *github.Response, error) {
				return req, fake.GenerateEmptyResponse(), nil
			}
			repos.MockReplaceAllTopics = func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
				return topics, fake.GenerateEmptyResponse(), nil
			}
			cr := settingsOnlyRepository()
			cr.Spec.ForProvider.RepositoryRules = repository(withDuplicate).Spec.ForProvider.RepositoryRules
			_, err := (&external{github: clientFor(repos, rulesetsWithoutCalls(t))}).Update(context.Background(), cr)
			check(t, err)
		})
		t.Run(list+"/Create", func(t *testing.T) {
			repos := &fake.MockRepositoriesClient{
				MockCreate: func(ctx context.Context, owner string, r *github.Repository) (*github.Repository, *github.Response, error) {
					return r, fake.GenerateEmptyResponse(), nil
				},
				MockAddCollaborator: func(ctx context.Context, owner, r, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
					return nil, fake.GenerateEmptyResponse(), nil
				},
				MockReplaceAllTopics: func(ctx context.Context, owner, r string, topics []string) ([]string, *github.Response, error) {
					return topics, fake.GenerateEmptyResponse(), nil
				},
			}
			teams := &fake.MockTeamsClient{
				MockAddTeamRepoBySlug: func(ctx context.Context, org, slug, owner, r string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
					return fake.GenerateEmptyResponse(), nil
				},
			}
			cr := repository(withDuplicate)
			cr.Spec.ForProvider.Webhooks = nil
			cr.Spec.ForProvider.BranchProtectionRules = nil
			e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repos, Teams: teams, Rulesets: rulesetsWithoutCalls(t)}}}
			_, err := e.Create(context.Background(), cr)
			check(t, err)
		})
	}
}

// A ruleset the CR does not name is deleted without a GET, so its deletion goes ahead
// even when the GET or its parameters would fail.
func TestSurplusRulesetIsNotFetched(t *testing.T) {
	surplus := &rulesets.Ruleset{ID: github.Ptr(githubOnlyRulesetID), Name: "github-only", SourceType: github.Ptr("Repository"), Enforcement: rr1enforcement}
	for name, get := range map[string]func() (*rulesets.Ruleset, error){
		"GetFails": func() (*rulesets.Ruleset, error) { return nil, errors.New("boom") },
		"BadParameters": func() (*rulesets.Ruleset, error) {
			return &rulesets.Ruleset{ID: surplus.ID, Name: surplus.Name, Rules: []*rulesets.Rule{{Type: "max_file_size", Parameters: json.RawMessage(`{"max_file_size":"big"}`)}}}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			rs := upToDateRulesets()
			rs.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
				return append(githubRuleset(), surplus), fake.GenerateEmptyResponse(), nil
			}
			rs.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
				if rulesetID == githubOnlyRulesetID {
					t.Errorf("GetRuleset called for the surplus ruleset")
					r, err := get()
					return r, fake.GenerateEmptyResponse(), err
				}
				return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
			}
			var deleted []int64
			rs.MockDeleteRuleset = func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
				deleted = append(deleted, rulesetID)
				return fake.GenerateEmptyResponse(), nil
			}

			got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), repository())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if got.ResourceUpToDate {
				t.Errorf("ResourceUpToDate = true, want false: the surplus ruleset must be deleted")
			}
			if err := updateRepositoryRules(context.Background(), repository(), clientFor(upToDateRepositories(nil), rs), repo); err != nil {
				t.Fatalf("updateRepositoryRules: %v", err)
			}
			if diff := cmp.Diff([]int64{githubOnlyRulesetID}, deleted); diff != "" {
				t.Errorf("DeleteRuleset IDs: -want, +got:\n%s", diff)
			}
		})
	}
}

// Observe skips rulesets while the CR is being deleted, so the deletion goes ahead even
// when the ruleset endpoint fails.
func TestObserveDeletedCRSkipsRulesets(t *testing.T) {
	rs := &fake.MockRulesetsClient{
		MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return nil, nil, errors.New("403 Upgrade to GitHub Pro or make this repository public")
		},
	}
	cr := repository()
	now := metav1.Now()
	cr.SetDeletionTimestamp(&now)

	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceExists {
		t.Errorf("ResourceExists = false, want true: the repository is still there to delete")
	}
}

// GitHub's bypass actors are sorted again after an OrganizationAdmin takes the CR's ID.
func TestBypassActorsResortedAfterIDOverride(t *testing.T) {
	cr := repository(withBypassActors(
		&v1alpha1.RulesetByPassActors{ActorID: github.Ptr(int64(5)), ActorType: github.Ptr("OrganizationAdmin"), BypassMode: github.Ptr("always")},
		&v1alpha1.RulesetByPassActors{ActorType: github.Ptr("OrganizationAdmin"), BypassMode: github.Ptr("exempt")},
	))
	rs := upToDateRulesets()
	rulesetWithBypassActors(rs,
		&rulesets.BypassActor{ActorType: github.Ptr("OrganizationAdmin"), BypassMode: github.Ptr("always")},
		&rulesets.BypassActor{ActorType: github.Ptr("OrganizationAdmin"), BypassMode: github.Ptr("exempt")},
	)
	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: GitHub holds both actors")
	}
}

// A status check listed twice in the CR is sent and compared twice, as GitHub keeps both.
func TestStatusChecksDuplicatesKept(t *testing.T) {
	cr := repository(withRules(func(r *v1alpha1.Rules) {
		r.RequiredStatusChecks = &v1alpha1.RulesRequiredStatusChecks{RequiredStatusChecks: []*v1alpha1.RulesRequiredStatusChecksParameters{{Context: "ci"}, {Context: "ci"}}}
	}))
	twice := func(r *rulesets.ModelledRules) {
		r.RequiredStatusChecks = &rulesets.RequiredStatusChecksRuleParameters{RequiredStatusChecks: []*rulesets.StatusCheck{{Context: "ci"}, {Context: "ci"}}}
	}
	rs := upToDateRulesets()
	rulesetHolding(t, rs, twice)
	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr.DeepCopy())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: GitHub holds ci twice, as declared")
	}

	once := upToDateRulesets()
	rulesetHolding(t, once, func(r *rulesets.ModelledRules) {
		r.RequiredStatusChecks = &rulesets.RequiredStatusChecksRuleParameters{RequiredStatusChecks: []*rulesets.StatusCheck{{Context: "ci"}}}
	})
	want := githubRules()
	twice(want)
	if diff := cmp.Diff(want, updatedRules(t, once, cr.DeepCopy())); diff != "" {
		t.Errorf("UpdateRuleset rules: -want, +got:\n%s", diff)
	}
}

// An unset target means "branch", GitHub's default, and is sent explicitly.
func TestRulesetTargetDefaultsToBranch(t *testing.T) {
	cr := repository(func(cr *v1alpha1.Repository) { (*cr.Spec.ForProvider.RepositoryRules)[0].Target = nil })
	got, err := (&external{github: clientFor(upToDateRepositories(nil), upToDateRulesets())}).Observe(context.Background(), cr.DeepCopy())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: GitHub holds a branch ruleset")
	}
	sent, err := crRepoRulesToRulesConfig(crRulesets(t, *cr.Spec.ForProvider.RepositoryRules)[rr1name])
	if err != nil {
		t.Fatalf("crRepoRulesToRulesConfig: %v", err)
	}
	if diff := cmp.Diff(github.Ptr("branch"), sent.Target); diff != "" {
		t.Errorf("target sent: -want, +got:\n%s", diff)
	}
}

// Bypass actors of other types differ by type, ID and mode, so all of them are kept
// and sent.
func TestBypassActorsDistinctIDsKept(t *testing.T) {
	actor := func(id int64, typ, mode string) *v1alpha1.RulesetByPassActors {
		return &v1alpha1.RulesetByPassActors{ActorID: github.Ptr(id), ActorType: github.Ptr(typ), BypassMode: github.Ptr(mode)}
	}
	wire := func(id int64, typ, mode string) *rulesets.BypassActor {
		return &rulesets.BypassActor{ActorID: github.Ptr(id), ActorType: github.Ptr(typ), BypassMode: github.Ptr(mode)}
	}
	cr := repository(withBypassActors(
		actor(7, "Team", "always"),
		actor(9, "Team", "always"),
		actor(7, "User", "always"),
		actor(9, "Team", "pull_request"),
	))
	rs := upToDateRulesets()
	rulesetWithBypassActors(rs,
		wire(9, "Team", "pull_request"),
		wire(7, "User", "always"),
		wire(9, "Team", "always"),
		wire(7, "Team", "always"),
	)

	got, err := (&external{github: clientFor(upToDateRepositories(nil), rs)}).Observe(context.Background(), cr.DeepCopy())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = false, want true: GitHub holds the four declared actors")
	}

	sent, err := crRepoRulesToRulesConfig(crRulesets(t, *cr.Spec.ForProvider.RepositoryRules)[rr1name])
	if err != nil {
		t.Fatalf("crRepoRulesToRulesConfig: %v", err)
	}
	want := []*rulesets.BypassActor{
		wire(7, "Team", "always"),
		wire(9, "Team", "always"),
		wire(9, "Team", "pull_request"),
		wire(7, "User", "always"),
	}
	if diff := cmp.Diff(want, sent.BypassActors); diff != "" {
		t.Errorf("bypass actors sent: -want, +got:\n%s", diff)
	}
}
