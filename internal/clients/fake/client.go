package fake

import (
	"context"
	"net/http"

	"github.com/google/go-github/v90/github"

	"github.com/provider-github/provider-github/internal/clients/rulesets"
)

type MockActionsClient struct {
	MockListEnabledReposInOrg           func(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error)
	MockSetEnabledReposInOrg            func(ctx context.Context, owner string, repositoryIDs []int64) (*github.Response, error)
	MockGetOrgPublicKey                 func(ctx context.Context, org string) (*github.PublicKey, *github.Response, error)
	MockGetOrgSecret                    func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error)
	MockCreateOrUpdateOrgSecret         func(ctx context.Context, org, name string, secret github.SecretOrgRequest) (*github.Response, error)
	MockDeleteOrgSecret                 func(ctx context.Context, org, name string) (*github.Response, error)
	MockListSelectedReposForOrgSecret   func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
	MockSetSelectedReposForOrgSecret    func(ctx context.Context, org, name string, ids []int64) (*github.Response, error)
	MockGetOrgVariable                  func(ctx context.Context, org, name string) (*github.ActionsVariable, *github.Response, error)
	MockCreateOrgVariable               func(ctx context.Context, org string, variable github.ActionsCreateOrgVariableRequest) (*github.Response, error)
	MockUpdateOrgVariable               func(ctx context.Context, org, name string, variable github.ActionsUpdateOrgVariableRequest) (*github.Response, error)
	MockDeleteOrgVariable               func(ctx context.Context, org, name string) (*github.Response, error)
	MockListSelectedReposForOrgVariable func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
	MockListOrganizationRunnerGroups    func(ctx context.Context, org string, opts *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error)
	MockCreateOrganizationRunnerGroup   func(ctx context.Context, org string, createReq github.CreateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error)
	MockUpdateOrganizationRunnerGroup   func(ctx context.Context, org string, groupID int64, updateReq github.UpdateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error)
	MockDeleteOrganizationRunnerGroup   func(ctx context.Context, org string, groupID int64) (*github.Response, error)
	MockListRepositoryAccessRunnerGroup func(ctx context.Context, org string, groupID int64, opts *github.ListOptions) (*github.ListRepositories, *github.Response, error)
	MockSetRepositoryAccessRunnerGroup  func(ctx context.Context, org string, groupID int64, ids github.SetRepoAccessRunnerGroupRequest) (*github.Response, error)
}

func (m *MockActionsClient) ListEnabledReposInOrg(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
	return m.MockListEnabledReposInOrg(ctx, owner, opts)
}

func (m *MockActionsClient) SetEnabledReposInOrg(ctx context.Context, owner string, repositoryIDs []int64) (*github.Response, error) {
	return m.MockSetEnabledReposInOrg(ctx, owner, repositoryIDs)
}

func (m *MockActionsClient) GetOrgPublicKey(ctx context.Context, org string) (*github.PublicKey, *github.Response, error) {
	return m.MockGetOrgPublicKey(ctx, org)
}

func (m *MockActionsClient) CreateOrUpdateOrgSecret(ctx context.Context, org, name string, secret github.SecretOrgRequest) (*github.Response, error) {
	return m.MockCreateOrUpdateOrgSecret(ctx, org, name, secret)
}

func (m *MockActionsClient) DeleteOrgSecret(ctx context.Context, org, name string) (*github.Response, error) {
	return m.MockDeleteOrgSecret(ctx, org, name)
}

func (m *MockActionsClient) GetOrgSecret(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
	return m.MockGetOrgSecret(ctx, org, name)
}

func (m *MockActionsClient) ListSelectedReposForOrgSecret(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return m.MockListSelectedReposForOrgSecret(ctx, org, name, opts)
}

func (m *MockActionsClient) SetSelectedReposForOrgSecret(ctx context.Context, org, name string, ids []int64) (*github.Response, error) {
	return m.MockSetSelectedReposForOrgSecret(ctx, org, name, ids)
}

func (m *MockActionsClient) GetOrgVariable(ctx context.Context, org, name string) (*github.ActionsVariable, *github.Response, error) {
	return m.MockGetOrgVariable(ctx, org, name)
}

func (m *MockActionsClient) CreateOrgVariable(ctx context.Context, org string, variable github.ActionsCreateOrgVariableRequest) (*github.Response, error) {
	return m.MockCreateOrgVariable(ctx, org, variable)
}

func (m *MockActionsClient) UpdateOrgVariable(ctx context.Context, org, name string, variable github.ActionsUpdateOrgVariableRequest) (*github.Response, error) {
	return m.MockUpdateOrgVariable(ctx, org, name, variable)
}

