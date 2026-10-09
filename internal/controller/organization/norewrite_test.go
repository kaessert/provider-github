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

package organization

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
)

const (
	harnessOrg    = "harness-org"
	harnessSecret = "HARNESS_SECRET"
	harnessDesc   = "declared description"
)

var harnessRepoIDs = map[string]int64{"repo-a": 11, "repo-b": 22, "repo-c": 33}

// githubHarness serves the organization endpoints the Organization controller
// uses, keeps their state, and records every request by method and path.
type githubHarness struct {
	mu sync.Mutex

	desc    string
	enabled []string
	// secrets holds the selected repository names of the Actions and the
	// Dependabot copy of harnessSecret, keyed "actions" and "dependabot".
	secrets map[string][]string
	// staleSecretReads makes the next n selected-repository lists of a
	// secret kind answer with an empty list, as a replica that has not seen
	// an earlier write would.
	staleSecretReads map[string]int

	requests []string
}

func newGitHubHarness() *githubHarness {
	return &githubHarness{
		desc:    harnessDesc,
		enabled: []string{"repo-a", "repo-b"},
		secrets: map[string][]string{
			"actions":    {"repo-a"},
			"dependabot": {"repo-a", "repo-c"},
		},
		staleSecretReads: map[string]int{},
	}
}

func (h *githubHarness) writes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.requests {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
}

func (h *githubHarness) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func repoList(names []string) map[string]any {
	repos := make([]map[string]any, 0, len(names))
	for _, n := range names {
		repos = append(repos, map[string]any{"id": harnessRepoIDs[n], "name": n})
	}
	return map[string]any{"total_count": len(repos), "repositories": repos}
}

