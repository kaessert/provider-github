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

// Package secretaccess holds the helpers shared by the ActionsSecretAccess
// and DependabotSecretAccess controllers.
package secretaccess

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
)

// VisibilitySelected is the visibility under which a secret has a repository list.
const VisibilitySelected = "selected"

const (
	reasonSecretNotFound     xpv2.ConditionReason = "SecretNotFound"
	reasonVisibilityMismatch xpv2.ConditionReason = "VisibilityMismatch"
)

// RepoLister lists the repositories that can use an organization secret.
type RepoLister interface {
	ListSelectedReposForOrgSecret(ctx context.Context, org, name string, opts *github.ListOptions) (*github.SelectedReposList, *github.Response, error)
}

// SecretNotFound is the Ready condition for a secret missing on GitHub.
func SecretNotFound(name string) xpv2.Condition {
	return notReady(reasonSecretNotFound,
		fmt.Sprintf("secret %s not found on GitHub; create it there first (this resource only manages repository access)", name))
}

// VisibilityMismatch is the Ready condition for a visibility the provider cannot change.
func VisibilityMismatch(ghVisibility, specVisibility string) xpv2.Condition {
	return notReady(reasonVisibilityMismatch,
		fmt.Sprintf("visibility is %q on GitHub but %q in spec; change it on GitHub (the API cannot change visibility without the secret value)", ghVisibility, specVisibility))
}

// SecretNotFoundError is the Create error: the secret must exist on GitHub first.
func SecretNotFoundError(name string) error {
	return errors.Errorf("secret %s not found on GitHub; create it there first", name)
}

func notReady(reason xpv2.ConditionReason, message string) xpv2.Condition {
	return xpv2.Condition{
		Type:               xpv2.TypeReady,
		Status:             corev1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}

// RepoNames returns the distinct repository names declared in spec, in spec
// order, keeping the first spelling of names that differ only in case. The
// declared list is a set: a duplicate must not read as drift or be sent to
// GitHub twice, or the provider rewrites the list every poll.
func RepoNames(refs []v1alpha1.SecretSelectedRepo) []string {
	names := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		k := strings.ToLower(r.Repo)
		if seen[k] {
			continue
		}
		seen[k] = true
		names = append(names, r.Repo)
	}
	return names
}

// ListRepoNames returns the names of all repositories that can use the secret
// on GitHub, spelled as GitHub reports them.
func ListRepoNames(ctx context.Context, l RepoLister, org, name string) ([]string, error) {
	repos, err := listRepos(ctx, l, org, name)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.GetName())
	}
	return names, nil
}

// ListRepoIDs returns the ID of every repository that can use the secret on
// GitHub, keyed by lowercased name: GitHub repository names are
// case-insensitive, so callers must look names up lowercased.
func ListRepoIDs(ctx context.Context, l RepoLister, org, name string) (map[string]int64, error) {
	repos, err := listRepos(ctx, l, org, name)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]int64, len(repos))
	for _, r := range repos {
		ids[strings.ToLower(r.GetName())] = r.GetID()
	}
	return ids, nil
}

// listRepos pages through the repositories that can use the secret on GitHub.
func listRepos(ctx context.Context, l RepoLister, org, name string) ([]*github.Repository, error) {
	opts := &github.ListOptions{PerPage: 100}
	var repos []*github.Repository
	for {
		list, resp, err := l.ListSelectedReposForOrgSecret(ctx, org, name, opts)
		if err != nil {
			return nil, err
		}
		repos = append(repos, list.Repositories...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return repos, nil
}

// RenamedRepoError returns an error if a declared name absent from the list
// (added) resolved to an ID the list (known, from ListRepoIDs) already holds
// under another name. That repository was renamed on GitHub and the spec still
// uses the old name: writing would change nothing and repeat every poll, so
// fail loud instead. addedIDs are the IDs of added, in the same order.
func RenamedRepoError(known map[string]int64, added []string, addedIDs []int64) error {
	byID := make(map[int64]string, len(known))
	for n, id := range known {
		byID[id] = n
	}
	for i, n := range added {
		if current, ok := byID[addedIDs[i]]; ok {
			return errors.Errorf("repository %s is named %s on GitHub; update selectedRepositories to the new name", n, current)
		}
	}
	return nil
}

// SameRepos reports whether the spec and GitHub list the same repositories, in
// any order and ignoring case. GitHub repository names are case-insensitive, so
// a case difference must not read as drift or Update rewrites the list every poll.
func SameRepos(spec []v1alpha1.SecretSelectedRepo, ghNames []string) bool {
	specNames := RepoNames(spec)
	for i, n := range specNames {
		specNames[i] = strings.ToLower(n)
	}
	gh := make([]string, 0, len(ghNames))
	for _, n := range ghNames {
		gh = append(gh, strings.ToLower(n))
	}
	slices.Sort(specNames)
	slices.Sort(gh)
	return slices.Equal(specNames, gh)
}

// RepoObservations returns names as observed repositories, sorted.
func RepoObservations(names []string) []v1alpha1.SecretSelectedRepoObservation {
	out := make([]v1alpha1.SecretSelectedRepoObservation, 0, len(names))
	for _, n := range names {
		out = append(out, v1alpha1.SecretSelectedRepoObservation{Repo: n})
	}
	slices.SortFunc(out, func(a, b v1alpha1.SecretSelectedRepoObservation) int { return strings.Compare(a.Repo, b.Repo) })
	return out
}
