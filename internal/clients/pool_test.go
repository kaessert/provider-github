/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package clients

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v90/github"
)

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestPoolRecordResponseUpdatesRemaining(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	resp := &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate: github.Rate{
			Limit:     5000,
			Remaining: 4000,
			Reset:     github.Timestamp{Time: now.Add(30 * time.Minute)},
		},
	}

	p.recordResponse("app1", resp, nil)

	q := p.snapshot("app1")
	if q.Remaining != 4000 {
		t.Errorf("Remaining = %d, want 4000", q.Remaining)
	}
	if !q.CooldownUntil.IsZero() {
		t.Errorf("CooldownUntil = %v, want zero on success", q.CooldownUntil)
	}
}

func TestPoolRecordResponseTooManyRequestsSetsCooldownToReset(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	resetAt := now.Add(10 * time.Minute)
	resp := &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate: github.Rate{
			Limit:     5000,
			Remaining: 0,
			Reset:     github.Timestamp{Time: resetAt},
		},
	}

	p.recordResponse("app1", resp, nil)

	q := p.snapshot("app1")
	if !q.CooldownUntil.Equal(resetAt) {
		t.Errorf("CooldownUntil = %v, want %v", q.CooldownUntil, resetAt)
	}
}

func TestPoolRecordResponseTooManyRequestsNoResetHeaderUsesFloor(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	resp := &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		// No rate info at all
	}

	p.recordResponse("app1", resp, nil)

	q := p.snapshot("app1")
	want := now.Add(cooldownFloor)
	if !q.CooldownUntil.Equal(want) {
		t.Errorf("CooldownUntil = %v, want %v (now + floor)", q.CooldownUntil, want)
	}
}

func TestPoolRecordResponseTooManyRequestsPastResetHeaderUsesFloor(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	resp := &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate: github.Rate{
			Limit:     5000,
			Remaining: 0,
			Reset:     github.Timestamp{Time: now.Add(-1 * time.Minute)},
		},
	}

	p.recordResponse("app1", resp, nil)

	q := p.snapshot("app1")
	want := now.Add(cooldownFloor)
	if !q.CooldownUntil.Equal(want) {
		t.Errorf("CooldownUntil = %v, want %v (floor since reset is in the past)", q.CooldownUntil, want)
	}
}

func TestPoolRecordResponseNilResponseNoOp(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	p.recordResponse("app1", nil, nil)

	q := p.snapshot("app1")
	if q.Remaining != 0 {
		t.Errorf("snapshot Remaining should remain zero for nil response, got %d", q.Remaining)
	}
	if !q.CooldownUntil.IsZero() {
		t.Errorf("nil resp + nil err must not set a cooldown, got %v", q.CooldownUntil)
	}
	if q.ConsecutiveFailures != 0 {
		t.Errorf("nil resp + nil err must not increment ConsecutiveFailures, got %d", q.ConsecutiveFailures)
	}
}

// A (nil response, non-nil error) outcome — ghinstallation can't mint a
// token, or any other pre-HTTP failure — must set a cooldown so the
// picker skips the credential on the next pick.
func TestPoolRecordResponseAuthFailureSetsUnhealthyCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	p.recordResponse("app1", nil, errors.New("could not refresh installation id 67890's token: 401"))

	q := p.snapshot("app1")
	want := now.Add(unhealthyCooldownBase)
	if !q.CooldownUntil.Equal(want) {
		t.Errorf("CooldownUntil = %v, want %v (now + unhealthy cooldown)", q.CooldownUntil, want)
	}
}

// Each consecutive (nil, err) outcome doubles the cooldown: 60s, 120s,
// 240s, 480s, … A permanently broken credential should not be retried
// at a fixed cadence forever.
func TestPoolRecordResponseAuthFailureBackoffGrowsExponentially(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	want := []time.Duration{
		60 * time.Second,
		120 * time.Second,
		240 * time.Second,
		480 * time.Second,
	}
	for i, expected := range want {
		p.recordResponse("app1", nil, errors.New("fail"))
		q := p.snapshot("app1")
		if !q.CooldownUntil.Equal(now.Add(expected)) {
			t.Errorf("after failure #%d: cooldown = %v, want %v", i+1, q.CooldownUntil, now.Add(expected))
		}
	}
}

// Backoff caps at 15 minutes regardless of how many consecutive failures
// have accumulated; a recovered credential can't be locked out for hours.
func TestPoolRecordResponseAuthFailureBackoffCappedAt15Min(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	for i := 0; i < 20; i++ {
		p.recordResponse("app1", nil, errors.New("fail"))
	}

	q := p.snapshot("app1")
	if !q.CooldownUntil.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("capped cooldown = %v, want %v", q.CooldownUntil, now.Add(15*time.Minute))
	}
}