func (m *MockActionsClient) DeleteOrgVariable(ctx context.Context, org, name string) (*github.Response, error) {
	return m.MockDeleteOrgVariable(ctx, org, name)
}

func (m *MockActionsClient) ListSelectedReposForOrgVariable(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return m.MockListSelectedReposForOrgVariable(ctx, org, name, opts)
}

func (m *MockActionsClient) ListOrganizationRunnerGroups(ctx context.Context, org string, opts *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error) {
	return m.MockListOrganizationRunnerGroups(ctx, org, opts)
}

func (m *MockActionsClient) CreateOrganizationRunnerGroup(ctx context.Context, org string, createReq github.CreateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error) {
	return m.MockCreateOrganizationRunnerGroup(ctx, org, createReq)
}

func (m *MockActionsClient) UpdateOrganizationRunnerGroup(ctx context.Context, org string, groupID int64, updateReq github.UpdateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error) {
	return m.MockUpdateOrganizationRunnerGroup(ctx, org, groupID, updateReq)
}

func (m *MockActionsClient) DeleteOrganizationRunnerGroup(ctx context.Context, org string, groupID int64) (*github.Response, error) {
	return m.MockDeleteOrganizationRunnerGroup(ctx, org, groupID)
}

func (m *MockActionsClient) ListRepositoryAccessRunnerGroup(ctx context.Context, org string, groupID int64, opts *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
	return m.MockListRepositoryAccessRunnerGroup(ctx, org, groupID, opts)
}

func (m *MockActionsClient) SetRepositoryAccessRunnerGroup(ctx context.Context, org string, groupID int64, ids github.SetRepoAccessRunnerGroupRequest) (*github.Response, error) {
	return m.MockSetRepositoryAccessRunnerGroup(ctx, org, groupID, ids)
}

type MockDependabotClient struct {
	MockGetOrgSecret                  func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error)
	MockListSelectedReposForOrgSecret func(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
	MockSetSelectedReposForOrgSecret  func(ctx context.Context, org, name string, ids []int64) (*github.Response, error)
}

func (m *MockDependabotClient) GetOrgSecret(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
	return m.MockGetOrgSecret(ctx, org, name)
}

func (m *MockDependabotClient) ListSelectedReposForOrgSecret(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return m.MockListSelectedReposForOrgSecret(ctx, org, name, opts)
}

func (m *MockDependabotClient) SetSelectedReposForOrgSecret(ctx context.Context, org, name string, ids []int64) (*github.Response, error) {
	return m.MockSetSelectedReposForOrgSecret(ctx, org, name, ids)
}

type MockOrganizationsClient struct {
	MockGet                 func(ctx context.Context, org string) (*github.Organization, *github.Response, error)
	MockEdit                func(ctx context.Context, name string, org *github.Organization) (*github.Organization, *github.Response, error)
	MockGetOrgMembership    func(ctx context.Context, user, org string) (*github.Membership, *github.Response, error)
	MockCreateOrgInvitation func(ctx context.Context, org string, opts *github.CreateOrgInvitationOptions) (*github.Invitation, *github.Response, error)
	MockEditOrgMembership   func(ctx context.Context, user, org string, membership *github.Membership) (*github.Membership, *github.Response, error)
	MockRemoveOrgMembership func(ctx context.Context, user, org string) (*github.Response, error)
	MockListHooks           func(ctx context.Context, org string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error)
	MockGetHook             func(ctx context.Context, org string, id int64) (*github.Hook, *github.Response, error)
	MockCreateHook          func(ctx context.Context, org string, hook *github.Hook) (*github.Hook, *github.Response, error)
	MockEditHook            func(ctx context.Context, org string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error)
	MockDeleteHook          func(ctx context.Context, org string, id int64) (*github.Response, error)
}

func (m *MockOrganizationsClient) Get(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
	return m.MockGet(ctx, org)
}

func (m *MockOrganizationsClient) Edit(ctx context.Context, name string, org *github.Organization) (*github.Organization, *github.Response, error) {
	return m.MockEdit(ctx, name, org)
}

func (m *MockOrganizationsClient) GetOrgMembership(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
	return m.MockGetOrgMembership(ctx, user, org)
}

func (m *MockOrganizationsClient) CreateOrgInvitation(ctx context.Context, org string, opts *github.CreateOrgInvitationOptions) (*github.Invitation, *github.Response, error) {
	return m.MockCreateOrgInvitation(ctx, org, opts)
}

