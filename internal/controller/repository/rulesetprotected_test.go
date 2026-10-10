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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
)

const rulesetOnlyBranch = "release"

// branchProtectionServer answers the branch protection endpoints the way
// GitHub does: the protected-branch listing includes branches guarded only by a
// ruleset, and the classic protection endpoint answers those with 404 "Branch
// not protected".
type branchProtectionServer struct {
	mu sync.Mutex
	// classic holds the branches with a classic protection rule.
	classic map[string]bool
	// listed are the branches the protected=true listing returns.
	listed []string
	// failStatus makes GET protection for a branch answer this status instead.
	failStatus map[string]int
	// writes records every mutating request as "METHOD path".
	writes []string
}

func (s *branchProtectionServer) handler(t *testing.T) http.Handler {
	t.Helper()
	prefix := "/api/v3/repos/acme/" + repo + "/branches"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if r.Method != http.MethodGet {
			s.writes = append(s.writes, r.Method+" "+strings.TrimPrefix(path, prefix))
		}
		if path == prefix && r.Method == http.MethodGet {
			branches := make([]*github.Branch, 0, len(s.listed))
			for _, b := range s.listed {
				branches = append(branches, &github.Branch{Name: github.Ptr(b), Protected: github.Ptr(true)})
			}
			_ = json.NewEncoder(w).Encode(branches)
			return
		}
		rest := strings.TrimPrefix(path, prefix+"/")
		branch, tail, _ := strings.Cut(rest, "/")
		switch {
		case tail == "protection" && r.Method == http.MethodGet:
			if code, ok := s.failStatus[branch]; ok {
				w.WriteHeader(code)
				_, _ = fmt.Fprintf(w, `{"message":"Server said no"}`)
				return
			}
			if !s.classic[branch] {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"message":"Branch not protected","documentation_url":"https://docs.github.com/rest"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(githubProtectedBranch())
		case tail == "protection" && r.Method == http.MethodPut:
			s.classic[branch] = true
			_ = json.NewEncoder(w).Encode(githubProtectedBranch())
		case tail == "protection" && r.Method == http.MethodDelete:
			delete(s.classic, branch)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(tail, "protection/required_signatures"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func (s *branchProtectionServer) writeLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// clientAgainst returns a go-github client that talks to the test server.
func clientAgainst(t *testing.T, serverURL string) *github.Client {
	t.Helper()
	base := serverURL + "/api/v3/"
	gh, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	return gh
}

// repositoriesAgainst returns an otherwise up-to-date repositories client whose
// branch listing and branch protection calls go through go-github to srv.
func repositoriesAgainst(t *testing.T, srv *httptest.Server) *fake.MockRepositoriesClient {
	t.Helper()
	gh := clientAgainst(t, srv.URL)

	repos := upToDateRepositories(nil)
	repos.MockEdit = func(_ context.Context, _, _ string, r *github.Repository) (*github.Repository, *github.Response, error) {
		return r, fake.GenerateEmptyResponse(), nil
	}
	repos.MockReplaceAllTopics = func(_ context.Context, _, _ string, topics []string) ([]string, *github.Response, error) {
		return topics, fake.GenerateEmptyResponse(), nil
	}
	repos.MockGetPermissionLevel = func(context.Context, string, string, string) (*github.RepositoryPermissionLevel, *github.Response, error) {
		return &github.RepositoryPermissionLevel{Permission: github.Ptr("write")}, fake.GenerateEmptyResponse(), nil
	}
	repos.MockListBranches = gh.Repositories.ListBranches
	repos.MockGetBranchProtection = gh.Repositories.GetBranchProtection
	repos.MockUpdateBranchProtection = func(ctx context.Context, owner, r, branch string, preq *github.ProtectionRequest) (*github.Protection, *github.Response, error) {
		return gh.Repositories.UpdateBranchProtection(ctx, owner, r, branch, preq)
	}
	repos.MockRemoveBranchProtection = gh.Repositories.RemoveBranchProtection
	repos.MockOptionalSignaturesOnProtectedBranch = gh.Repositories.OptionalSignaturesOnProtectedBranch
	return repos
}

// scopeRunner drives one scope variant of the Repository reconciler.
type scopeRunner struct {
	observe func(ctx context.Context) (managed.ExternalObservation, error)
	update  func(ctx context.Context) error
	ready   func() xpv2.Condition
}

func scopeRunners(t *testing.T, repos *fake.MockRepositoriesClient, mods ...repositoryModifier) map[string]scopeRunner {
	t.Helper()
	newExternal := func(ns string) *external {
		// Declared actors of a branch with no classic rule are probed for write access.
		teams := &fake.MockTeamsClient{
			MockIsTeamRepoBySlug: func(context.Context, string, string, string, string) (*github.Repository, *github.Response, error) {
				return &github.Repository{Permissions: repoPermissions(map[string]bool{"push": true})}, fake.GenerateEmptyResponse(), nil
			},
		}
		gh := &ghclient.Client{Services: &ghclient.Services{Repositories: repos, Teams: teams}}
		return &external{github: gh, secretNamespace: ns}
	}

	cluster := repository(mods...)
	cluster.Spec.ForProvider.Org = "acme"
	meta.SetExternalName(cluster, repo)
	clusterExt := newExternal("")

	ns := &namespacedv1alpha1.Repository{}
	ns.Namespace = "default"
	data, err := json.Marshal(cluster.Spec.ForProvider)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ns.Spec.ForProvider); err != nil {
		t.Fatal(err)
	}
	meta.SetExternalName(ns, repo)
	bridged := scopebridge.New[*namespacedv1alpha1.Repository](
		newExternal(ns.Namespace),
		func() *clusterv1alpha1.Repository { return &clusterv1alpha1.Repository{} },
	)

	return map[string]scopeRunner{
		"Cluster": {
			observe: func(ctx context.Context) (managed.ExternalObservation, error) {
				return clusterExt.Observe(ctx, cluster)
			},
			update: func(ctx context.Context) error {
				_, err := clusterExt.Update(ctx, cluster)
				return err
			},
			ready: func() xpv2.Condition { return cluster.GetCondition(xpv2.TypeReady) },
		},
		"Namespaced": {
			observe: func(ctx context.Context) (managed.ExternalObservation, error) {
				return bridged.Observe(ctx, ns)
			},
			update: func(ctx context.Context) error {
				_, err := bridged.Update(ctx, ns)
				return err
			},
			ready: func() xpv2.Condition { return ns.GetCondition(xpv2.TypeReady) },
		},
	}
}

func newBranchProtectionServer(t *testing.T, s *branchProtectionServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)
	return srv
}

// A branch GitHub lists as protected only because a ruleset guards it has no
// classic rule: the declared rule is drift to apply, not a read error, and once
// applied the next Observe is in sync.
func TestRulesetOnlyProtectedBranchReconciles(t *testing.T) {
	for _, scope := range []string{"Cluster", "Namespaced"} {
		t.Run(scope, func(t *testing.T) {
			s := &branchProtectionServer{
				classic: map[string]bool{},
				listed:  []string{bpr1branch, rulesetOnlyBranch},
			}
			runner := scopeRunners(t, repositoriesAgainst(t, newBranchProtectionServer(t, s)))[scope]
			ctx := context.Background()

			got, err := runner.observe(ctx)
			if err != nil {
				t.Fatalf("Observe on a ruleset-only branch: unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and not up to date", got)
			}
			assertUnavailable(t, runner.ready())

			if err := runner.update(ctx); err != nil {
				t.Fatalf("Update: unexpected error: %v", err)
			}
			writes := s.writeLog()
			if len(writes) == 0 || writes[0] != "PUT /"+bpr1branch+"/protection" {
				t.Fatalf("Update writes = %v, want a PUT of the classic rule on %q first", writes, bpr1branch)
			}
			for _, w := range writes {
				if strings.Contains(w, rulesetOnlyBranch) {
					t.Errorf("Update wrote to the ruleset-only branch: %q", w)
				}
			}

			got, err = runner.observe(ctx)
			if err != nil {
				t.Fatalf("Observe after Update: unexpected error: %v", err)
			}
			if !got.ResourceExists || !got.ResourceUpToDate {
				t.Fatalf("Observe after Update = %+v, want exists and up to date", got)
			}
			if c := runner.ready(); c.Status != "True" || c.Reason != xpv2.ReasonAvailable {
				t.Errorf("Ready = %s/%s, want True/%s", c.Status, c.Reason, xpv2.ReasonAvailable)
			}
		})
	}
}

// A branch guarded by both a classic rule and a ruleset, next to a ruleset-only
// branch the spec does not mention, is in sync and costs no write.
func TestClassicAndRulesetProtectedBranchesAreInSync(t *testing.T) {
	for _, scope := range []string{"Cluster", "Namespaced"} {
		t.Run(scope, func(t *testing.T) {
			s := &branchProtectionServer{
				classic: map[string]bool{bpr1branch: true},
				listed:  []string{bpr1branch, rulesetOnlyBranch},
			}
			runner := scopeRunners(t, repositoriesAgainst(t, newBranchProtectionServer(t, s)))[scope]
			ctx := context.Background()

			got, err := runner.observe(ctx)
			if err != nil {
				t.Fatalf("Observe: unexpected error: %v", err)
			}
			if !got.ResourceExists || !got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and up to date", got)
			}
			if c := runner.ready(); c.Status != "True" || c.Reason != xpv2.ReasonAvailable {
				t.Errorf("Ready = %s/%s, want True/%s", c.Status, c.Reason, xpv2.ReasonAvailable)
			}

			if err := runner.update(ctx); err != nil {
				t.Fatalf("Update: unexpected error: %v", err)
			}
			for _, w := range s.writeLog() {
				if strings.Contains(w, "protection") && !strings.HasPrefix(w, "PUT /"+bpr1branch+"/protection") {
					t.Errorf("Update touched branch protection beyond the declared rule: %q", w)
				}
				if strings.HasPrefix(w, "DELETE") {
					t.Errorf("Update removed protection: %q", w)
				}
			}
		})
	}
}

// Only "Branch not protected" is read as no classic rule: any other failure of
// the protection endpoint is still an error.
func TestBranchProtectionReadErrorsStillFail(t *testing.T) {
	for _, scope := range []string{"Cluster", "Namespaced"} {
		for name, tc := range map[string]struct {
			status int
			branch string
		}{
			"Forbidden":                    {http.StatusForbidden, bpr1branch},
			"InternalServerError":          {http.StatusInternalServerError, bpr1branch},
			"BadGateway":                   {http.StatusBadGateway, bpr1branch},
			"ForbiddenOnRulesetOnlyBranch": {http.StatusForbidden, rulesetOnlyBranch},
		} {
			t.Run(scope+"/"+name, func(t *testing.T) {
				s := &branchProtectionServer{
					classic:    map[string]bool{bpr1branch: true},
					listed:     []string{bpr1branch, rulesetOnlyBranch},
					failStatus: map[string]int{tc.branch: tc.status},
				}
				runner := scopeRunners(t, repositoriesAgainst(t, newBranchProtectionServer(t, s)))[scope]
				ctx := context.Background()

				if _, err := runner.observe(ctx); err == nil {
					t.Errorf("Observe with HTTP %d from GetBranchProtection: want an error, got nil", tc.status)
				}
				if err := runner.update(ctx); err == nil {
					t.Errorf("Update with HTTP %d from GetBranchProtection: want an error, got nil", tc.status)
				}
				if w := s.writeLog(); len(w) != 0 {
					t.Errorf("writes after a failed read = %v, want none", w)
				}
			})
		}
	}
}

// A 404 whose message is not "Branch not protected" (for example a missing
// repository) is not a ruleset-only branch and is still an error.
func TestBranchProtectionOtherNotFoundStillFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	t.Cleanup(srv.Close)
	gh := clientAgainst(t, srv.URL)

	_, err := getBPRWithConfig(context.Background(), clientFor(&fake.MockRepositoriesClient{
		MockGetBranchProtection: gh.Repositories.GetBranchProtection,
	}), "acme", repo, []*github.Branch{{Name: github.Ptr(bpr1branch)}})
	if err == nil {
		t.Fatal("getBPRWithConfig on a plain 404: want an error, got nil")
	}
}
