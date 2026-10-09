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

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha1 "github.com/crossplane/provider-github/apis/cluster/v1alpha1"
	"github.com/crossplane/provider-github/internal/telemetry"
)

// pickCredsForPool returns the entry from resolvedCreds whose corresponding
// pool snapshot has the highest remaining quota and is not currently in
// cooldown, plus a reason label suitable for picker_picks_total. If every
// entry is in cooldown, the returned error wraps ErrAllAppsInCooldown
// (use errors.As to extract *CooldownError for the soonest retry hint).
func pickCredsForPool(resolvedCreds []string) (string, string, error) {
	if len(resolvedCreds) == 0 {
		return "", "", errors.New("no credentials available")
	}

	keys := make([]string, len(resolvedCreds))
	keyToCreds := make(map[string]string, len(resolvedCreds))
	for i, c := range resolvedCreds {
		k := GenerateCacheKey(c)
		keys[i] = k
		keyToCreds[k] = c
	}

	chosenKey, reason, err := globalPool.pick(keys)
	if err != nil {
		return "", "", err
	}
	return keyToCreds[chosenKey], reason, nil
}

// ResolveAndConnect resolves every credential entry on pc (Credentials and
// AdditionalCredentials), picks the one with the most available rate-limit
// quota, builds a cached GitHub client for it, and returns a wrapped
// Client ready for use by a controller.
//
// On a fully-exhausted pool the returned error wraps ErrAllAppsInCooldown
// so callers can decide whether to requeue or surface the error.
func ResolveAndConnect(ctx context.Context, kube client.Client, pc *apisv1alpha1.ProviderConfig, metrics *telemetry.RateLimitMetrics, org string) (*Client, error) {
	resolved, err := resolveAllCredentials(ctx, kube, pc)
	if err != nil {
		return nil, err
	}

	chosen, pickReason, err := pickCredsForPool(resolved)
	if err != nil {
		return nil, err
	}

	// Pull the IDs out first so we can record a failure with meaningful
	// labels even if NewCachedServices (below) rejects the credentials.
	// ExtractAppIDs only requires the three-field comma layout; it's
	// strictly more lenient than NewCachedServices.
	appID, installationID, err := ExtractAppIDs(chosen)
	if err != nil {
		return nil, err
	}
	cacheKey := GenerateCacheKey(chosen)

	// Record the pick before any further work — even if construction
	// fails downstream, the picker did select this credential.
	if metrics != nil {
		metrics.RecordPickerPick(org, appID, installationID, pickReason)
	}

	services, err := NewCachedServices(chosen)
	if err != nil {
		// Treat construction failures (non-numeric IDs, malformed PEM,
		// etc.) the same way as token-mint failures: record on the pool
		// so the picker steers away, and bump the unhealthy counter so
		// the operator can see the broken credential in Grafana.
		globalPool.recordResponse(cacheKey, nil, err)
		if metrics != nil {
			metrics.RecordAppUnhealthy(org, appID, installationID)
		}
		return nil, err
	}
	return NewClient(services, metrics).WithRateLimitTracking(org, appID, installationID, cacheKey), nil
}