func (m *MockOrganizationsClient) EditOrgMembership(ctx context.Context, user, org string, membership *github.Membership) (*github.Membership, *github.Response, error) {
	return m.MockEditOrgMembership(ctx, user, org, membership)
}

func (m *MockOrganizationsClient) RemoveOrgMembership(ctx context.Context, user, org string) (*github.Response, error) {
	return m.MockRemoveOrgMembership(ctx, user, org)
}

func (m *MockOrganizationsClient) ListHooks(ctx context.Context, org string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
	return m.MockListHooks(ctx, org, opts)
}

func (m *MockOrganizationsClient) GetHook(ctx context.Context, org string, id int64) (*github.Hook, *github.Response, error) {
	return m.MockGetHook(ctx, org, id)
}

func (m *MockOrganizationsClient) CreateHook(ctx context.Context, org string, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return m.MockCreateHook(ctx, org, hook)
}

func (m *MockOrganizationsClient) EditHook(ctx context.Context, org string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return m.MockEditHook(ctx, org, id, hook)
}

func (m *MockOrganizationsClient) DeleteHook(ctx context.Context, org string, id int64) (*github.Response, error) {
	return m.MockDeleteHook(ctx, org, id)
}

type MockUsersClient struct {
	MockGet func(ctx context.Context, user string) (*github.User, *github.Response, error)
}

func (m *MockUsersClient) Get(ctx context.Context, user string) (*github.User, *github.Response, error) {
	return m.MockGet(ctx, user)
}

type MockRepositoriesClient struct {
	MockGet                                 func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error)
	MockEdit                                func(ctx context.Context, owner, repo string, repository *github.Repository) (*github.Repository, *github.Response, error)
	MockListTeams                           func(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error)
	MockListCollaborators                   func(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error)
	MockListInvitations                     func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.RepositoryInvitation, *github.Response, error)
	MockCreate                              func(ctx context.Context, org string, repo *github.Repository) (*github.Repository, *github.Response, error)
	MockCreateFromTemplate                  func(ctx context.Context, templateOwner, templateRepo string, templateRepoReq github.TemplateRepoRequest) (*github.Repository, *github.Response, error)
	MockCreateFork                          func(ctx context.Context, owner, repo string, opts *github.RepositoryCreateForkOptions) (*github.Repository, *github.Response, error)
	MockAddCollaborator                     func(ctx context.Context, owner, repo, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error)
	MockRemoveCollaborator                  func(ctx context.Context, owner, repo, user string) (*github.Response, error)
	MockGetPermissionLevel                  func(ctx context.Context, owner, repo, user string) (*github.RepositoryPermissionLevel, *github.Response, error)
	MockDelete                              func(ctx context.Context, owner, repo string) (*github.Response, error)
	MockCreateHook                          func(ctx context.Context, owner, repo string, hook *github.Hook) (*github.Hook, *github.Response, error)
	MockEditHook                            func(ctx context.Context, owner, repo string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error)
	MockDeleteHook                          func(ctx context.Context, owner, repo string, id int64) (*github.Response, error)
	MockListHooks                           func(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error)
	MockListBranches                        func(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error)
	MockGetBranch                           func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error)
	MockGetBranchProtection                 func(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error)
	MockUpdateBranchProtection              func(ctx context.Context, owner, repo, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error)
	MockRemoveBranchProtection              func(ctx context.Context, owner, repo, branch string) (*github.Response, error)
	MockRequireSignaturesOnProtectedBranch  func(ctx context.Context, owner, repo, branch string) (*github.SignaturesProtectedBranch, *github.Response, error)
	MockOptionalSignaturesOnProtectedBranch func(ctx context.Context, owner, repo, branch string) (*github.Response, error)
	MockReplaceAllTopics                    func(ctx context.Context, owner, repo string, topics []string) ([]string, *github.Response, error)
}

func (m *MockRepositoriesClient) Get(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
	return m.MockGet(ctx, owner, repo)
}

func (m *MockRepositoriesClient) Edit(ctx context.Context, owner, repo string, repository *github.Repository) (*github.Repository, *github.Response, error) {
	return m.MockEdit(ctx, owner, repo, repository)
}

func (m *MockRepositoriesClient) AddCollaborator(ctx context.Context, owner, repo, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
	return m.MockAddCollaborator(ctx, owner, repo, user, opts)
}

func (m *MockRepositoriesClient) Create(ctx context.Context, org string, repo *github.Repository) (*github.Repository, *github.Response, error) {
	return m.MockCreate(ctx, org, repo)
}

