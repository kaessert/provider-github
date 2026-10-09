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
	"encoding/json"
	"slices"
	"sort"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
)

const errMirror = "cannot record the observed state of the repository"

// clone returns a pointer to a copy of *p, or nil.
func clone[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// mirrorRepository records in status.atProvider the settings GitHub reports in
// the repository response Observe already fetched. org is the organization the
// repository was read under, used when the response names no owner.
func mirrorRepository(ap *v1alpha1.RepositoryObservation, repo *github.Repository, org string) {
	ap.Description = repo.GetDescription()
	ap.Org = repo.GetOwner().GetLogin()
	if ap.Org == "" {
		ap.Org = org
	}
	ap.Archived = clone(repo.Archived)
	ap.Private = clone(repo.Private)
	ap.IsTemplate = clone(repo.IsTemplate)
	ap.Topics = slices.Sorted(slices.Values(repo.Topics))
	ap.DefaultBranch = clone(repo.DefaultBranch)
	ap.AllowMergeCommit = clone(repo.AllowMergeCommit)
	ap.AllowSquashMerge = clone(repo.AllowSquashMerge)
	ap.AllowRebaseMerge = clone(repo.AllowRebaseMerge)
	ap.AllowAutoMerge = clone(repo.AllowAutoMerge)
	ap.AllowUpdateBranch = clone(repo.AllowUpdateBranch)
	ap.DeleteBranchOnMerge = clone(repo.DeleteBranchOnMerge)
	ap.HasIssues = clone(repo.HasIssues)
	ap.HasProjects = clone(repo.HasProjects)
	ap.HasWiki = clone(repo.HasWiki)
	ap.HasDiscussions = clone(repo.HasDiscussions)
	ap.MergeCommitTitle = clone(repo.MergeCommitTitle)
	ap.MergeCommitMessage = clone(repo.MergeCommitMessage)
	ap.SquashMergeCommitTitle = clone(repo.SquashMergeCommitTitle)
	ap.SquashMergeCommitMessage = clone(repo.SquashMergeCommitMessage)

	ap.CreateFromTemplate = nil
	if t := repo.TemplateRepository; t != nil {
		ap.CreateFromTemplate = &v1alpha1.TemplateRepoObservation{Owner: t.GetOwner().GetLogin(), Repo: t.GetName()}
	}
	ap.CreateFork = nil
	if p := repo.Parent; p != nil && repo.GetFork() {
		ap.CreateFork = &v1alpha1.RepoForkObservation{Owner: p.GetOwner().GetLogin(), Repo: p.GetName()}
	}
}

// mirrorUsers lists collaborators, given as login to role, sorted by login.
func mirrorUsers(users map[string]string) []v1alpha1.RepositoryUserObservation {
	out := make([]v1alpha1.RepositoryUserObservation, 0, len(users))
	for user, role := range users {
		out = append(out, v1alpha1.RepositoryUserObservation{User: user, Role: role})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// mirrorTeams lists teams, given as slug to role, sorted by slug.
func mirrorTeams(teams map[string]string) []v1alpha1.RepositoryTeamObservation {
	out := make([]v1alpha1.RepositoryTeamObservation, 0, len(teams))
	for team, role := range teams {
		out = append(out, v1alpha1.RepositoryTeamObservation{Team: team, Role: role})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Team < out[j].Team })
	return out
}

// mirrorWebhooks, mirrorBranchProtection and mirrorRulesets list what GitHub
// reports, in key order, in the shape the observation uses. The observation
// types carry the JSON names of their spec twins and leave out what GitHub
// never returns (the webhook secret), so the conversion goes through JSON.
func mirrorWebhooks(hooks map[string]v1alpha1.RepositoryWebhook) ([]v1alpha1.RepositoryWebhookObservation, error) {
	return mirrorSorted[v1alpha1.RepositoryWebhook, v1alpha1.RepositoryWebhookObservation](hooks)
}

func mirrorBranchProtection(rules map[string]v1alpha1.BranchProtectionRule) ([]v1alpha1.BranchProtectionRuleObservation, error) {
	return mirrorSorted[v1alpha1.BranchProtectionRule, v1alpha1.BranchProtectionRuleObservation](rules)
}

func mirrorRulesets(rulesets map[string]v1alpha1.RepositoryRuleset) ([]v1alpha1.RepositoryRulesetObservation, error) {
	return mirrorSorted[v1alpha1.RepositoryRuleset, v1alpha1.RepositoryRulesetObservation](rulesets)
}

func mirrorSorted[S, O any](in map[string]S) ([]O, error) {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]O, 0, len(keys))
	for _, k := range keys {
		raw, err := json.Marshal(in[k])
		if err != nil {
			return nil, errors.Wrap(err, errMirror)
		}
		var o O
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, errors.Wrap(err, errMirror)
		}
		out = append(out, o)
	}
	return out, nil
}
