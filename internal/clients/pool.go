/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package clients

import (
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/google/go-github/v90/github"
)

// cooldownFloor is the minimum amount of time we will skip a credential
// after a 429 when GitHub does not provide a usable X-RateLimit-Reset header.
const cooldownFloor = 30 * time.Second

// Auth/network failure backoff. A credential that fails without producing
// an HTTP response (typically ghinstallation 401 or a transient network
// error) is skipped for unhealthyCooldownBase initially; each consecutive
// failure doubles the wait, capped at unhealthyCooldownCap. The counter
// resets on any successful HTTP response so a mostly-healthy credential
// with occasional blips doesn't accumulate a multi-minute backoff.
const (
	unhealthyCooldownBase = 60 * time.Second
	unhealthyCooldownCap  = 15 * time.Minute
)

// ErrAllAppsInCooldown is the sentinel "every credential is in cooldown"
// error. Callers that only need to detect the condition use
// errors.Is(err, ErrAllAppsInCooldown). Callers that need the soonest
// retry hint use errors.As to unwrap a *CooldownError.
var ErrAllAppsInCooldown = errors.New("all GitHub apps are in rate-limit cooldown")

// CooldownError is returned when every credential in the pool is
// currently cooling down. RetryAt carries the soonest expiry time so the
// caller can issue a sensible RequeueAfter or backoff. Unwraps to
// ErrAllAppsInCooldown so legacy errors.Is checks keep working.
type CooldownError struct {
	RetryAt time.Time
}

func (e *CooldownError) Error() string {
	return ErrAllAppsInCooldown.Error()
}

func (e *CooldownError) Unwrap() error {
	return ErrAllAppsInCooldown
}

// AppQuota is a snapshot of the last-known rate-limit state for one credential.
type AppQuota struct {
	Remaining     int
	CooldownUntil time.Time
	// ConsecutiveFailures counts (nil-response, non-nil-error) outcomes
	// since the last successful HTTP response. Drives the exponential
	// backoff for unhealthyCooldown. Reset to zero on any non-nil resp.
	ConsecutiveFailures int
}

type quotaPool struct {
	mu     sync.RWMutex
	now    func() time.Time
	quotas map[string]*AppQuota
}

func newQuotaPool(now func() time.Time) *quotaPool {
	return &quotaPool{
		now:    now,
		quotas: make(map[string]*AppQuota),
	}
}

// globalPool is the process-wide quota pool. It is updated by every
// rate-limited GitHub call and consulted by the credential picker on each
// reconcile.
var globalPool = newQuotaPool(time.Now)

