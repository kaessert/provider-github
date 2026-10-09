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
)

// TestPickCredsForPCSingleCreds returns the only available credential when
// there are no AdditionalCredentials.
func TestPickCredsForPCSingleCreds(t *testing.T) {
	chosen, _, err := pickCredsForPool([]string{"creds-A"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "creds-A" {
		t.Errorf("chosen = %q, want creds-A", chosen)
	}
}

// TestPickCredsForPCMultipleCredsPrefersHealthier picks the credential
// whose pool entry shows the most remaining quota.
func TestPickCredsForPCMultipleCredsPrefersHealthier(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	credsA := "creds-A"
	credsB := "creds-B"
	keyA := GenerateCacheKey(credsA)
	keyB := GenerateCacheKey(credsB)

	// Make A nearly empty, B fresh.
	globalPool.recordResponse(keyA, &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate:     github.Rate{Limit: 5000, Remaining: 10, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
	}, nil)
	globalPool.recordResponse(keyB, &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate:     github.Rate{Limit: 5000, Remaining: 4900, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
	}, nil)

	chosen, _, err := pickCredsForPool([]string{credsA, credsB})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != credsB {
		t.Errorf("chosen = %q, want %q", chosen, credsB)
	}
}

// ResolveAndConnect threads the picker's reason through to the
// github_app_picker_picks_total counter, labeled with the chosen
// credential's app_id / app_installation_id. With one credential, the
// reason is "only_candidate".
func TestResolveAndConnectRecordsPickerPick(t *testing.T) {
	kube := newFakeKubeWithSecret(t, "github-secret", "crossplane-system", map[string][]byte{
		"creds": []byte("12345,67890,-----BEGIN RSA PRIVATE KEY-----\nfake\n-----END RSA PRIVATE KEY-----"),
	}).Build()
	pc := pcWithSecretRef("github-secret", "crossplane-system", "creds")

	swapGlobalPool(t, newQuotaPool(time.Now))
	metrics := telemetryNewForTest(t)

	// NewCachedServices will fail because the PEM body is fake, but the
	// picker still ran successfully — we only care that the pick was
	// recorded with the correct labels and reason.
	_, _ = ResolveAndConnect(context.Background(), kube, pc, metrics, "acme")

	if got := metrics.PicksCountForTest("acme", "12345", "67890", "only_candidate"); got != 1 {
		t.Errorf("picks_total{reason=only_candidate} = %v, want 1", got)
	}
}

// Construction failures inside NewCachedServices (non-numeric IDs,
// malformed PEM) happen before any wrapped GitHub call. ResolveAndConnect
// must still record the failure on the pool and bump the unhealthy
// counter so the picker can avoid the broken credential on next pick.
func TestResolveAndConnectNewClientFailureRecordsToPool(t *testing.T) {
	// Three comma-separated fields (passes ExtractAppIDs) but the second
	// field isn't a valid integer, so strconv.Atoi inside NewCachedServices
	// will fail before any HTTP call.
	badCreds := []byte("12345,not-a-number,-----BEGIN RSA PRIVATE KEY-----\nbody\n-----END RSA PRIVATE KEY-----")

	kube := newFakeKubeWithSecret(t, "github-secret", "crossplane-system", map[string][]byte{
		"creds": badCreds,
	}).Build()
	pc := pcWithSecretRef("github-secret", "crossplane-system", "creds")

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	_, err := ResolveAndConnect(context.Background(), kube, pc, nil, "acme")
	if err == nil {
		t.Fatal("expected error from NewCachedServices on bad creds")
	}

	// Pool should reflect the failure so the picker steers away next time.
	cacheKey := GenerateCacheKey(string(badCreds))
	q := globalPool.snapshot(cacheKey)
	if q.ConsecutiveFailures != 1 {
		t.Errorf("ConsecutiveFailures = %d, want 1", q.ConsecutiveFailures)
	}
	if q.CooldownUntil.IsZero() {
		t.Errorf("CooldownUntil = zero, want a future time (first-failure backoff)")
	}
}

// TestPickCredsForPCAllInCooldownReturnsTypedError lets callers requeue
// reconciles intelligently when every app is exhausted.
func TestPickCredsForPCAllInCooldownReturnsTypedError(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	swapGlobalPool(t, newQuotaPool(func() time.Time { return now }))

	credsA := "creds-A"
	credsB := "creds-B"
	soonest := now.Add(2 * time.Minute)

	globalPool.recordResponse(GenerateCacheKey(credsA), &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate:     github.Rate{Reset: github.Timestamp{Time: now.Add(5 * time.Minute)}},
	}, nil)
	globalPool.recordResponse(GenerateCacheKey(credsB), &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate:     github.Rate{Reset: github.Timestamp{Time: soonest}},
	}, nil)

	_, _, err := pickCredsForPool([]string{credsA, credsB})
	if err == nil {
		t.Fatal("expected error when all in cooldown")
	}
	if !errors.Is(err, ErrAllAppsInCooldown) {
		t.Errorf("err is not ErrAllAppsInCooldown: %v", err)
	}
	var cool *CooldownError
	if !errors.As(err, &cool) {
		t.Fatalf("err is not *CooldownError: %v", err)
	}
	if !cool.RetryAt.Equal(soonest) {
		t.Errorf("RetryAt = %v, want soonest %v", cool.RetryAt, soonest)
	}
}
