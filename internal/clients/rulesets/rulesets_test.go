/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package rulesets

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
)

// request is what the test server saw of one call.
type request struct {
	method string
	path   string
	query  string
	body   string
}

// serve returns a Service whose requests reach a test server answering with status and
// body (and the given headers), and the request the server saw.
func serve(t *testing.T, status int, body string, header http.Header) (*Service, *request) {
	t.Helper()
	got := &request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		*got = request{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, body: string(b)}
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	client, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithURLs(&base, nil))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	return NewService(client), got
}

// jsonEqual compares two JSON documents by value, so key order and spacing do not matter.
func jsonEqual(t *testing.T, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("unmarshal want %s: %v", want, err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("unmarshal got %s: %v", got, err)
	}
	if diff := cmp.Diff(w, g); diff != "" {
		t.Errorf("JSON: -want, +got:\n%s", diff)
	}
}

// The list call sends includes_parents and the page, and returns the next page from
// the Link header, which the controller pages on.
func TestGetAllRulesetsRequest(t *testing.T) {
	header := http.Header{"Link": {`<https://api.github.com/repositories/1/rulesets?page=3>; rel="next"`}}
	s, got := serve(t, http.StatusOK, `[{"id":7,"name":"a","target":"branch","source_type":"Organization","source":"acme","enforcement":"active","node_id":"x"}]`, header)

	opts := &github.RepositoryListRulesetsOptions{IncludesParents: github.Ptr(false), ListOptions: github.ListOptions{Page: 2, PerPage: 100}}
	list, resp, err := s.GetAllRulesets(context.Background(), "acme", "repo", opts)
	if err != nil {
		t.Fatalf("GetAllRulesets: %v", err)
	}

	want := request{method: http.MethodGet, path: "/repos/acme/repo/rulesets", query: "includes_parents=false&page=2&per_page=100"}
	if diff := cmp.Diff(want, *got, cmp.AllowUnexported(request{})); diff != "" {
		t.Errorf("request: -want, +got:\n%s", diff)
	}
	wantList := []*Ruleset{{ID: github.Ptr(int64(7)), Name: "a", Target: github.Ptr("branch"), SourceType: github.Ptr("Organization"), Source: "acme", Enforcement: "active"}}
	if diff := cmp.Diff(wantList, list); diff != "" {
		t.Errorf("rulesets: -want, +got:\n%s", diff)
	}
	if resp.NextPage != 3 {
		t.Errorf("NextPage = %d, want 3", resp.NextPage)
	}
}

// liveRuleset is a ruleset as GitHub returns it, with an unmodelled rule type, a
// reviewer id as a string and an OrganizationAdmin with a null actor_id.
const liveRuleset = `{"id":1001,"name":"main-protection","target":"branch","source_type":"Repository","source":"acme/repo","enforcement":"disabled",
"conditions":{"ref_name":{"exclude":[],"include":["refs/heads/zeta","~DEFAULT_BRANCH"]}},
"rules":[{"type":"deletion"},{"type":"creation"},
{"type":"pull_request","parameters":{"required_approving_review_count":2,"dismiss_stale_reviews_on_push":true,"required_reviewers":[{"file_patterns":["*"],"minimum_approvals":1,"reviewer":{"id":"2002","type":"Team"}}],"require_code_owner_review":false,"dismissal_restriction":{"enabled":false,"allowed_actors":[]},"require_last_push_approval":false,"required_review_thread_resolution":false,"require_extra_approval_for_unattributed_changes":true,"ignore_approvals_from_contributors":false,"allowed_merge_methods":["squash"]}},
{"type":"workflows","parameters":{"workflows":[{"path":".github/workflows/ci.yml","repository_id":3003}]}}],
"node_id":"RRS_x","created_at":"2026-01-01T00:00:00.000+00:00","updated_at":"2026-01-01T00:00:00.000+00:00",
"bypass_actors":[{"actor_id":null,"actor_type":"OrganizationAdmin","bypass_mode":"always"},{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"pull_request"}],
"current_user_can_bypass":"always","_links":{"self":{"href":"https://api.github.com/repos/acme/repo/rulesets/1001"}}}`