func (m *MockRepositoriesClient) CreateFromTemplate(ctx context.Context, templateOwner, templateRepo string, templateRepoReq github.TemplateRepoRequest) (*github.Repository, *github.Response, error) {
	return m.MockCreateFromTemplate(ctx, templateOwner, templateRepo, templateRepoReq)
}

func (m *MockRepositoriesClient) CreateFork(ctx context.Context, owner, repo string, opts *github.RepositoryCreateForkOptions) (*github.Repository, *github.Response, error) {
	return m.MockCreateFork(ctx, owner, repo, opts)
}

func (m *MockRepositoriesClient) Delete(ctx context.Context, owner, repo string) (*github.Response, error) {
	return m.MockDelete(ctx, owner, repo)
}

func (m *MockRepositoriesClient) ListCollaborators(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
	return m.MockListCollaborators(ctx, owner, repo, opts)
}

func (m *MockRepositoriesClient) ListInvitations(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.RepositoryInvitation, *github.Response, error) {
	return m.MockListInvitations(ctx, owner, repo, opts)
}

func (m *MockRepositoriesClient) ListTeams(ctx context.Context, owner string, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
	return m.MockListTeams(ctx, owner, repo, opts)
}

func (m *MockRepositoriesClient) RemoveCollaborator(ctx context.Context, owner, repo, user string) (*github.Response, error) {
	return m.MockRemoveCollaborator(ctx, owner, repo, user)
}

func (m *MockRepositoriesClient) GetPermissionLevel(ctx context.Context, owner, repo, user string) (*github.RepositoryPermissionLevel, *github.Response, error) {
	return m.MockGetPermissionLevel(ctx, owner, repo, user)
}

func (m *MockRepositoriesClient) CreateHook(ctx context.Context, owner, repo string, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return m.MockCreateHook(ctx, owner, repo, hook)
}

func (m *MockRepositoriesClient) EditHook(ctx context.Context, owner, repo string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return m.MockEditHook(ctx, owner, repo, id, hook)
}

func (m *MockRepositoriesClient) DeleteHook(ctx context.Context, owner, repo string, id int64) (*github.Response, error) {
	return m.MockDeleteHook(ctx, owner, repo, id)
}

func (m *MockRepositoriesClient) ListHooks(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
	return m.MockListHooks(ctx, owner, repo, opts)
}

func (m *MockRepositoriesClient) ListBranches(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
	return m.MockListBranches(ctx, owner, repo, opts)
}

func (m *MockRepositoriesClient) GetBranch(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
	return m.MockGetBranch(ctx, owner, repo, branch, maxRedirects)
}

func (m *MockRepositoriesClient) GetBranchProtection(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
	return m.MockGetBranchProtection(ctx, owner, repo, branch)
}

func (m *MockRepositoriesClient) UpdateBranchProtection(ctx context.Context, owner, repo, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error) {
	return m.MockUpdateBranchProtection(ctx, owner, repo, branch, preq)
}

func (m *MockRepositoriesClient) RemoveBranchProtection(ctx context.Context, owner, repo, branch string) (*github.Response, error) {
	return m.MockRemoveBranchProtection(ctx, owner, repo, branch)
}

func (m *MockRepositoriesClient) RequireSignaturesOnProtectedBranch(ctx context.Context, owner, repo, branch string) (*github.SignaturesProtectedBranch, *github.Response, error) {
	return m.MockRequireSignaturesOnProtectedBranch(ctx, owner, repo, branch)
}

func (m *MockRepositoriesClient) OptionalSignaturesOnProtectedBranch(ctx context.Context, owner, repo, branch string) (*github.Response, error) {
	return m.MockOptionalSignaturesOnProtectedBranch(ctx, owner, repo, branch)
}

func (m *MockRepositoriesClient) ReplaceAllTopics(ctx context.Context, owner, repo string, topics []string) ([]string, *github.Response, error) {
	return m.MockReplaceAllTopics(ctx, owner, repo, topics)
}

type MockRulesetsClient struct {
	MockGetAllRulesets func(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error)
	MockGetRuleset     func(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error)
	MockCreateRuleset  func(ctx context.Context, owner, repo string, ruleset rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error)
	MockUpdateRuleset  func(ctx context.Context, owner, repo string, rulesetID int64, ruleset rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error)
	MockDeleteRuleset  func(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error)
}

func (m *MockRulesetsClient) GetAllRulesets(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
	return m.MockGetAllRulesets(ctx, owner, repo, opts)
}

func (m *MockRulesetsClient) GetRuleset(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
	return m.MockGetRuleset(ctx, owner, repo, rulesetID, includesParents)
}

