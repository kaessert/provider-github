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
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/telemetry"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/google/go-github/v90/github"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
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
	cr.Spec.ForProvider.RepositoryRules = []v1alpha1.RepositoryRuleset{
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

func githubRuleset() []*github.RepositoryRuleset {
	return []*github.RepositoryRuleset{
		{
			ID:          &rr1Id,
			Name:        rr1name,
			Target:      github.Ptr(github.RulesetTarget(rr1target)),
			Enforcement: github.RulesetEnforcement(rr1enforcement),
			Conditions: &github.RepositoryRulesetConditions{
				RefName: &github.RepositoryRulesetRefConditionParameters{
					Include: rr1Include,
					Exclude: rr1Exclude,
				},
			},
			BypassActors: []*github.BypassActor{
				{
					ActorID:    &rr1actorId,
					ActorType:  github.Ptr(github.BypassActorType(rr1actorType)),
					BypassMode: github.Ptr(github.BypassMode(rr1bypassMode)),
				},
			},
			Rules: &github.RepositoryRulesetRules{
				Creation:              &github.EmptyRuleParameters{},
				Deletion:              &github.EmptyRuleParameters{},
				Update:                &github.UpdateRuleParameters{},
				RequiredLinearHistory: &github.EmptyRuleParameters{},
				RequiredSignatures:    &github.EmptyRuleParameters{},
				NonFastForward:        &github.EmptyRuleParameters{},
			},
		},
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
						MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset(), fake.GenerateEmptyResponse(), nil
						},
						MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
						MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset(), fake.GenerateEmptyResponse(), nil
						},
						MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
						MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset(), fake.GenerateEmptyResponse(), nil
						},
						MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
						MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset(), fake.GenerateEmptyResponse(), nil
						},
						MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
		// date. Branch protection, rulesets and webhooks are frozen while archived,
		// so no mocks are wired for them — a call would be a nil-func panic, proving
		// Observe does not read the frozen dimensions.
		"ArchivedUpToDate": {
			fields: fields{github: &ghclient.Client{
				Services: &ghclient.Services{
					Repositories: &fake.MockRepositoriesClient{
						MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
							r := githubRepository()
							r.Archived = github.Ptr(true)
							return r, nil, nil
						},
						MockListCollaborators: func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
							return githubCollaborators(), fake.GenerateEmptyResponse(), nil
						},
						MockListTeams: func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
							return githubTeams(), fake.GenerateEmptyResponse(), nil
						},
					},
				},
			}},
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
				MockCreateRuleset: func(ctx context.Context, owner, r string, rs github.RepositoryRuleset) (*github.RepositoryRuleset, *github.Response, error) {
					frozenWrite = "CreateRuleset"
					return &rs, fake.GenerateEmptyResponse(), nil
				},
				MockCreateHook: func(ctx context.Context, owner, r string, hook *github.Hook) (*github.Hook, *github.Response, error) {
					frozenWrite = "CreateHook"
					return hook, fake.GenerateEmptyResponse(), nil
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
			e := external{github: &ghclient.Client{Services: &ghclient.Services{Repositories: repoClient, Teams: teamsClient}}}
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
					MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
						return githubRuleset(), fake.GenerateEmptyResponse(), nil
					},
					MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
						return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
						MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset(), fake.GenerateEmptyResponse(), nil
						},
						MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
						MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset(), fake.GenerateEmptyResponse(), nil
						},
						MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
						MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset(), fake.GenerateEmptyResponse(), nil
						},
						MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
							return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
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
		MockGetAllRulesets: func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
			return githubRuleset(), fake.GenerateEmptyResponse(), nil
		},
		MockGetRuleset: func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
			return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
		},
	}
}

func clientFor(repos *fake.MockRepositoriesClient) *ghclient.Client {
	return &ghclient.Client{Services: &ghclient.Services{Repositories: repos}}
}

// Observe publishes the collaborators gauge from the CollaboratorPartial condition: 1 while an invitee is pending, 0 once none is.
func TestObservePublishesCollaboratorsUnreconcilable(t *testing.T) {
	metrics := telemetry.NewForTest()
	gauge := metrics.RepositoryUnreconcilableForTest()

	pending := repository(withExtraUser("pending-user", "pull"))
	pending.Spec.ForProvider.Org = "acme"
	invitations := []*github.RepositoryInvitation{{Invitee: &github.User{Login: github.Ptr("pending-user")}}}
	e := external{github: clientFor(upToDateRepositories(invitations)), metrics: metrics}
	if _, err := e.Observe(context.Background(), pending); err != nil {
		t.Fatalf("Observe(pending): %v", err)
	}
	if got := testutil.ToFloat64(gauge.WithLabelValues("acme", repo, telemetry.DimensionCollaborators)); got != 1 {
		t.Errorf("unreconcilable{dimension=collaborators} with a pending invitee = %v, want 1", got)
	}

	clean := repository()
	clean.Spec.ForProvider.Org = "acme"
	e = external{github: clientFor(upToDateRepositories(nil)), metrics: metrics}
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

	e := external{github: clientFor(repos), metrics: metrics}
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

	e := external{github: clientFor(repos), metrics: metrics}
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

	e := external{github: clientFor(repos), metrics: metrics}
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

			e := external{github: clientFor(upToDateRepositories(nil)), metrics: metrics}
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
	e := external{github: clientFor(repos)}

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
			e := external{github: clientFor(repos)}

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

// Observe compares the CR against the rulesets GitHub lists with includes_parents=true;
// a listing without inherited rulesets changes which rulesets the controller sees as present.
func TestObserveListsRulesetsIncludingParents(t *testing.T) {
	var got *github.RepositoryListRulesetsOptions
	repos := upToDateRepositories(nil)
	repos.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
		got = opts
		return githubRuleset(), fake.GenerateEmptyResponse(), nil
	}

	if _, err := (&external{github: clientFor(repos)}).Observe(context.Background(), repository()); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.GetIncludesParents() {
		t.Errorf("GetAllRulesets IncludesParents = %v, want true", got.GetIncludesParents())
	}
}