// A read keeps every rule type GitHub returns, because the guard needs the unmodelled ones.
func TestGetRulesetKeepsWhatIsNotModelled(t *testing.T) {
	s, got := serve(t, http.StatusOK, liveRuleset, nil)

	rs, _, err := s.GetRuleset(context.Background(), "acme", "repo", 42, true)
	if err != nil {
		t.Fatalf("GetRuleset: %v", err)
	}

	want := request{method: http.MethodGet, path: "/repos/acme/repo/rulesets/42", query: "includes_parents=true"}
	if diff := cmp.Diff(want, *got, cmp.AllowUnexported(request{})); diff != "" {
		t.Errorf("request: -want, +got:\n%s", diff)
	}
	types := make([]string, 0, len(rs.Rules))
	for _, r := range rs.Rules {
		types = append(types, r.Type)
	}
	if diff := cmp.Diff([]string{"deletion", "creation", "pull_request", "workflows"}, types); diff != "" {
		t.Errorf("rule types: -want, +got:\n%s", diff)
	}
	jsonEqual(t, `{"workflows":[{"path":".github/workflows/ci.yml","repository_id":3003}]}`, string(rs.Rules[3].Parameters))
	wantActors := []*BypassActor{
		{ActorType: github.Ptr("OrganizationAdmin"), BypassMode: github.Ptr("always")},
		{ActorID: github.Ptr(int64(5)), ActorType: github.Ptr("RepositoryRole"), BypassMode: github.Ptr("pull_request")},
	}
	if diff := cmp.Diff(wantActors, rs.BypassActors); diff != "" {
		t.Errorf("bypass actors: -want, +got:\n%s", diff)
	}

	modelled, err := Decode(rs.Rules)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantModelled := &ModelledRules{
		Deletion: &EmptyRuleParameters{},
		Creation: &EmptyRuleParameters{},
		PullRequest: &PullRequestRuleParameters{
			AllowedMergeMethods:          []string{"squash"},
			DismissStaleReviewsOnPush:    true,
			DismissalRestriction:         &DismissalRestriction{AllowedActors: []*Actor{}},
			RequiredApprovingReviewCount: 2,
			RequiredReviewers:            []*RequiredReviewer{{FilePatterns: []string{"*"}, MinimumApprovals: 1, Reviewer: Actor{ID: "2002", Type: "Team"}}},

			RequireExtraApprovalForUnattributedChanges: github.Ptr(true),
		},
	}
	if diff := cmp.Diff(wantModelled, modelled); diff != "" {
		t.Errorf("Decode: -want, +got:\n%s", diff)
	}
}

// An actor id decodes from a JSON string or a number and is sent as a number.
func TestIDDecodesStringOrNumber(t *testing.T) {
	for _, in := range []string{`"2002"`, `2002`} {
		var a Actor
		if err := json.Unmarshal([]byte(`{"id":`+in+`,"type":"Team"}`), &a); err != nil {
			t.Fatalf("unmarshal id %s: %v", in, err)
		}
		if a.ID != "2002" {
			t.Errorf("id %s decoded as %s, want 2002", in, a.ID)
		}
		out, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(out) != `{"id":2002,"type":"Team"}` {
			t.Errorf("id %s sent as %s, want a number", in, out)
		}
	}
	var a Actor
	if err := json.Unmarshal([]byte(`{"id":"team-x"}`), &a); err == nil {
		t.Errorf("unmarshal of a non-numeric id string succeeded, want an error")
	}
}

// Encode sends a parameterless rule as {"type":...} and parameters in field order.
// No rules encode as [], so an update clears the rules.
func TestEncode(t *testing.T) {
	m := &ModelledRules{
		Creation:             &EmptyRuleParameters{},
		CommitMessagePattern: &PatternRuleParameters{Name: github.Ptr("conventional"), Operator: "starts_with", Pattern: "feat"},
	}
	rules, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `[{"type":"creation"},{"type":"commit_message_pattern","parameters":{"name":"conventional","operator":"starts_with","pattern":"feat"}}]`
	if string(got) != want {
		t.Errorf("Encode JSON:\nwant %s\n got %s", want, got)
	}

	empty, err := (&ModelledRules{}).Encode()
	if err != nil {
		t.Fatalf("Encode empty: %v", err)
	}
	if got, _ := json.Marshal(empty); string(got) != `[]` {
		t.Errorf("Encode of no rules = %s, want []", got)
	}
}