// Any successful HTTP response (resp != nil, regardless of status code,
// including 429) resets ConsecutiveFailures so the next isolated blip
// starts at the 60s base rather than continuing the exponential growth.
// CooldownUntil is deliberately not cleared — max() semantics remain in
// force so a longer 429 cooldown is never shortened.
func TestPoolRecordResponseSuccessResetsUnhealthyCounter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	// Three failures → counter at 3, cooldown at 240s.
	p.recordResponse("app1", nil, errors.New("fail"))
	p.recordResponse("app1", nil, errors.New("fail"))
	p.recordResponse("app1", nil, errors.New("fail"))
	if got := p.snapshot("app1").ConsecutiveFailures; got != 3 {
		t.Fatalf("setup: ConsecutiveFailures = %d, want 3", got)
	}

	// Success — counter resets to zero.
	p.recordResponse("app1", &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate:     github.Rate{Limit: 5000, Remaining: 4900, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
	}, nil)
	if got := p.snapshot("app1").ConsecutiveFailures; got != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0 after success", got)
	}
}

// max() semantics on CooldownUntil: a shorter blip cooldown (60s) cannot
// shrink a longer one (e.g. a 5-minute 429 cooldown) — otherwise the
// picker would try the still-rate-limited credential and trigger another
// 429.
func TestPoolRecordResponseAuthFailureDoesNotShortenLongerCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	resetAt := now.Add(5 * time.Minute)
	p.recordResponse("app1", &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate:     github.Rate{Reset: github.Timestamp{Time: resetAt}},
	}, nil)

	// Now an auth blip arrives — its 60s cooldown is shorter than the
	// existing 5-minute one and must not override it.
	p.recordResponse("app1", nil, errors.New("transient network error"))

	q := p.snapshot("app1")
	if !q.CooldownUntil.Equal(resetAt) {
		t.Errorf("CooldownUntil = %v, want %v (existing 5-min cooldown preserved)", q.CooldownUntil, resetAt)
	}
}

// pick returns a reason for the selection: "only_candidate" when filter
// leaves a single option, "highest_remaining" when one candidate strictly
// dominates, "random_tiebreak" when N ≥ 2 share the top.
func TestPoolPickReasonValues(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		setup    func(*quotaPool)
		keys     []string
		wantReas string
	}{
		{
			name:     "single candidate -> only_candidate",
			keys:     []string{"a"},
			wantReas: "only_candidate",
		},
		{
			name: "two candidates strict winner -> highest_remaining",
			setup: func(p *quotaPool) {
				p.recordResponse("a", &github.Response{Response: &http.Response{StatusCode: 200}, Rate: github.Rate{Limit: 5000, Remaining: 100}}, nil)
				p.recordResponse("b", &github.Response{Response: &http.Response{StatusCode: 200}, Rate: github.Rate{Limit: 5000, Remaining: 4500}}, nil)
			},
			keys:     []string{"a", "b"},
			wantReas: "highest_remaining",
		},
		{
			name: "two candidates tied -> random_tiebreak",
			setup: func(p *quotaPool) {
				p.recordResponse("a", &github.Response{Response: &http.Response{StatusCode: 200}, Rate: github.Rate{Limit: 5000, Remaining: 2500}}, nil)
				p.recordResponse("b", &github.Response{Response: &http.Response{StatusCode: 200}, Rate: github.Rate{Limit: 5000, Remaining: 2500}}, nil)
			},
			keys:     []string{"a", "b"},
			wantReas: "random_tiebreak",
		},
		{
			name: "two unseen candidates -> random_tiebreak",
			keys: []string{"x", "y"},
			// unseen candidates are tied at MaxInt
			wantReas: "random_tiebreak",
		},
		{
			name: "second cred in cooldown -> only_candidate",
			setup: func(p *quotaPool) {
				p.recordResponse("a", &github.Response{Response: &http.Response{StatusCode: 200}, Rate: github.Rate{Limit: 5000, Remaining: 100}}, nil)
				p.recordResponse("b", &github.Response{Response: &http.Response{StatusCode: http.StatusTooManyRequests}, Rate: github.Rate{Reset: github.Timestamp{Time: now.Add(time.Hour)}}}, nil)
			},
			keys:     []string{"a", "b"},
			wantReas: "only_candidate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newQuotaPool(fixedNow(now))
			if tc.setup != nil {
				tc.setup(p)
			}
			_, reason, err := p.pick(tc.keys)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if reason != tc.wantReas {
				t.Errorf("reason = %q, want %q", reason, tc.wantReas)
			}
		})
	}
}