func (m *MockRulesetsClient) CreateRuleset(ctx context.Context, owner, repo string, ruleset rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
	return m.MockCreateRuleset(ctx, owner, repo, ruleset)
}

func (m *MockRulesetsClient) UpdateRuleset(ctx context.Context, owner, repo string, rulesetID int64, ruleset rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
	return m.MockUpdateRuleset(ctx, owner, repo, rulesetID, ruleset)
}

func (m *MockRulesetsClient) DeleteRuleset(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
	return m.MockDeleteRuleset(ctx, owner, repo, rulesetID)
}

type MockTeamsClient struct {
	MockGetTeamBySlug                    func(ctx context.Context, org, slug string) (*github.Team, *github.Response, error)
	MockListTeamMembersBySlug            func(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error)
	MockCreateTeam                       func(ctx context.Context, org string, team github.NewTeam) (*github.Team, *github.Response, error)
	MockAddTeamMembershipBySlug          func(ctx context.Context, org, slug, user string, opts *github.TeamAddTeamMembershipOptions) (*github.Membership, *github.Response, error)
	MockRemoveTeamMembershipBySlug       func(ctx context.Context, org, slug, user string) (*github.Response, error)
	MockEditTeamBySlug                   func(ctx context.Context, org, slug string, team github.NewTeam, removeParent bool) (*github.Team, *github.Response, error)
	MockDeleteTeamBySlug                 func(ctx context.Context, org, slug string) (*github.Response, error)
	MockAddTeamRepoBySlug                func(ctx context.Context, org, slug, owner, repo string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error)
	MockRemoveTeamRepoBySlug             func(ctx context.Context, org, slug, owner, repo string) (*github.Response, error)
	MockIsTeamRepoBySlug                 func(ctx context.Context, org, slug, owner, repo string) (*github.Repository, *github.Response, error)
	MockListPendingTeamInvitationsBySlug func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error)
	MockListChildTeamsByParentSlug       func(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error)
}

func (m *MockTeamsClient) IsTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string) (*github.Repository, *github.Response, error) {
	return m.MockIsTeamRepoBySlug(ctx, org, slug, owner, repo)
}

func (m *MockTeamsClient) RemoveTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string) (*github.Response, error) {
	return m.MockRemoveTeamRepoBySlug(ctx, org, slug, owner, repo)
}

func (m *MockTeamsClient) RemoveTeamMembershipBySlug(ctx context.Context, org, slug, user string) (*github.Response, error) {
	return m.MockRemoveTeamMembershipBySlug(ctx, org, slug, user)
}

func (m *MockTeamsClient) ListTeamMembersBySlug(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
	return m.MockListTeamMembersBySlug(ctx, org, slug, opts)
}

func (m *MockTeamsClient) EditTeamBySlug(ctx context.Context, org, slug string, team github.NewTeam, removeParent bool) (*github.Team, *github.Response, error) {
	return m.MockEditTeamBySlug(ctx, org, slug, team, removeParent)
}

func (m *MockTeamsClient) DeleteTeamBySlug(ctx context.Context, org, slug string) (*github.Response, error) {
	return m.MockDeleteTeamBySlug(ctx, org, slug)
}

func (m *MockTeamsClient) CreateTeam(ctx context.Context, org string, team github.NewTeam) (*github.Team, *github.Response, error) {
	return m.MockCreateTeam(ctx, org, team)
}

func (m *MockTeamsClient) GetTeamBySlug(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
	return m.MockGetTeamBySlug(ctx, org, slug)
}

func (m *MockTeamsClient) AddTeamMembershipBySlug(ctx context.Context, org, slug, user string, opts *github.TeamAddTeamMembershipOptions) (*github.Membership, *github.Response, error) {
	return m.MockAddTeamMembershipBySlug(ctx, org, slug, user, opts)
}

func (m *MockTeamsClient) AddTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
	return m.MockAddTeamRepoBySlug(ctx, org, slug, owner, repo, opts)
}

func (m *MockTeamsClient) ListPendingTeamInvitationsBySlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
	return m.MockListPendingTeamInvitationsBySlug(ctx, org, slug, opts)
}

func (m *MockTeamsClient) ListChildTeamsByParentSlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
	return m.MockListChildTeamsByParentSlug(ctx, org, slug, opts)
}

func Generate404Response() *github.ErrorResponse {
	return &github.ErrorResponse{
		Response: &http.Response{
			StatusCode: 404,
		},
	}
}

func GenerateEmptyResponse() *github.Response {
	return &github.Response{
		NextPage: 0,
	}
}