// recordResponse updates the quota snapshot for the given credential cache
// key based on the outcome of a GitHub call. Three cases:
//
//   - (resp != nil, *) — normal HTTP response. Rate-limit headers update
//     the snapshot; a 429 sets a cooldown until the response's Reset time
//     (or now+cooldownFloor if that header is missing or in the past).
//   - (nil, err) — the call failed without producing a response, typically
//     because ghinstallation could not mint an installation token (401) or
//     because of a network blip. ConsecutiveFailures bumps and the
//     credential is marked unhealthy for an exponentially-growing cooldown
//     (60s, 120s, 240s, ... capped at 15m) so the picker steers away.
//   - (nil, nil) — no-op.
//
// Cooldown updates use max() semantics: a shorter cooldown can never
// shrink a longer one. This stops a 60-second auth blip from undoing the
// longer cooldown set by a 429. Any successful HTTP response (resp != nil)
// resets ConsecutiveFailures so a recovered credential starts fresh.
func (p *quotaPool) recordResponse(cacheKey string, resp *github.Response, err error) {
	if resp == nil {
		if err != nil {
			p.recordUnhealthy(cacheKey)
		}
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	q, ok := p.quotas[cacheKey]
	if !ok {
		q = &AppQuota{}
		p.quotas[cacheKey] = q
	}

	// A real HTTP response — including a 429 — means ghinstallation could
	// reach GitHub and the credential is at least minting tokens. Reset
	// the unhealthy counter so the next auth/network blip starts at 60s.
	q.ConsecutiveFailures = 0

	if resp.Rate.Limit > 0 {
		q.Remaining = resp.Rate.Remaining
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		now := p.now()
		floor := now.Add(cooldownFloor)
		cooldown := resp.Rate.Reset.Time
		if cooldown.Before(floor) {
			cooldown = floor
		}
		if cooldown.After(q.CooldownUntil) {
			q.CooldownUntil = cooldown
		}
	}
}

// recordUnhealthy increments the failure counter for cacheKey and extends
// its cooldown by the next exponential backoff step.
func (p *quotaPool) recordUnhealthy(cacheKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q, ok := p.quotas[cacheKey]
	if !ok {
		q = &AppQuota{}
		p.quotas[cacheKey] = q
	}
	q.ConsecutiveFailures++
	candidate := p.now().Add(unhealthyBackoff(q.ConsecutiveFailures))
	if candidate.After(q.CooldownUntil) {
		q.CooldownUntil = candidate
	}
}

// unhealthyBackoff returns the cooldown duration for the n-th consecutive
// failure (n >= 1): 60s, 120s, 240s, ... capped at unhealthyCooldownCap.
// The shift is bounded so a runaway counter cannot overflow.
func unhealthyBackoff(n int) time.Duration {
	if n < 1 {
		return unhealthyCooldownBase
	}
	// Cap the shift count: 60s << 16 already exceeds the 15-min cap.
	shift := n - 1
	if shift > 16 {
		shift = 16
	}
	d := unhealthyCooldownBase << uint(shift)
	if d > unhealthyCooldownCap || d <= 0 {
		return unhealthyCooldownCap
	}
	return d
}

// snapshot returns a copy of the quota for the given cache key. An unseen
// key returns a zero-valued AppQuota.
func (p *quotaPool) snapshot(cacheKey string) AppQuota {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if q, ok := p.quotas[cacheKey]; ok {
		return *q
	}
	return AppQuota{}
}

// Reason values returned by pick to label picker_picks_total. Stable
// across releases — operators may build dashboards keyed on them.
const (
	PickReasonOnlyCandidate    = "only_candidate"
	PickReasonHighestRemaining = "highest_remaining"
	PickReasonRandomTiebreak   = "random_tiebreak"
)

// pick chooses the credential with the most remaining quota, skipping any
// currently in cooldown. Unseen credentials are treated as fully available
// so they get tried before depleted ones. If every credential is in
// cooldown, a *CooldownError carrying the soonest expiry is returned;
// callers can detect it via errors.As (for the retry hint) or errors.Is
// (for the kind alone).
//
// The second return value names why the selection was made
// (PickReason* constants above), suitable as a Prometheus label.
func (p *quotaPool) pick(cacheKeys []string) (string, string, error) {
	if len(cacheKeys) == 0 {
		return "", "", errors.New("no credentials available")
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	now := p.now()

	type candidate struct {
		key       string
		remaining int
	}
	candidates := make([]candidate, 0, len(cacheKeys))
	soonestReset := time.Time{}

	for _, k := range cacheKeys {
		q, ok := p.quotas[k]
		if !ok {
			// Unseen: treat as fully available, sort to the top.
			candidates = append(candidates, candidate{key: k, remaining: 1<<31 - 1})
			continue
		}
		if !q.CooldownUntil.IsZero() && q.CooldownUntil.After(now) {
			if soonestReset.IsZero() || q.CooldownUntil.Before(soonestReset) {
				soonestReset = q.CooldownUntil
			}
			continue
		}
		candidates = append(candidates, candidate{key: k, remaining: q.Remaining})
	}

	if len(candidates) == 0 {
		return "", "", &CooldownError{RetryAt: soonestReset}
	}

	// Exactly one candidate post-filter: no real choice was made, but the
	// metric still distinguishes this from a multi-candidate pick.
	if len(candidates) == 1 {
		return candidates[0].key, PickReasonOnlyCandidate, nil
	}

	best := candidates[0].remaining
	bestKeys := make([]string, 1, len(candidates))
	bestKeys[0] = candidates[0].key
	for _, c := range candidates[1:] {
		switch {
		case c.remaining > best:
			best = c.remaining
			bestKeys = append(bestKeys[:0], c.key)
		case c.remaining == best:
			bestKeys = append(bestKeys, c.key)
		}
	}

	if len(bestKeys) == 1 {
		return bestKeys[0], PickReasonHighestRemaining, nil
	}
	// Random tie-break across credentials with identical remaining quota.
	return bestKeys[rand.Intn(len(bestKeys))], PickReasonRandomTiebreak, nil //nolint:gosec
}