func TestPoolPickEmptyKeysReturnsError(t *testing.T) {
	p := newQuotaPool(time.Now)

	_, _, err := p.pick(nil)
	if err == nil {
		t.Fatal("pick with no keys should return error")
	}
}

func TestPoolPickSingleUnseenAppReturnsIt(t *testing.T) {
	p := newQuotaPool(time.Now)

	chosen, _, err := p.pick([]string{"app1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "app1" {
		t.Errorf("chosen = %q, want app1", chosen)
	}
}

func TestPoolPickPrefersHighestRemaining(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	p.recordResponse("low", &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate:     github.Rate{Limit: 5000, Remaining: 100, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
	}, nil)
	p.recordResponse("high", &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate:     github.Rate{Limit: 5000, Remaining: 4500, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
	}, nil)

	chosen, _, err := p.pick([]string{"low", "high"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "high" {
		t.Errorf("chosen = %q, want high", chosen)
	}
}

func TestPoolPickPrefersUnseenOverDepleted(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	p.recordResponse("seen", &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate:     github.Rate{Limit: 5000, Remaining: 50, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
	}, nil)

	// "unseen" has no recorded quota; it should be treated as fully available.
	chosen, _, err := p.pick([]string{"seen", "unseen"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "unseen" {
		t.Errorf("chosen = %q, want unseen", chosen)
	}
}

func TestPoolPickSkipsAppsInCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	// app1 is in cooldown
	p.recordResponse("app1", &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate:     github.Rate{Limit: 5000, Remaining: 0, Reset: github.Timestamp{Time: now.Add(5 * time.Minute)}},
	}, nil)
	// app2 is healthy
	p.recordResponse("app2", &github.Response{
		Response: &http.Response{StatusCode: 200},
		Rate:     github.Rate{Limit: 5000, Remaining: 100, Reset: github.Timestamp{Time: now.Add(time.Hour)}},
	}, nil)

	chosen, _, err := p.pick([]string{"app1", "app2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "app2" {
		t.Errorf("chosen = %q, want app2 (app1 in cooldown)", chosen)
	}
}

func TestPoolPickAllInCooldownReturnsCooldownError(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	p := newQuotaPool(fixedNow(now))

	soonest := now.Add(2 * time.Minute)
	p.recordResponse("a", &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate:     github.Rate{Reset: github.Timestamp{Time: now.Add(5 * time.Minute)}},
	}, nil)
	p.recordResponse("b", &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate:     github.Rate{Reset: github.Timestamp{Time: soonest}},
	}, nil)

	_, _, err := p.pick([]string{"a", "b"})
	if err == nil {
		t.Fatal("pick with all-in-cooldown should return error")
	}

	// Callers that want the retry hint use errors.As to reach RetryAt.
	var cool *CooldownError
	if !errors.As(err, &cool) {
		t.Fatalf("err is not *CooldownError: %v", err)
	}
	if !cool.RetryAt.Equal(soonest) {
		t.Errorf("RetryAt = %v, want %v (soonest)", cool.RetryAt, soonest)
	}

	// Backward compat: callers that only care about the kind keep working.
	if !errors.Is(err, ErrAllAppsInCooldown) {
		t.Errorf("errors.Is(err, ErrAllAppsInCooldown) = false; want true")
	}
}

func TestPoolPickCooldownExpiredAppIsAgainAvailable(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	currentTime := start
	p := newQuotaPool(func() time.Time { return currentTime })

	p.recordResponse("app1", &github.Response{
		Response: &http.Response{StatusCode: http.StatusTooManyRequests},
		Rate:     github.Rate{Reset: github.Timestamp{Time: start.Add(1 * time.Minute)}},
	}, nil)

	// At t=0, app1 is in cooldown.
	if _, _, err := p.pick([]string{"app1"}); err == nil {
		t.Error("expected error while still in cooldown")
	}

	// Advance past cooldown.
	currentTime = start.Add(2 * time.Minute)
	chosen, _, err := p.pick([]string{"app1"})
	if err != nil {
		t.Fatalf("after cooldown, expected app1 to be picked, got error: %v", err)
	}
	if chosen != "app1" {
		t.Errorf("chosen = %q, want app1", chosen)
	}
}
