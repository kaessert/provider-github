/*
Copyright 2024 The Crossplane Authors.

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

	"github.com/google/go-github/v90/github"
	"github.com/provider-github/provider-github/internal/clients/rulesets"
	"github.com/provider-github/provider-github/internal/telemetry"
)

// Client is the GitHub client controllers use. It wraps a *Services with
// per-credential labels so every API call is recorded into Prometheus
// telemetry and the per-credential cooldown pool.
type Client struct {
	*Services
	metrics *telemetry.RateLimitMetrics
}

// NewClient builds a Client around a *Services (typically returned by
// NewCachedServices) so every wrapped call carries rate-limit telemetry.
func NewClient(services *Services, metrics *telemetry.RateLimitMetrics) *Client {
	return &Client{
		Services: services,
		metrics:  metrics,
	}
}

// WithRateLimitTracking returns a new Client whose service wrappers tag
// every API response with the given identifiers:
//   - org is the GitHub organization for the Prometheus organization label;
//   - appID and installationID are the GitHub App ID and Installation ID,
//     used as the app_id and app_installation_id metric labels;
//   - cacheKey is the credential cache key, used internally by the per-app
//     quota pool to track 429s and steer future picks.
//
// appID/installationID and cacheKey are separate so that the metric labels
// stay human-meaningful while the pool keeps its stable internal identity.
func (c *Client) WithRateLimitTracking(org, appID, installationID, cacheKey string) *Client {
	return &Client{
		Services: &Services{
			Actions: &actionsClient{
				ActionsClient:  c.Actions,
				metrics:        c.metrics,
				org:            org,
				appID:          appID,
				installationID: installationID,
				cacheKey:       cacheKey,
			},
			Dependabot: &dependabotClient{
				DependabotClient: c.Dependabot,
				metrics:          c.metrics,
				org:              org,
				appID:            appID,
				installationID:   installationID,
				cacheKey:         cacheKey,
			},
			Organizations: &organizationsClient{
				OrganizationsClient: c.Organizations,
				metrics:             c.metrics,
				org:                 org,
				appID:               appID,
				installationID:      installationID,
				cacheKey:            cacheKey,
			},
			Users: &usersClient{
				UsersClient:    c.Users,
				metrics:        c.metrics,
				org:            org,
				appID:          appID,
				installationID: installationID,
				cacheKey:       cacheKey,
			},
			Teams: &teamsClient{
				TeamsClient:    c.Teams,
				metrics:        c.metrics,
				org:            org,
				appID:          appID,
				installationID: installationID,
				cacheKey:       cacheKey,
			},
			Repositories: &repositoriesClient{
				RepositoriesClient: c.Repositories,
				metrics:            c.metrics,
				org:                org,
				appID:              appID,
				installationID:     installationID,
				cacheKey:           cacheKey,
			},
			Rulesets: &rulesetsClient{
				RulesetsClient: c.Rulesets,
				metrics:        c.metrics,
				org:            org,
				appID:          appID,
				installationID: installationID,
				cacheKey:       cacheKey,
			},
		},
		metrics: c.metrics,
	}
}

// recordResponse fans the outcome of a GitHub call out to both Prometheus
// telemetry and the per-app quota pool. metrics may be nil (e.g. in unit
// tests); the pool is always updated so the picker stays accurate.
//
// Every call increments github_api_calls_total. When the call returns no
// HTTP response but an error (typically because ghinstallation failed to
// mint a token), github_app_unhealthy_total is also incremented and the
// pool records a short cooldown.
func recordResponse(metrics *telemetry.RateLimitMetrics, org, appID, installationID, cacheKey, method string, resp *github.Response, err error) {
	if metrics != nil {
		metrics.RecordAPICall(org, appID, installationID, method)
		metrics.RecordRateLimitInfo(resp, org, appID, installationID)
		if resp == nil && err != nil {
			metrics.RecordAppUnhealthy(org, appID, installationID)
		}
	}
	globalPool.recordResponse(cacheKey, resp, err)
}

// recordRateLimit wraps a value-returning GitHub call so the response's
// rate-limit information is recorded once it returns.
func recordRateLimit[T any](
	_ context.Context,
	metrics *telemetry.RateLimitMetrics,
	org, appID, installationID, cacheKey, method string,
	fn func() (T, *github.Response, error),
) (T, *github.Response, error) {
	result, resp, err := fn()
	recordResponse(metrics, org, appID, installationID, cacheKey, method, resp, err)
	return result, resp, err
}

// actionsClient wraps the Actions client with rate limit tracking
type actionsClient struct {
	ActionsClient
	metrics        *telemetry.RateLimitMetrics
	org            string
	appID          string
	installationID string
	cacheKey       string
}

// organizationsClient wraps the Organizations client with rate limit tracking
type organizationsClient struct {
	OrganizationsClient
	metrics        *telemetry.RateLimitMetrics
	org            string
	appID          string
	installationID string
	cacheKey       string
}

// repositoriesClient wraps the Repositories client with rate limit tracking
type repositoriesClient struct {
	RepositoriesClient
	metrics        *telemetry.RateLimitMetrics
	org            string
	appID          string
	installationID string
	cacheKey       string
}

// rulesetsClient wraps the Rulesets client with rate limit tracking
type rulesetsClient struct {
	RulesetsClient
	metrics        *telemetry.RateLimitMetrics
	org            string
	appID          string
	installationID string
	cacheKey       string
}

// teamsClient wraps the Teams client with rate limit tracking
type teamsClient struct {
	TeamsClient
	metrics        *telemetry.RateLimitMetrics
	org            string
	appID          string
	installationID string
	cacheKey       string
}

// usersClient wraps the Users client with rate limit tracking
type usersClient struct {
	UsersClient
	metrics        *telemetry.RateLimitMetrics
	org            string
	appID          string
	installationID string
	cacheKey       string
}

// dependabotClient wraps the Dependabot client with rate limit tracking
type dependabotClient struct {
	DependabotClient
	metrics        *telemetry.RateLimitMetrics
	org            string
	appID          string
	installationID string
	cacheKey       string
}

// Sample implementations for key methods - only tracking rate limits

// organizationsClient methods
func (c *organizationsClient) Get(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.Get", func() (*github.Organization, *github.Response, error) {
		return c.OrganizationsClient.Get(ctx, org)
	})
}

func (c *organizationsClient) Edit(ctx context.Context, name string, org *github.Organization) (*github.Organization, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.Edit", func() (*github.Organization, *github.Response, error) {
		return c.OrganizationsClient.Edit(ctx, name, org)
	})
}

func (c *organizationsClient) GetOrgMembership(ctx context.Context, user, org string) (*github.Membership, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.GetOrgMembership", func() (*github.Membership, *github.Response, error) {
		return c.OrganizationsClient.GetOrgMembership(ctx, user, org)
	})
}

func (c *organizationsClient) CreateOrgInvitation(ctx context.Context, org string, opts *github.CreateOrgInvitationOptions) (*github.Invitation, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.CreateOrgInvitation", func() (*github.Invitation, *github.Response, error) {
		return c.OrganizationsClient.CreateOrgInvitation(ctx, org, opts)
	})
}

func (c *organizationsClient) EditOrgMembership(ctx context.Context, user, org string, membership *github.Membership) (*github.Membership, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.EditOrgMembership", func() (*github.Membership, *github.Response, error) {
		return c.OrganizationsClient.EditOrgMembership(ctx, user, org, membership)
	})
}

func (c *organizationsClient) RemoveOrgMembership(ctx context.Context, user, org string) (*github.Response, error) {
	resp, err := c.OrganizationsClient.RemoveOrgMembership(ctx, user, org)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.RemoveOrgMembership", resp, err)
	return resp, err
}

func (c *organizationsClient) ListHooks(ctx context.Context, org string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.ListHooks", func() ([]*github.Hook, *github.Response, error) {
		return c.OrganizationsClient.ListHooks(ctx, org, opts)
	})
}

func (c *organizationsClient) GetHook(ctx context.Context, org string, id int64) (*github.Hook, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.GetHook", func() (*github.Hook, *github.Response, error) {
		return c.OrganizationsClient.GetHook(ctx, org, id)
	})
}

func (c *organizationsClient) CreateHook(ctx context.Context, org string, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.CreateHook", func() (*github.Hook, *github.Response, error) {
		return c.OrganizationsClient.CreateHook(ctx, org, hook)
	})
}

func (c *organizationsClient) EditHook(ctx context.Context, org string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.EditHook", func() (*github.Hook, *github.Response, error) {
		return c.OrganizationsClient.EditHook(ctx, org, id, hook)
	})
}

func (c *organizationsClient) DeleteHook(ctx context.Context, org string, id int64) (*github.Response, error) {
	resp, err := c.OrganizationsClient.DeleteHook(ctx, org, id)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Organizations.DeleteHook", resp, err)
	return resp, err
}

// repositoriesClient methods
func (c *repositoriesClient) Get(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.Get", func() (*github.Repository, *github.Response, error) {
		return c.RepositoriesClient.Get(ctx, owner, repo)
	})
}

func (c *repositoriesClient) Edit(ctx context.Context, owner, repo string, repository *github.Repository) (*github.Repository, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.Edit", func() (*github.Repository, *github.Response, error) {
		return c.RepositoriesClient.Edit(ctx, owner, repo, repository)
	})
}

func (c *repositoriesClient) Create(ctx context.Context, org string, repo *github.Repository) (*github.Repository, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.Create", func() (*github.Repository, *github.Response, error) {
		return c.RepositoriesClient.Create(ctx, org, repo)
	})
}

func (c *repositoriesClient) Delete(ctx context.Context, owner, repo string) (*github.Response, error) {
	resp, err := c.RepositoriesClient.Delete(ctx, owner, repo)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.Delete", resp, err)
	return resp, err
}

func (c *repositoriesClient) ListTeams(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.ListTeams", func() ([]*github.Team, *github.Response, error) {
		return c.RepositoriesClient.ListTeams(ctx, owner, repo, opts)
	})
}

func (c *repositoriesClient) ListCollaborators(ctx context.Context, owner, repo string, opts *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.ListCollaborators", func() ([]*github.User, *github.Response, error) {
		return c.RepositoriesClient.ListCollaborators(ctx, owner, repo, opts)
	})
}

func (c *repositoriesClient) ListInvitations(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.RepositoryInvitation, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.ListInvitations", func() ([]*github.RepositoryInvitation, *github.Response, error) {
		return c.RepositoriesClient.ListInvitations(ctx, owner, repo, opts)
	})
}

func (c *repositoriesClient) CreateFromTemplate(ctx context.Context, templateOwner, templateRepo string, templateRepoReq github.TemplateRepoRequest) (*github.Repository, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.CreateFromTemplate", func() (*github.Repository, *github.Response, error) {
		return c.RepositoriesClient.CreateFromTemplate(ctx, templateOwner, templateRepo, templateRepoReq)
	})
}

func (c *repositoriesClient) CreateFork(ctx context.Context, owner, repo string, opts *github.RepositoryCreateForkOptions) (*github.Repository, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.CreateFork", func() (*github.Repository, *github.Response, error) {
		return c.RepositoriesClient.CreateFork(ctx, owner, repo, opts)
	})
}

func (c *repositoriesClient) AddCollaborator(ctx context.Context, owner, repo, user string, opts *github.RepositoryAddCollaboratorOptions) (*github.CollaboratorInvitation, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.AddCollaborator", func() (*github.CollaboratorInvitation, *github.Response, error) {
		return c.RepositoriesClient.AddCollaborator(ctx, owner, repo, user, opts)
	})
}

func (c *repositoriesClient) RemoveCollaborator(ctx context.Context, owner, repo, user string) (*github.Response, error) {
	resp, err := c.RepositoriesClient.RemoveCollaborator(ctx, owner, repo, user)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.RemoveCollaborator", resp, err)
	return resp, err
}

func (c *repositoriesClient) GetPermissionLevel(ctx context.Context, owner, repo, user string) (*github.RepositoryPermissionLevel, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.GetPermissionLevel", func() (*github.RepositoryPermissionLevel, *github.Response, error) {
		return c.RepositoriesClient.GetPermissionLevel(ctx, owner, repo, user)
	})
}

func (c *repositoriesClient) CreateHook(ctx context.Context, owner, repo string, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.CreateHook", func() (*github.Hook, *github.Response, error) {
		return c.RepositoriesClient.CreateHook(ctx, owner, repo, hook)
	})
}

func (c *repositoriesClient) EditHook(ctx context.Context, owner, repo string, id int64, hook *github.Hook) (*github.Hook, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.EditHook", func() (*github.Hook, *github.Response, error) {
		return c.RepositoriesClient.EditHook(ctx, owner, repo, id, hook)
	})
}

func (c *repositoriesClient) DeleteHook(ctx context.Context, owner, repo string, id int64) (*github.Response, error) {
	resp, err := c.RepositoriesClient.DeleteHook(ctx, owner, repo, id)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.DeleteHook", resp, err)
	return resp, err
}

func (c *repositoriesClient) ListHooks(ctx context.Context, owner, repo string, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.ListHooks", func() ([]*github.Hook, *github.Response, error) {
		return c.RepositoriesClient.ListHooks(ctx, owner, repo, opts)
	})
}

func (c *repositoriesClient) ListBranches(ctx context.Context, owner, repo string, opts *github.BranchListOptions) ([]*github.Branch, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.ListBranches", func() ([]*github.Branch, *github.Response, error) {
		return c.RepositoriesClient.ListBranches(ctx, owner, repo, opts)
	})
}

func (c *repositoriesClient) GetBranch(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.GetBranch", func() (*github.Branch, *github.Response, error) {
		return c.RepositoriesClient.GetBranch(ctx, owner, repo, branch, maxRedirects)
	})
}

func (c *repositoriesClient) GetBranchProtection(ctx context.Context, owner, repo, branch string) (*github.Protection, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.GetBranchProtection", func() (*github.Protection, *github.Response, error) {
		return c.RepositoriesClient.GetBranchProtection(ctx, owner, repo, branch)
	})
}

func (c *repositoriesClient) UpdateBranchProtection(ctx context.Context, owner, repo, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.UpdateBranchProtection", func() (*github.Protection, *github.Response, error) {
		return c.RepositoriesClient.UpdateBranchProtection(ctx, owner, repo, branch, preq)
	})
}

func (c *repositoriesClient) RemoveBranchProtection(ctx context.Context, owner, repo, branch string) (*github.Response, error) {
	resp, err := c.RepositoriesClient.RemoveBranchProtection(ctx, owner, repo, branch)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.RemoveBranchProtection", resp, err)
	return resp, err
}

func (c *repositoriesClient) RequireSignaturesOnProtectedBranch(ctx context.Context, owner, repo, branch string) (*github.SignaturesProtectedBranch, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.RequireSignaturesOnProtectedBranch", func() (*github.SignaturesProtectedBranch, *github.Response, error) {
		return c.RepositoriesClient.RequireSignaturesOnProtectedBranch(ctx, owner, repo, branch)
	})
}

func (c *repositoriesClient) OptionalSignaturesOnProtectedBranch(ctx context.Context, owner, repo, branch string) (*github.Response, error) {
	resp, err := c.RepositoriesClient.OptionalSignaturesOnProtectedBranch(ctx, owner, repo, branch)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.OptionalSignaturesOnProtectedBranch", resp, err)
	return resp, err
}

func (c *repositoriesClient) ReplaceAllTopics(ctx context.Context, owner, repo string, topics []string) ([]string, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.ReplaceAllTopics", func() ([]string, *github.Response, error) {
		return c.RepositoriesClient.ReplaceAllTopics(ctx, owner, repo, topics)
	})
}

// rulesetsClient methods record "Repositories." method labels, which dashboards and
// alerts use.
func (c *rulesetsClient) GetAllRulesets(ctx context.Context, owner, repo string, opts *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.GetAllRulesets", func() ([]*rulesets.Ruleset, *github.Response, error) {
		return c.RulesetsClient.GetAllRulesets(ctx, owner, repo, opts)
	})
}

func (c *rulesetsClient) GetRuleset(ctx context.Context, owner, repo string, rulesetID int64, includesParents bool) (*rulesets.Ruleset, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.GetRuleset", func() (*rulesets.Ruleset, *github.Response, error) {
		return c.RulesetsClient.GetRuleset(ctx, owner, repo, rulesetID, includesParents)
	})
}

func (c *rulesetsClient) CreateRuleset(ctx context.Context, owner, repo string, ruleset rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.CreateRuleset", func() (*rulesets.Ruleset, *github.Response, error) {
		return c.RulesetsClient.CreateRuleset(ctx, owner, repo, ruleset)
	})
}

func (c *rulesetsClient) UpdateRuleset(ctx context.Context, owner, repo string, rulesetID int64, ruleset rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.UpdateRuleset", func() (*rulesets.Ruleset, *github.Response, error) {
		return c.RulesetsClient.UpdateRuleset(ctx, owner, repo, rulesetID, ruleset)
	})
}

func (c *rulesetsClient) DeleteRuleset(ctx context.Context, owner, repo string, rulesetID int64) (*github.Response, error) {
	resp, err := c.RulesetsClient.DeleteRuleset(ctx, owner, repo, rulesetID)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Repositories.DeleteRuleset", resp, err)
	return resp, err
}

// teamsClient methods
func (c *teamsClient) GetTeamBySlug(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.GetTeamBySlug", func() (*github.Team, *github.Response, error) {
		return c.TeamsClient.GetTeamBySlug(ctx, org, slug)
	})
}

func (c *teamsClient) CreateTeam(ctx context.Context, org string, team github.NewTeam) (*github.Team, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.CreateTeam", func() (*github.Team, *github.Response, error) {
		return c.TeamsClient.CreateTeam(ctx, org, team)
	})
}

func (c *teamsClient) EditTeamBySlug(ctx context.Context, org, slug string, team github.NewTeam, removeParent bool) (*github.Team, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.EditTeamBySlug", func() (*github.Team, *github.Response, error) {
		return c.TeamsClient.EditTeamBySlug(ctx, org, slug, team, removeParent)
	})
}

func (c *teamsClient) DeleteTeamBySlug(ctx context.Context, org, slug string) (*github.Response, error) {
	resp, err := c.TeamsClient.DeleteTeamBySlug(ctx, org, slug)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.DeleteTeamBySlug", resp, err)
	return resp, err
}

func (c *teamsClient) ListTeamMembersBySlug(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.ListTeamMembersBySlug", func() ([]*github.User, *github.Response, error) {
		return c.TeamsClient.ListTeamMembersBySlug(ctx, org, slug, opts)
	})
}

func (c *teamsClient) AddTeamMembershipBySlug(ctx context.Context, org, slug, user string, opts *github.TeamAddTeamMembershipOptions) (*github.Membership, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.AddTeamMembershipBySlug", func() (*github.Membership, *github.Response, error) {
		return c.TeamsClient.AddTeamMembershipBySlug(ctx, org, slug, user, opts)
	})
}

func (c *teamsClient) RemoveTeamMembershipBySlug(ctx context.Context, org, slug, user string) (*github.Response, error) {
	resp, err := c.TeamsClient.RemoveTeamMembershipBySlug(ctx, org, slug, user)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.RemoveTeamMembershipBySlug", resp, err)
	return resp, err
}

func (c *teamsClient) ListPendingTeamInvitationsBySlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Invitation, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.ListPendingTeamInvitationsBySlug", func() ([]*github.Invitation, *github.Response, error) {
		return c.TeamsClient.ListPendingTeamInvitationsBySlug(ctx, org, slug, opts)
	})
}

func (c *teamsClient) ListChildTeamsByParentSlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.ListChildTeamsByParentSlug", func() ([]*github.Team, *github.Response, error) {
		return c.TeamsClient.ListChildTeamsByParentSlug(ctx, org, slug, opts)
	})
}

func (c *teamsClient) IsTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string) (*github.Repository, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.IsTeamRepoBySlug", func() (*github.Repository, *github.Response, error) {
		return c.TeamsClient.IsTeamRepoBySlug(ctx, org, slug, owner, repo)
	})
}

func (c *teamsClient) AddTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string, opts *github.TeamAddTeamRepoOptions) (*github.Response, error) {
	resp, err := c.TeamsClient.AddTeamRepoBySlug(ctx, org, slug, owner, repo, opts)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.AddTeamRepoBySlug", resp, err)
	return resp, err
}

func (c *teamsClient) RemoveTeamRepoBySlug(ctx context.Context, org, slug, owner, repo string) (*github.Response, error) {
	resp, err := c.TeamsClient.RemoveTeamRepoBySlug(ctx, org, slug, owner, repo)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Teams.RemoveTeamRepoBySlug", resp, err)
	return resp, err
}

// usersClient methods
func (c *usersClient) Get(ctx context.Context, user string) (*github.User, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Users.Get", func() (*github.User, *github.Response, error) {
		return c.UsersClient.Get(ctx, user)
	})
}

// actionsClient methods
func (c *actionsClient) ListEnabledReposInOrg(ctx context.Context, owner string, opts *github.ListOptions) (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.ListEnabledReposInOrg", func() (*github.ActionsEnabledOnOrgRepos, *github.Response, error) {
		return c.ActionsClient.ListEnabledReposInOrg(ctx, owner, opts)
	})
}

func (c *actionsClient) SetEnabledReposInOrg(ctx context.Context, owner string, repositoryIDs []int64) (*github.Response, error) {
	resp, err := c.ActionsClient.SetEnabledReposInOrg(ctx, owner, repositoryIDs)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.SetEnabledReposInOrg", resp, err)
	return resp, err
}

func (c *actionsClient) GetOrgSecret(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.GetOrgSecret", func() (*github.Secret, *github.Response, error) {
		return c.ActionsClient.GetOrgSecret(ctx, org, name)
	})
}

func (c *actionsClient) ListSelectedReposForOrgSecret(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.ListSelectedReposForOrgSecret", func() (*github.SelectedReposList, *github.Response, error) {
		return c.ActionsClient.ListSelectedReposForOrgSecret(ctx, org, name, opts)
	})
}

func (c *actionsClient) SetSelectedReposForOrgSecret(ctx context.Context, org, name string, ids []int64) (*github.Response, error) {
	resp, err := c.ActionsClient.SetSelectedReposForOrgSecret(ctx, org, name, ids)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.SetSelectedReposForOrgSecret", resp, err)
	return resp, err
}

func (c *actionsClient) GetOrgVariable(ctx context.Context, org, name string) (*github.ActionsVariable, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.GetOrgVariable", func() (*github.ActionsVariable, *github.Response, error) {
		return c.ActionsClient.GetOrgVariable(ctx, org, name)
	})
}

func (c *actionsClient) CreateOrgVariable(ctx context.Context, org string, variable github.ActionsCreateOrgVariableRequest) (*github.Response, error) {
	resp, err := c.ActionsClient.CreateOrgVariable(ctx, org, variable)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.CreateOrgVariable", resp, err)
	return resp, err
}

func (c *actionsClient) UpdateOrgVariable(ctx context.Context, org, name string, variable github.ActionsUpdateOrgVariableRequest) (*github.Response, error) {
	resp, err := c.ActionsClient.UpdateOrgVariable(ctx, org, name, variable)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.UpdateOrgVariable", resp, err)
	return resp, err
}

func (c *actionsClient) DeleteOrgVariable(ctx context.Context, org, name string) (*github.Response, error) {
	resp, err := c.ActionsClient.DeleteOrgVariable(ctx, org, name)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.DeleteOrgVariable", resp, err)
	return resp, err
}

func (c *actionsClient) ListSelectedReposForOrgVariable(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.ListSelectedReposForOrgVariable", func() (*github.SelectedReposList, *github.Response, error) {
		return c.ActionsClient.ListSelectedReposForOrgVariable(ctx, org, name, opts)
	})
}

func (c *actionsClient) ListOrganizationRunnerGroups(ctx context.Context, org string, opts *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.ListOrganizationRunnerGroups", func() (*github.RunnerGroups, *github.Response, error) {
		return c.ActionsClient.ListOrganizationRunnerGroups(ctx, org, opts)
	})
}

func (c *actionsClient) CreateOrganizationRunnerGroup(ctx context.Context, org string, createReq github.CreateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.CreateOrganizationRunnerGroup", func() (*github.RunnerGroup, *github.Response, error) {
		return c.ActionsClient.CreateOrganizationRunnerGroup(ctx, org, createReq)
	})
}

func (c *actionsClient) UpdateOrganizationRunnerGroup(ctx context.Context, org string, groupID int64, updateReq github.UpdateRunnerGroupRequest) (*github.RunnerGroup, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.UpdateOrganizationRunnerGroup", func() (*github.RunnerGroup, *github.Response, error) {
		return c.ActionsClient.UpdateOrganizationRunnerGroup(ctx, org, groupID, updateReq)
	})
}

func (c *actionsClient) DeleteOrganizationRunnerGroup(ctx context.Context, org string, groupID int64) (*github.Response, error) {
	resp, err := c.ActionsClient.DeleteOrganizationRunnerGroup(ctx, org, groupID)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.DeleteOrganizationRunnerGroup", resp, err)
	return resp, err
}

func (c *actionsClient) ListRepositoryAccessRunnerGroup(ctx context.Context, org string, groupID int64, opts *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.ListRepositoryAccessRunnerGroup", func() (*github.ListRepositories, *github.Response, error) {
		return c.ActionsClient.ListRepositoryAccessRunnerGroup(ctx, org, groupID, opts)
	})
}

func (c *actionsClient) SetRepositoryAccessRunnerGroup(ctx context.Context, org string, groupID int64, ids github.SetRepoAccessRunnerGroupRequest) (*github.Response, error) {
	resp, err := c.ActionsClient.SetRepositoryAccessRunnerGroup(ctx, org, groupID, ids)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Actions.SetRepositoryAccessRunnerGroup", resp, err)
	return resp, err
}

// dependabotClient methods
func (c *dependabotClient) GetOrgSecret(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Dependabot.GetOrgSecret", func() (*github.Secret, *github.Response, error) {
		return c.DependabotClient.GetOrgSecret(ctx, org, name)
	})
}

func (c *dependabotClient) ListSelectedReposForOrgSecret(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
	return recordRateLimit(ctx, c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Dependabot.ListSelectedReposForOrgSecret", func() (*github.SelectedReposList, *github.Response, error) {
		return c.DependabotClient.ListSelectedReposForOrgSecret(ctx, org, name, opts)
	})
}

func (c *dependabotClient) SetSelectedReposForOrgSecret(ctx context.Context, org, name string, ids []int64) (*github.Response, error) {
	resp, err := c.DependabotClient.SetSelectedReposForOrgSecret(ctx, org, name, ids)
	recordResponse(c.metrics, c.org, c.appID, c.installationID, c.cacheKey, "Dependabot.SetSelectedReposForOrgSecret", resp, err)
	return resp, err
}