func namesOf(ids []int64) []string {
	var out []string
	for name, id := range harnessRepoIDs {
		if slices.Contains(ids, id) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func (h *githubHarness) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, r.Method+" "+r.URL.Path)

	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	var body struct {
		Description string  `json:"description"`
		IDs         []int64 `json:"selected_repository_ids"`
	}
	if r.Method == http.MethodPut || r.Method == http.MethodPatch {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	base := "/orgs/" + harnessOrg
	path := r.URL.Path
	switch {
	case path == base && r.Method == http.MethodGet:
		reply(map[string]any{"login": harnessOrg, "description": h.desc})
	case path == base && r.Method == http.MethodPatch:
		h.desc = body.Description
		reply(map[string]any{"login": harnessOrg, "description": h.desc})
	case path == base+"/actions/permissions/repositories" && r.Method == http.MethodGet:
		reply(repoList(h.enabled))
	case path == base+"/actions/permissions/repositories" && r.Method == http.MethodPut:
		h.enabled = namesOf(body.IDs)
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(path, "/repos/"+harnessOrg+"/") && r.Method == http.MethodGet:
		name := strings.TrimPrefix(path, "/repos/"+harnessOrg+"/")
		id, ok := harnessRepoIDs[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		reply(map[string]any{"id": id, "name": name})
	default:
		h.serveSecret(w, r, path, body.IDs, reply)
	}
}

func (h *githubHarness) serveSecret(w http.ResponseWriter, r *http.Request, path string, ids []int64, reply func(any)) {
	for kind, prefix := range map[string]string{
		"actions":    "/orgs/" + harnessOrg + "/actions/secrets/" + harnessSecret,
		"dependabot": "/orgs/" + harnessOrg + "/dependabot/secrets/" + harnessSecret,
	} {
		switch {
		case path == prefix && r.Method == http.MethodGet:
			reply(map[string]any{"name": harnessSecret, "visibility": visibilitySelected})
			return
		case path == prefix+"/repositories" && r.Method == http.MethodGet:
			if h.staleSecretReads[kind] > 0 {
				h.staleSecretReads[kind]--
				reply(repoList(nil))
				return
			}
			reply(repoList(h.secrets[kind]))
			return
		case path == prefix+"/repositories" && r.Method == http.MethodPut:
			h.secrets[kind] = namesOf(ids)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	http.NotFound(w, r)
}

func (h *githubHarness) client(t *testing.T) *ghclient.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	gc, err := github.NewClient(github.WithURLs(github.Ptr(srv.URL+"/"), github.Ptr(srv.URL+"/")))
	if err != nil {
		t.Fatal(err)
	}
	return &ghclient.Client{Services: &ghclient.Services{
		Actions:       gc.Actions,
		Dependabot:    gc.Dependabot,
		Organizations: gc.Organizations,
		Repositories:  gc.Repositories,
	}}
}

// harnessCR declares exactly what newGitHubHarness serves.
func harnessCR() *clusterv1alpha1.Organization {
	cr := &clusterv1alpha1.Organization{}
	meta.SetExternalName(cr, harnessOrg)
	cr.Spec.ForProvider.Description = harnessDesc
	cr.Spec.ForProvider.Actions.EnabledRepos = []clusterv1alpha1.ActionEnabledRepo{{Repo: "repo-a"}, {Repo: "repo-b"}}
	cr.Spec.ForProvider.Secrets = &clusterv1alpha1.SecretConfiguration{
		ActionsSecrets: []clusterv1alpha1.OrgSecret{{
			Name:                 harnessSecret,
			RepositoryAccessList: []clusterv1alpha1.SecretSelectedRepo{{Repo: "repo-a"}},
		}},
		DependabotSecrets: []clusterv1alpha1.OrgSecret{{
			Name:                 harnessSecret,
			RepositoryAccessList: []clusterv1alpha1.SecretSelectedRepo{{Repo: "repo-c"}, {Repo: "repo-a"}},
		}},
	}
	return cr
}

// scoped runs one reconcile pass the way the managed reconciler does: Observe,
// then Update when Observe reports the resource out of date. It returns
// whether Update ran.
type scoped struct {
	name  string
	cycle func(t *testing.T, gh *ghclient.Client) (updated bool)
}

func scopes() []scoped {
	return []scoped{
		{name: "Cluster", cycle: clusterCycle()},
		{name: "Namespaced", cycle: namespacedCycle()},
	}
}

func clusterCycle() func(*testing.T, *ghclient.Client) bool {
	cr := harnessCR()
	return func(t *testing.T, gh *ghclient.Client) bool {
		t.Helper()
		e := &external{github: gh}
		return runCycle(t,
			func(ctx context.Context) (managed.ExternalObservation, error) { return e.Observe(ctx, cr) },
			func(ctx context.Context) (managed.ExternalUpdate, error) { return e.Update(ctx, cr) })
	}
}

func namespacedCycle() func(*testing.T, *ghclient.Client) bool {
	cr := harnessCR()
	ns := &namespacedv1alpha1.Organization{}
	ns.Namespace = "default"
	if err := roundTrip(cr, ns); err != nil {
		panic(err)
	}
	meta.SetExternalName(ns, harnessOrg)
	return func(t *testing.T, gh *ghclient.Client) bool {
		t.Helper()
		bridged := scopebridge.New[*namespacedv1alpha1.Organization](
			&external{github: gh},
			func() *clusterv1alpha1.Organization { return &clusterv1alpha1.Organization{} },
		)
		return runCycle(t,
			func(ctx context.Context) (managed.ExternalObservation, error) { return bridged.Observe(ctx, ns) },
			func(ctx context.Context) (managed.ExternalUpdate, error) { return bridged.Update(ctx, ns) })
	}
}

func runCycle(t *testing.T,
	observe func(context.Context) (managed.ExternalObservation, error),
	update func(context.Context) (managed.ExternalUpdate, error),
) bool {
	t.Helper()
	obs, err := observe(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.ResourceExists {
		t.Fatal("Observe: resource does not exist")
	}
	if obs.ResourceUpToDate {
		return false
	}
	if _, err := update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	return true
}

// An organization that matches GitHub is polled with reads only: no Update, and
// no PATCH or PUT to the organization, its Actions permissions, or the selected
// repositories of its secrets.
func TestPollingInSyncOrganizationWritesNothing(t *testing.T) {
	for _, s := range scopes() {
		t.Run(s.name, func(t *testing.T) {
			h := newGitHubHarness()
			gh := h.client(t)
			for i := 0; i < 25; i++ {
				if s.cycle(t, gh) {
					t.Fatalf("poll %d: Update ran for an organization that matches GitHub", i+1)
				}
			}
			if w := h.writes(); len(w) != 0 {
				t.Errorf("writes after 25 polls = %v, want none", w)
			}
			if h.count() == 0 {
				t.Error("no requests reached the harness")
			}
		})
	}
}

// A selected-repository list that is stale for one read makes Observe report
// drift. Update reads again before writing, finds nothing to change, and writes
// nothing.
func TestUpdateAfterTransientStaleReadWritesNothing(t *testing.T) {
	for _, kind := range []string{"actions", "dependabot"} {
		for _, s := range scopes() {
			t.Run(kind+s.name, func(t *testing.T) {
				h := newGitHubHarness()
				gh := h.client(t)
				h.staleSecretReads[kind] = 1
				if !s.cycle(t, gh) {
					t.Fatal("a stale list did not reach Update; the test no longer exercises the second read")
				}
				if w := h.writes(); len(w) != 0 {
					t.Errorf("writes = %v, want none: the state on GitHub already matched", w)
				}
				if s.cycle(t, gh) {
					t.Error("the poll after the stale read still reports drift")
				}
			})
		}
	}
}

// A real difference is corrected by exactly one write to the thing that
// differs, and the next poll is in sync.
func TestRealDriftIsCorrectedWithOneWrite(t *testing.T) {
	cases := map[string]struct {
		drift func(*githubHarness)
		want  []string
	}{
		"Description": {
			drift: func(h *githubHarness) { h.desc = "changed elsewhere" },
			want:  []string{"PATCH /orgs/" + harnessOrg},
		},
		"EnabledRepos": {
			drift: func(h *githubHarness) { h.enabled = []string{"repo-a"} },
			want:  []string{"PUT /orgs/" + harnessOrg + "/actions/permissions/repositories"},
		},
		"ActionsSecret": {
			drift: func(h *githubHarness) { h.secrets["actions"] = []string{"repo-b"} },
			want:  []string{"PUT /orgs/" + harnessOrg + "/actions/secrets/" + harnessSecret + "/repositories"},
		},
		"DependabotSecret": {
			drift: func(h *githubHarness) { h.secrets["dependabot"] = nil },
			want:  []string{"PUT /orgs/" + harnessOrg + "/dependabot/secrets/" + harnessSecret + "/repositories"},
		},
	}
	for name, tc := range cases {
		for _, s := range scopes() {
			t.Run(name+s.name, func(t *testing.T) {
				h := newGitHubHarness()
				gh := h.client(t)
				tc.drift(h)
				if !s.cycle(t, gh) {
					t.Fatal("a real difference did not reach Update")
				}
				if diff := cmp.Diff(tc.want, h.writes()); diff != "" {
					t.Errorf("writes (-want +got):\n%s", diff)
				}
				if s.cycle(t, gh) {
					t.Error("the poll after the correction still reports drift")
				}
				if got := len(h.writes()); got != len(tc.want) {
					t.Errorf("writes after the follow-up poll = %d, want %d", got, len(tc.want))
				}
			})
		}
	}
}

// Observe names the field that differed on the Ready condition, so a drift that
// does not correspond to a real change can be traced to its source.
func TestObserveDriftMessageNamesTheField(t *testing.T) {
	h := newGitHubHarness()
	h.secrets["actions"] = []string{"repo-b"}
	cr := harnessCR()
	obs, err := (&external{github: h.client(t)}).Observe(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ResourceUpToDate {
		t.Fatal("want drift")
	}
	msg := cr.GetCondition("Ready").Message
	if !strings.Contains(msg, "secrets.actionsSecrets") || !strings.Contains(msg, harnessSecret) {
		t.Errorf("Ready message %q does not name the drifted field", msg)
	}
}
