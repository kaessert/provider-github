/*
Copyright 2021 The Crossplane Authors.

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

package clients

import (
	"context"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-github/v90/github"
)

type Services struct {
	Actions       ActionsClient
	Dependabot    DependabotClient
	Organizations OrganizationsClient
	Users         UsersClient
	Teams         TeamsClient
	Repositories  RepositoriesClient
}

type ActionsClient interface {
	ListEnabledReposInOrg(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error)
	SetEnabledReposInOrg(ctx context.Context, owner string, repositoryIDs []int64) (*github.Response, error)
	GetOrgSecret(ctx context.Context, org, name string) (*github.Secret, *github.Response, error)
	ListSelectedReposForOrgSecret(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
	SetSelectedReposForOrgSecret(ctx context.Context, org, name string, ids []int64) (*github.Response, error)
	GetOrgVariable(ctx context.Context, org, name string) (*github.ActionsVariable, *github.Response, error)
	CreateOrgVariable(ctx context.Context, org string, variable github.ActionsCreateOrgVariableRequest) (*github.Response, error)
	UpdateOrgVariable(ctx context.Context, org, name string, variable github.ActionsUpdateOrgVariableRequest) (*github.Response, error)
	DeleteOrgVariable(ctx context.Context, org, name string) (*github.Response, error)
	ListSelectedReposForOrgVariable(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
	ListOrganizationRunnerGroups(ctx context.Context, org string, opts *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error)
	CreateOrganizationRunnerGroup(ctx context.Context, org string, createReq github.CreateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error)
	UpdateOrganizationRunnerGroup(ctx context.Context, org string, groupID int64, updateReq github.UpdateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error)
	DeleteOrganizationRunnerGroup(ctx context.Context, org string, groupID int64) (*github.Response, error)
	ListRepositoryAccessRunnerGroup(ctx context.Context, org string, groupID int64, opts *github.ListOptions) (*github.ListRepositories, *github.Response, error)
	SetRepositoryAccessRunnerGroup(ctx context.Context, org string, groupID int64, ids github.SetRepoAccessRunnerGroupRequest) (*github.Response, error)
}

type DependabotClient interface {
	GetOrgSecret(ctx context.Context, org, name string) (*github.Secret, *github.Response, error)
	ListSelectedReposForOrgSecret(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
	SetSelectedReposForOrgSecret(ctx context.Context, org, name string, ids []int64) (*github.Response, error)
}

type OrganizationsClient interface {
	Get(ctx context.Context, org string) (*github.Organization, *github.Response, error)
	Edit(ctx context.Context, name string, org *github.Organization) (*github.Organization, *github.Response, error)
	GetOrgMembership(ctx context.Context, user, org string) (*github.Membership, *github.Response, error)
	CreateOrgInvitation(ctx context.Context, org string, opts *github.CreateOrgInvitationOptions) (*github.Invitation, *github.Response, error)
	EditOrgMembership(ctx context.Context, user, org string, membership *github.Membership) (*github.Membership, *github.Response, error)
	RemoveOrgMembership(ctx context.Context, user, org string) (*github.Response, error)
	ListHooks(ctx context.Context, org string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error)
	GetHook(ctx context.Context, org string, id int64) (*github.Hook, *github.Response, error)
	CreateHook(ctx context.Context, org string, hook *github.Hook) (*github.Hook, *github.Response, error)
	EditHook(ctx context.Context, org string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error)
	DeleteHook(ctx context.Context, org string, id int64) (*github.Response, error)
}

type UsersClient interface {
	Get(ctx context.Context, user string) (*github.User, *github.Response, error)
}

type TeamsClient interface {
	GetTeamBySlug(ctx context.Context, org, slug string) (*github.Team, *github.Response, error)
	ListTeamMembersBySlug(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error)
	CreateTeam(ctx context.Context, org string, team github.NewTeam) (*github.Team, *github.Response, error)
	AddTeamMembershipBySlug(ctx context.Context, org, slug, user string, opts *github.TeamAddTeamMembershipOptions) (*github.Membership, *github.Response, error)
	RemoveTeamMembershipBySlug(ctx context.Context, org, slug, user string) (*github.Response, error)
	ListPendingTeamInvitationsBySlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error)
	ListChildTeamsByParentSlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error)
	EditTeamBySlug(ctx context.Context, org, slug string, team github.NewTeam, removeParent bool) (*github.Team, *github.Response, error)
	DeleteTeamBySlug(ctx context.Context, org, slug string) (*github.Response, error)
	AddTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error)
	RemoveTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string) (*github.Response, error)
	IsTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string) (*github.Repository, *github.Response, error)
}

type RepositoriesClient interface {
	Get(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error)
	Edit(ctx context.Context, owner, repo string, repository *github.Repository) (*github.Repository, *github.Response, error)
	ListTeams(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error)
	ListCollaborators(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error)
	ListInvitations(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.RepositoryInvitation, *github.Response, error)
	Create(ctx context.Context, org string, repo *github.Repository) (*github.Repository, *github.Response, error)
	CreateFromTemplate(ctx context.Context, templateOwner, templateRepo string, templateRepoReq github.TemplateRepoRequest) (*github.Repository, *github.Response, error)
	CreateFork(ctx context.Context, owner, repo string, opts *github.RepositoryCreateForkOptions) (*github.Repository, *github.Response, error)
	AddCollaborator(ctx context.Context, owner, repo, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error)
	RemoveCollaborator(ctx context.Context, owner, repo, user string) (*github.Response, error)
	GetPermissionLevel(ctx context.Context, owner, repo, user string) (*github.RepositoryPermissionLevel, *github.Response, error)
	Delete(ctx context.Context, owner, repo string) (*github.Response, error)
	CreateHook(ctx context.Context, owner, repo string, hook *github.Hook) (*github.Hook, *github.Response, error)
	EditHook(ctx context.Context, owner, repo string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error)
	DeleteHook(ctx context.Context, owner, repo string, id int64) (*github.Response, error)
	ListHooks(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error)
	ListBranches(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error)
	GetBranch(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error)
	GetBranchProtection(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error)
	UpdateBranchProtection(ctx context.Context, owner, repo, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error)
	RemoveBranchProtection(ctx context.Context, owner, repo, branch string) (*github.Response, error)
	RequireSignaturesOnProtectedBranch(ctx context.Context, owner, repo, branch string) (*github.SignaturesProtectedBranch, *github.Response, error)
	OptionalSignaturesOnProtectedBranch(ctx context.Context, owner, repo, branch string) (*github.Response, error)
	GetAllRulesets(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*github.RepositoryRuleset, *github.Response, error)
	GetRuleset(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*github.RepositoryRuleset, *github.Response, error)
	CreateRuleset(ctx context.Context, owner, repo string, ruleset github.RepositoryRuleset) (*github.RepositoryRuleset, *github.Response, error)
	UpdateRuleset(ctx context.Context, owner, repo string, rulesetID int64, ruleset github.RepositoryRuleset) (*github.RepositoryRuleset, *github.Response, error)
	DeleteRuleset(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error)
	ReplaceAllTopics(ctx context.Context, owner, repo string, topics []string) ([]string, *github.Response, error)
}

func Is404(err error) bool {
	var errResp *github.ErrorResponse

	if errors.As(err, &errResp) && errResp.Response.StatusCode == 404 {
		return true
	}

	return false
}