// UnmanagedParameters names the parameters an update would reset because the
// provider has no field for them. Unmodelled rule types are the guard's to report.
func TestUnmanagedParameters(t *testing.T) {
	cases := map[string]struct {
		rule *Rule
		want []string
	}{
		"ModelledRuleWithExtraKeys": {
			rule: &Rule{Type: "merge_queue", Parameters: json.RawMessage(`{"merge_method":"SQUASH","zeta":1,"actor_controlled_merging":true}`)},
			want: []string{"actor_controlled_merging", "zeta"},
		},
		"ModelledRuleWithKnownKeysOnly": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`{"required_approving_review_count":1,"dismissal_restriction":{"enabled":false},"require_extra_approval_for_unattributed_changes":true}`)},
		},
		"ParameterlessRuleWithParameters": {
			rule: &Rule{Type: "creation", Parameters: json.RawMessage(`{"new_flag":true}`)},
			want: []string{"new_flag"},
		},
		"NullParameters": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`null`)},
		},
		"NoParameters": {
			rule: &Rule{Type: "creation"},
		},
		"UnmodelledRuleType": {
			rule: &Rule{Type: "workflows", Parameters: json.RawMessage(`{"workflows":[]}`)},
		},
		"UnknownKeyInDismissalRestriction": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`{"dismissal_restriction":{"allowed_actors":[],"enabled":true,"foo":1}}`)},
			want: []string{"dismissal_restriction.foo"},
		},
		"UnknownKeyInOneRequiredReviewer": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`{"required_reviewers":[
				{"file_patterns":["*.go"],"minimum_approvals":1,"reviewer":{"id":1,"type":"Team"}},
				{"file_patterns":["*.md"],"minimum_approvals":1,"reviewer":{"id":2,"type":"Team"},"bar":true}
			]}`)},
			want: []string{"required_reviewers[].bar"},
		},
		"UnknownKeyInRequiredReviewerActor": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`{"required_reviewers":[{"reviewer":{"id":1,"type":"Team","slug":"core"}}]}`)},
			want: []string{"required_reviewers[].reviewer.slug"},
		},
		"SameUnknownKeyInEveryStatusCheck": {
			rule: &Rule{Type: "required_status_checks", Parameters: json.RawMessage(`{"required_status_checks":[
				{"context":"build","baz":"x"},
				{"context":"test","integration_id":1,"baz":"y"}
			]}`)},
			want: []string{"required_status_checks[].baz"},
		},
		"UnknownKeyInCodeScanningTool": {
			rule: &Rule{Type: "code_scanning", Parameters: json.RawMessage(`{"code_scanning_tools":[{"alerts_threshold":"errors","security_alerts_threshold":"high_or_higher","tool":"CodeQL","qux":[]}]}`)},
			want: []string{"code_scanning_tools[].qux"},
		},
		"TopLevelAndNestedUnknownKeys": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`{"zeta":1,"required_reviewers":[{"bar":true}],"dismissal_restriction":{"foo":1},"alpha":2}`)},
			want: []string{"alpha", "dismissal_restriction.foo", "required_reviewers[].bar", "zeta"},
		},
		"NullNestedObject": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`{"dismissal_restriction":null,"required_reviewers":null}`)},
		},
		// GitHub returns empty and null values for keys the provider models. They are
		// managed keys whatever their value, so a check that re-encodes the decoded
		// struct, which drops omitempty fields holding null, must not report them.
		"KnownNestedKeysWithEmptyValuesAreManaged": {
			rule: &Rule{Type: "pull_request", Parameters: json.RawMessage(`{
				"dismissal_restriction":{"allowed_actors":[],"enabled":false},
				"require_extra_approval_for_unattributed_changes":null,
				"required_reviewers":[{"file_patterns":[],"minimum_approvals":0,"reviewer":{"id":0,"type":""}}]
			}`)},
		},
		"KnownStatusCheckKeysWithEmptyValuesAreManaged": {
			rule: &Rule{Type: "required_status_checks", Parameters: json.RawMessage(`{"required_status_checks":[{"context":"","integration_id":null}]}`)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, UnmanagedParameters(tc.rule)); diff != "" {
				t.Errorf("UnmanagedParameters: -want, +got:\n%s", diff)
			}
		})
	}
}