// The ruleset sent on create and update carries every declared rule, and leaves "rules"
// and "bypass_actors" out when none are declared: on an update (PUT) an explicit empty
// list replaces what GitHub holds, while an omitted key leaves it alone.
func TestCrRepoRulesToRulesConfig(t *testing.T) {
	ruleset := func(m ...func(*v1alpha1.RepositoryRuleset)) v1alpha1.RepositoryRuleset {
		r := v1alpha1.RepositoryRuleset{
			Name:        rr1name,
			Target:      github.Ptr(rr1target),
			Enforcement: github.Ptr(rr1enforcement),
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

	cases := map[string]struct {
		reason        string
		rule          v1alpha1.RepositoryRuleset
		wantRuleTypes []string // nil: the "rules" key is omitted
		wantBypass    bool
	}{
		"NoRuleEnabled": {
			reason: "a ruleset with every rule off and nil bypass actors must omit both, not send empty lists",
			rule:   ruleset(),
		},
		"EmptyBypassActors": {
			reason: "an empty bypass actor list must be omitted, not sent as []",
			rule:   ruleset(func(r *v1alpha1.RepositoryRuleset) { r.BypassActors = []*v1alpha1.RulesetByPassActors{} }),
		},
		"UpdateOnly": {
			reason:        "an update rule on its own must be sent",
			rule:          ruleset(func(r *v1alpha1.RepositoryRuleset) { r.Rules.Update = github.Ptr(true) }),
			wantRuleTypes: []string{"update"},
		},
		"AllRules": {
			reason:        "every modelled rule and the bypass actors must be sent",
			rule:          ruleset(allRules),
			wantRuleTypes: []string{"creation", "deletion", "non_fast_forward", "pull_request", "required_deployments", "required_linear_history", "required_signatures", "required_status_checks", "update"},
			wantBypass:    true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(crRepoRulesToRulesConfig(tc.rule))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if _, ok := fields["bypass_actors"]; ok != tc.wantBypass {
				t.Errorf("%s\nbypass_actors present = %v, want %v; body %s", tc.reason, ok, tc.wantBypass, body)
			}

			raw, ok := fields["rules"]
			if tc.wantRuleTypes == nil {
				if ok {
					t.Errorf("%s\nrules present, want omitted; body %s", tc.reason, body)
				}
				return
			}
			var rules []struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &rules); err != nil {
				t.Fatalf("unmarshal rules %s: %v", raw, err)
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

// An update sends only the rules the provider models, so GitHub drops every other rule
// in the ruleset. unmanagedRuleTypes names those rules so Observe can stop first. It
// leaves out merge_queue, workflows and the *_pattern rules: rulesets holding them are
// reconciled, and reporting them would stop that.
func TestUnmanagedRuleTypes(t *testing.T) {
	// decode builds the rules from GitHub's wire format, so each case also pins that the
	// type name GitHub sends reaches the field the guard checks.
	decode := func(types ...string) *github.RepositoryRulesetRules {
		wire := make([]map[string]string, len(types))
		for i, typ := range types {
			wire[i] = map[string]string{"type": typ}
		}
		body, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rules := &github.RepositoryRulesetRules{}
		if err := json.Unmarshal(body, rules); err != nil {
			t.Fatalf("unmarshal %s: %v", body, err)
		}
		return rules
	}

	type testCase struct {
		reason string
		rules  *github.RepositoryRulesetRules
		want   []string
	}
	cases := map[string]testCase{
		"NoRules": {
			reason: "a ruleset without rules holds nothing an update could drop",
		},
		"ModelledOnly": {
			reason: "rules the provider models survive an update",
			rules:  decode("creation", "update", "deletion", "required_linear_history", "required_deployments", "required_signatures", "pull_request", "required_status_checks", "non_fast_forward"),
		},
		"ReconciledUnmodelled": {
			reason: "rulesets holding these are reconciled; reporting them would stop that",
			rules:  decode("merge_queue", "workflows", "commit_message_pattern", "commit_author_email_pattern", "committer_email_pattern", "branch_name_pattern", "tag_name_pattern"),
		},
		"SortedWithModelled": {
			reason: "the error lists the unmanaged types sorted, whatever order GitHub sends them in",
			rules:  decode("max_file_size", "creation", "code_scanning"),
			want:   []string{"code_scanning", "max_file_size"},
		},
	}
	for _, typ := range []string{
		"code_scanning", "copilot_code_review", "file_extension_restriction", "file_path_restriction",
		"max_file_path_length", "max_file_size", "repository_create", "repository_delete",
		"repository_name", "repository_transfer", "repository_visibility",
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

// Observe fails on a ruleset holding a rule the provider does not model, and nothing is
// written: an update would send the ruleset without that rule and GitHub would drop it.
func TestObserveRulesetWithUnmanagedRuleFails(t *testing.T) {
	repos := upToDateRepositories(nil)
	repos.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
		rs := githubRuleset()[0]
		rs.Rules.FilePathRestriction = &github.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"secrets/"}}
		return rs, fake.GenerateEmptyResponse(), nil
	}
	repos.MockCreateRuleset = func(ctx context.Context, owner, repo string, rs github.RepositoryRuleset) (*github.RepositoryRuleset, *github.Response, error) {
		t.Errorf("CreateRuleset called; a ruleset with unmanaged rules must not be written")
		return nil, nil, nil
	}
	repos.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, rs github.RepositoryRuleset) (*github.RepositoryRuleset, *github.Response, error) {
		t.Errorf("UpdateRuleset called; it would drop the file_path_restriction rule")
		return nil, nil, nil
	}
	repos.MockDeleteRuleset = func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
		t.Errorf("DeleteRuleset called; a ruleset with unmanaged rules must not be written")
		return nil, nil
	}

	_, err := (&external{github: clientFor(repos)}).Observe(context.Background(), repository())

	want := errors.New("ruleset test-ruleset-1 has rule types this provider does not manage (file_path_restriction); remove them on GitHub or stop managing repositoryRules for this repository")
	if diff := cmp.Diff(want, err, test.EquateErrors()); diff != "" {
		t.Errorf("Observe(...): -want error, +got error:\n%s", diff)
	}
}

const githubOnlyRulesetID int64 = 456

// withGitHubOnlyRuleset adds a ruleset that exists on GitHub but is not named in the CR,
// holding a rule the provider does not model.
func withGitHubOnlyRuleset(repos *fake.MockRepositoriesClient) {
	githubOnly := func() *github.RepositoryRuleset {
		return &github.RepositoryRuleset{
			ID:          github.Ptr(githubOnlyRulesetID),
			Name:        "github-only",
			Enforcement: github.RulesetEnforcement(rr1enforcement),
			Rules: &github.RepositoryRulesetRules{
				FilePathRestriction: &github.FilePathRestrictionRuleParameters{RestrictedFilePaths: []string{"secrets/"}},
			},
		}
	}
	repos.MockGetAllRulesets = func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error) {
		return append(githubRuleset(), githubOnly()), fake.GenerateEmptyResponse(), nil
	}
	repos.MockGetRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error) {
		if rulesetID == githubOnlyRulesetID {
			return githubOnly(), fake.GenerateEmptyResponse(), nil
		}
		return githubRuleset()[0], fake.GenerateEmptyResponse(), nil
	}
}

