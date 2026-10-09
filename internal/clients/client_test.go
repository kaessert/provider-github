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
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/telemetry"
)

// telemetryNewForTest builds a RateLimitMetrics instance without
// registering it with the default Prometheus registry, so tests can run
// repeatedly without "duplicate registration" panics.
func telemetryNewForTest(t *testing.T) *telemetry.RateLimitMetrics {
	t.Helper()
	return telemetry.NewForTest()
}

// telemetryAPICallsCount reads api_calls_total via the test-only accessor.
func telemetryAPICallsCount(m *telemetry.RateLimitMetrics, org, appID, installID, method string) float64 {
	return m.APICallsCountForTest(org, appID, installID, method)
}

// swapGlobalPool substitutes the process-wide quota pool for the duration
// of a test. Use for tests that need a clock-controlled pool but go
// through call paths that use globalPool directly.
func swapGlobalPool(t *testing.T, p *quotaPool) {
	t.Helper()
	orig := globalPool
	globalPool = p
	t.Cleanup(func() { globalPool = orig })
}

// A 429 response routed through any rate-limited method updates the pool
// under the supplied cacheKey, with CooldownUntil set to the response's
// Reset time — the wire that lets the picker skip the credential on
// subsequent Connects.
func TestClientRecordsToPoolOnTooManyRequests(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(15 * time.Minute)

	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	// Build an Organizations service that returns a 429.
	orgs := &fake.MockOrganizationsClient{
		MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
			return nil, &github.Response{
				Response: &http.Response{StatusCode: http.StatusTooManyRequests},
				Rate: github.Rate{
					Limit:     5000,
					Remaining: 0,
					Reset:     github.Timestamp{Time: resetAt},
				},
			}, &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusTooManyRequests}}
		},
	}

	c := &Services{Organizations: orgs}
	client := NewClient(c, nil).WithRateLimitTracking("acme", "app", "install", "app-cache-key")

	_, _, _ = client.Organizations.Get(context.Background(), "acme")

	q := globalPool.snapshot("app-cache-key")
	if !q.CooldownUntil.Equal(resetAt) {
		t.Errorf("CooldownUntil = %v, want %v", q.CooldownUntil, resetAt)
	}
}

func TestClientRecordsToPoolOnSuccess(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	orgs := &fake.MockOrganizationsClient{
		MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
			return &github.Organization{}, &github.Response{
				Response: &http.Response{StatusCode: 200},
				Rate: github.Rate{
					Limit:     5000,
					Remaining: 4321,
					Reset:     github.Timestamp{Time: now.Add(time.Hour)},
				},
			}, nil
		},
	}

	c := &Services{Organizations: orgs}
	client := NewClient(c, nil).WithRateLimitTracking("acme", "app", "install", "app-cache-key")

	_, _, _ = client.Organizations.Get(context.Background(), "acme")

	q := globalPool.snapshot("app-cache-key")
	if q.Remaining != 4321 {
		t.Errorf("Remaining = %d, want 4321", q.Remaining)
	}
	if !q.CooldownUntil.IsZero() {
		t.Errorf("CooldownUntil = %v, want zero on success", q.CooldownUntil)
	}
}

// Pool wiring must work even when metrics is nil (e.g. in controller
// unit tests). Telemetry being optional should not skip pool updates.
func TestClientNilMetricsIsSafe(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	orgs := &fake.MockOrganizationsClient{
		MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
			return &github.Organization{}, &github.Response{
				Response: &http.Response{StatusCode: 200},
				Rate:     github.Rate{Limit: 5000, Remaining: 100, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
			}, nil
		},
	}

	c := &Services{Organizations: orgs}
	client := NewClient(c, nil).WithRateLimitTracking("acme", "app", "install", "k")

	_, _, _ = client.Organizations.Get(context.Background(), "acme")

	if globalPool.snapshot("k").Remaining != 100 {
		t.Errorf("expected pool to record despite nil metrics")
	}
}

// Exercises a non-Organizations service path (Repositories) to confirm
// cacheKey propagates through every wrapper, not just the Orgs one. The
// empty-key check guards against the field being silently dropped.
func TestClientRecordsToPoolRepositories(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	repos := &fake.MockRepositoriesClient{
		MockGet: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
			return &github.Repository{}, &github.Response{
				Response: &http.Response{StatusCode: 200},
				Rate:     github.Rate{Limit: 5000, Remaining: 1234, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
			}, nil
		},
	}

	c := &Services{Repositories: repos}
	client := NewClient(c, nil).WithRateLimitTracking("acme", "app", "install", "app-repos-key")

	_, _, _ = client.Repositories.Get(context.Background(), "acme", "my-repo")

	if got := globalPool.snapshot("app-repos-key").Remaining; got != 1234 {
		t.Errorf("Repositories.Get did not propagate cacheKey: pool.Remaining = %d, want 1234", got)
	}
	if got := globalPool.snapshot("").Remaining; got != 0 {
		t.Errorf("pool was written under empty cacheKey (suggesting cacheKey field was not threaded): got %d, want 0", got)
	}
}

// Every wrapped method call increments github_api_calls_total under
// the supplied (org, app_id, app_installation_id, method) labels —
// regardless of HTTP outcome. Verifies the method-name plumbing from
// each wrapper through recordResponse to telemetry.
func TestClientIncrementsAPICallsCounter(t *testing.T) {
	swapGlobalPool(t, newQuotaPool(time.Now))

	metrics := telemetryNewForTest(t)
	calls := map[string]int{}
	orgs := &fake.MockOrganizationsClient{
		MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
			calls["Get"]++
			return &github.Organization{}, &github.Response{
				Response: &http.Response{StatusCode: 200},
				Rate:     github.Rate{Limit: 5000, Remaining: 4900},
			}, nil
		},
	}
	c := &Services{Organizations: orgs}
	client := NewClient(c, metrics).WithRateLimitTracking("acme", "12345", "67890", "k")

	_, _, _ = client.Organizations.Get(context.Background(), "acme")
	_, _, _ = client.Organizations.Get(context.Background(), "acme")

	if calls["Get"] != 2 {
		t.Fatalf("setup: underlying Get called %d times, want 2", calls["Get"])
	}
	if got := telemetryAPICallsCount(metrics, "acme", "12345", "67890", "Organizations.Get"); got != 2 {
		t.Errorf("api_calls_total{method=Organizations.Get} = %v, want 2", got)
	}
}

// A wrapped call returning (nil, nil, err) — the ghinstallation token-mint
// failure shape — must mark the credential unhealthy in the pool so the
// picker steers away on the next pick.
func TestClientAuthFailureRecordsUnhealthy(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	orgs := &fake.MockOrganizationsClient{
		MockGet: func(ctx context.Context, org string) (*github.Organization, *github.Response, error) {
			return nil, nil, errors.New("could not refresh installation id 67890's token: 401 Unauthorized")
		},
	}

	c := &Services{Organizations: orgs}
	client := NewClient(c, nil).WithRateLimitTracking("acme", "12345", "67890", "broken-key")

	_, _, _ = client.Organizations.Get(context.Background(), "acme")

	q := globalPool.snapshot("broken-key")
	want := now.Add(unhealthyCooldownBase)
	if !q.CooldownUntil.Equal(want) {
		t.Errorf("CooldownUntil = %v, want %v (now + first-failure backoff)", q.CooldownUntil, want)
	}
}