// ruleset is a request body exercising every top-level key and both rule shapes.
func ruleset() Ruleset {
	return Ruleset{
		Name:         "main",
		Target:       github.Ptr("branch"),
		Enforcement:  "active",
		BypassActors: []*BypassActor{{ActorID: github.Ptr(int64(5)), ActorType: github.Ptr("RepositoryRole"), BypassMode: github.Ptr("always")}},
		Conditions:   &Conditions{RefName: &RefName{Include: []string{"~DEFAULT_BRANCH"}, Exclude: []string{}}},
		Rules: []*Rule{
			{Type: "creation"},
			{Type: "pull_request", Parameters: json.RawMessage(`{"required_approving_review_count":1}`)},
		},
	}
}

// wantBody is ruleset() as JSON. "source" is read-only, so it is left out.
const wantBody = `{"name":"main","target":"branch","enforcement":"active",
"bypass_actors":[{"actor_id":5,"actor_type":"RepositoryRole","bypass_mode":"always"}],
"conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},
"rules":[{"type":"creation"},{"type":"pull_request","parameters":{"required_approving_review_count":1}}]}`

func TestCreateRulesetRequest(t *testing.T) {
	s, got := serve(t, http.StatusCreated, `{"id":9,"name":"main","enforcement":"active"}`, nil)

	created, _, err := s.CreateRuleset(context.Background(), "acme", "repo", ruleset())
	if err != nil {
		t.Fatalf("CreateRuleset: %v", err)
	}
	if got.method != http.MethodPost || got.path != "/repos/acme/repo/rulesets" || got.query != "" {
		t.Errorf("request = %s %s?%s, want POST /repos/acme/repo/rulesets", got.method, got.path, got.query)
	}
	jsonEqual(t, wantBody, got.body)
	if created.ID == nil || *created.ID != 9 {
		t.Errorf("created ID = %v, want 9", created.ID)
	}
}

func TestUpdateRulesetRequest(t *testing.T) {
	s, got := serve(t, http.StatusOK, `{"id":42,"name":"main","enforcement":"active"}`, nil)

	if _, _, err := s.UpdateRuleset(context.Background(), "acme", "repo", 42, ruleset()); err != nil {
		t.Fatalf("UpdateRuleset: %v", err)
	}
	if got.method != http.MethodPut || got.path != "/repos/acme/repo/rulesets/42" || got.query != "" {
		t.Errorf("request = %s %s?%s, want PUT /repos/acme/repo/rulesets/42", got.method, got.path, got.query)
	}
	jsonEqual(t, wantBody, got.body)
}

// Empty bypass actor and rules lists are sent as [], which clears them on update.
func TestRulesetEmptyListsAreSent(t *testing.T) {
	s, got := serve(t, http.StatusOK, `{}`, nil)
	rs := Ruleset{Name: "main", Enforcement: "active", BypassActors: []*BypassActor{}, Rules: []*Rule{}}

	if _, _, err := s.UpdateRuleset(context.Background(), "acme", "repo", 42, rs); err != nil {
		t.Fatalf("UpdateRuleset: %v", err)
	}
	jsonEqual(t, `{"name":"main","enforcement":"active","bypass_actors":[],"rules":[]}`, got.body)
}

func TestDeleteRulesetRequest(t *testing.T) {
	s, got := serve(t, http.StatusNoContent, "", nil)

	if _, err := s.DeleteRuleset(context.Background(), "acme", "repo", 42); err != nil {
		t.Fatalf("DeleteRuleset: %v", err)
	}
	want := request{method: http.MethodDelete, path: "/repos/acme/repo/rulesets/42"}
	if diff := cmp.Diff(want, *got, cmp.AllowUnexported(request{})); diff != "" {
		t.Errorf("request: -want, +got:\n%s", diff)
	}
}

// GetRuleset sends includes_parents as asked.
func TestGetRulesetIncludesParents(t *testing.T) {
	for _, include := range []bool{true, false} {
		s, got := serve(t, http.StatusOK, `{"id":42,"name":"main","enforcement":"active"}`, nil)
		if _, _, err := s.GetRuleset(context.Background(), "acme", "repo", 42, include); err != nil {
			t.Fatalf("GetRuleset: %v", err)
		}
		if want := "includes_parents=" + strconv.FormatBool(include); got.query != want {
			t.Errorf("GetRuleset(includesParents=%v) query = %q, want %q", include, got.query, want)
		}
	}
}