// The unmanaged-rule guard covers only rulesets named in the CR. A ruleset not named there
// is deleted, never updated, so no rule of it can be dropped; failing on it would block
// the reconcile that removes it.
func TestObserveGitHubOnlyRulesetWithUnmanagedRuleIsSurplus(t *testing.T) {
	repos := upToDateRepositories(nil)
	withGitHubOnlyRuleset(repos)

	got, err := (&external{github: clientFor(repos)}).Observe(context.Background(), repository())
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
	repos := upToDateRepositories(nil)
	withGitHubOnlyRuleset(repos)
	var deleted []int64
	repos.MockDeleteRuleset = func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
		deleted = append(deleted, rulesetID)
		return fake.GenerateEmptyResponse(), nil
	}
	repos.MockUpdateRuleset = func(ctx context.Context, owner, repo string, rulesetID int64, rs github.RepositoryRuleset) (*github.RepositoryRuleset, *github.Response, error) {
		if rulesetID == githubOnlyRulesetID {
			t.Errorf("UpdateRuleset called for the GitHub-only ruleset; it would drop the file_path_restriction rule")
		}
		return &rs, fake.GenerateEmptyResponse(), nil
	}

	if err := updateRepositoryRules(context.Background(), repository(), clientFor(repos), repo); err != nil {
		t.Fatalf("updateRepositoryRules: %v", err)
	}
	if diff := cmp.Diff([]int64{githubOnlyRulesetID}, deleted); diff != "" {
		t.Errorf("DeleteRuleset IDs: -want, +got:\n%s", diff)
	}
}
