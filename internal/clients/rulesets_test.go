/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package clients

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-github/v90/github"

	"github.com/provider-github/provider-github/internal/clients/fake"
	"github.com/provider-github/provider-github/internal/clients/rulesets"
)

// The ruleset calls moved off go-github's Repositories service but dashboards, alerts and
// E2E scripts key on their method labels, so each must still be counted under its old
// "Repositories." name.
func TestRulesetsKeepRepositoriesMethodLabels(t *testing.T) {
	swapGlobalPool(t, newQuotaPool(time.Now))
	metrics := telemetryNewForTest(t)
	resp := &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}
	rs := &fake.MockRulesetsClient{
		MockGetAllRulesets: func(context.Context, string, string, *github.RepositoryListRulesetsOptions) ([]*rulesets.Ruleset, *github.Response, error) {
			return nil, resp, nil
		},
		MockGetRuleset: func(context.Context, string, string, int64, bool) (*rulesets.Ruleset, *github.Response, error) {
			return nil, resp, nil
		},
		MockCreateRuleset: func(context.Context, string, string, rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			return nil, resp, nil
		},
		MockUpdateRuleset: func(context.Context, string, string, int64, rulesets.Ruleset) (*rulesets.Ruleset, *github.Response, error) {
			return nil, resp, nil
		},
		MockDeleteRuleset: func(context.Context, string, string, int64) (*github.Response, error) {
			return resp, nil
		},
	}
	client := NewClient(&Services{Rulesets: rs}, metrics).WithRateLimitTracking("acme", "12345", "67890", "k")
	ctx := context.Background()

	_, _, _ = client.Rulesets.GetAllRulesets(ctx, "acme", "repo", nil)
	_, _, _ = client.Rulesets.GetRuleset(ctx, "acme", "repo", 1, true)
	_, _, _ = client.Rulesets.CreateRuleset(ctx, "acme", "repo", rulesets.Ruleset{})
	_, _, _ = client.Rulesets.UpdateRuleset(ctx, "acme", "repo", 1, rulesets.Ruleset{})
	_, _ = client.Rulesets.DeleteRuleset(ctx, "acme", "repo", 1)

	for _, method := range []string{"Repositories.GetAllRulesets", "Repositories.GetRuleset", "Repositories.CreateRuleset", "Repositories.UpdateRuleset", "Repositories.DeleteRuleset"} {
		if got := telemetryAPICallsCount(metrics, "acme", "12345", "67890", method); got != 1 {
			t.Errorf("api_calls_total{method=%s} = %v, want 1", method, got)
		}
	}
}

// rulesetsAgainst returns a tracked client whose ruleset calls reach handler, and counts
// the requests that reach it.
func rulesetsAgainst(t *testing.T, cacheKey string, handler http.HandlerFunc) (*Client, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	gh, err := github.NewClient(github.WithHTTPClient(srv.Client()), github.WithURLs(&base, nil))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	return NewClient(&Services{Rulesets: rulesets.NewService(gh)}, nil).WithRateLimitTracking("acme", "app", "install", cacheKey), &hits
}

// A ruleset that is gone answers 404: the caller must see it as Is404, and the response
// must reach the quota pool like any go-github call's, or the picker loses that
// credential's remaining quota.
func TestRulesetsNotFoundReachesPoolAndIs404(t *testing.T) {
	swapGlobalPool(t, newQuotaPool(time.Now))
	client, _ := rulesetsAgainst(t, "k404", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	})

	_, resp, err := client.Rulesets.GetRuleset(context.Background(), "acme", "repo", 42, true)

	if !Is404(err) {
		t.Errorf("Is404(%v) = false, want true", err)
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("response = %v, want the 404", resp)
	}
	if got := globalPool.snapshot("k404").Remaining; got != 4321 {
		t.Errorf("pool Remaining = %d, want 4321 from the 404's headers", got)
	}
}

// GitHub answers the ruleset list of a private repository on a plan without rulesets
// with 403; the repository controller reports that on a condition. Rate-limit 403s stay
// errors to retry, so Is403 is false for them.
func TestRulesetsIs403(t *testing.T) {
	cases := map[string]struct {
		headers map[string]string
		body    string
		want    bool
	}{
		"PlanWithoutRulesets": {
			body: `{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature.","documentation_url":"https://docs.github.com/rest/repos/rules#get-all-repository-rulesets","status":"403"}`,
			want: true,
		},
		"PrimaryRateLimit": {
			headers: map[string]string{"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "4102444800"},
			body:    `{"message":"API rate limit exceeded for installation ID 1."}`,
		},
		"SecondaryRateLimit": {
			headers: map[string]string{"Retry-After": "60"},
			body:    `{"message":"You have exceeded a secondary rate limit","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			swapGlobalPool(t, newQuotaPool(time.Now))
			client, _ := rulesetsAgainst(t, "k"+name, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, tc.body)
			})

			_, _, err := client.Rulesets.GetAllRulesets(context.Background(), "acme", "repo", nil)

			if err == nil {
				t.Fatal("GetAllRulesets error = nil, want the 403")
			}
			if got := Is403(err); got != tc.want {
				t.Errorf("Is403(%v) = %v, want %v", err, got, tc.want)
			}
			if Is404(err) {
				t.Errorf("Is404(%v) = true, want false", err)
			}
		})
	}
}

// After a secondary rate limit, go-github answers further calls itself without reaching
// GitHub. That answer carries a response, so the pool must not count it as a token-mint
// or network failure of the credential.
func TestRulesetsSecondaryRateLimitShortCircuitKeepsResponse(t *testing.T) {
	swapGlobalPool(t, newQuotaPool(time.Now))
	client, hits := rulesetsAgainst(t, "k403", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`)
	})
	ctx := context.Background()

	_, _, _ = client.Rulesets.GetAllRulesets(ctx, "acme", "repo", nil)
	_, resp, err := client.Rulesets.GetAllRulesets(ctx, "acme", "repo", nil)

	var abuse *github.AbuseRateLimitError
	if !errors.As(err, &abuse) {
		t.Fatalf("second call error = %v, want *github.AbuseRateLimitError", err)
	}
	if *hits != 1 {
		t.Errorf("server hits = %d, want 1: the second call should not reach GitHub", *hits)
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("second call response = %v, want go-github's 403", resp)
	}
	if got := globalPool.snapshot("k403").ConsecutiveFailures; got != 0 {
		t.Errorf("pool ConsecutiveFailures = %d, want 0", got)
	}
}
