/*
Copyright 2024 The Crossplane Authors.

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

package clients

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/google/go-github/v90/github"
)

// ServicesCache caches *Services keyed by credential hash so that
// installation tokens are reused across calls within a TTL.
type ServicesCache struct {
	mu      sync.RWMutex
	entries map[string]*cachedServicesEntry
}

type cachedServicesEntry struct {
	services  *Services
	createdAt time.Time
}

var (
	globalServicesCache = &ServicesCache{
		entries: make(map[string]*cachedServicesEntry),
	}
	// Cache clients for 50 minutes (GitHub App tokens expire after 1 hour)
	servicesCacheTimeout = 50 * time.Minute
)

// GenerateCacheKey creates a consistent cache key from credentials. The same
// key is used both for the client cache and for the per-app rate-limit quota
// pool, so that each unique credential blob has a single shared identity.
func GenerateCacheKey(creds string) string {
	hash := sha256.Sum256([]byte(creds))
	return fmt.Sprintf("%x", hash[:8]) // Use first 8 bytes of hash
}

// NewCachedServices creates a new cached GitHub client that reuses tokens
func NewCachedServices(creds string) (*Services, error) {
	cacheKey := GenerateCacheKey(creds)

	globalServicesCache.mu.Lock()
	defer globalServicesCache.mu.Unlock()

	// Check if we have a valid cached client
	if entry, exists := globalServicesCache.entries[cacheKey]; exists {
		// Check if cache entry is still valid
		if time.Since(entry.createdAt) < servicesCacheTimeout {
			return entry.services, nil
		}
		// Remove expired entry
		delete(globalServicesCache.entries, cacheKey)
	}

	// Create new client using existing logic
	services, err := createNewServices(creds)
	if err != nil {
		return nil, err
	}

	// Cache the new client
	globalServicesCache.entries[cacheKey] = &cachedServicesEntry{
		services:  services,
		createdAt: time.Now(),
	}

	return services, nil
}

// ExtractAppIDs returns the GitHub App ID and Installation ID from a
// credential string. The credential string format is
// "appID,installationID,privateKeyPEM"; PEM bodies are base64 plus
// dashes/newlines and never contain commas, so a 3-way split on the first
// two commas is sufficient. The returned values are kept as raw strings
// because they are used as Prometheus label values, not for arithmetic.
func ExtractAppIDs(creds string) (appID, installationID string, err error) {
	parts := strings.SplitN(creds, ",", 3)
	if len(parts) != 3 {
		return "", "", errors.New("invalid format for credentials")
	}
	return parts[0], parts[1], nil
}

// createNewServices contains the original client creation logic
func createNewServices(creds string) (*Services, error) {
	credss := strings.Split(creds, ",")
	if len(credss) != 3 {
		return nil, errors.New("invalid format for credentials")
	}

	appId, err := strconv.Atoi(credss[0])
	if err != nil {
		return nil, err
	}

	installationId, err := strconv.Atoi(credss[1])
	if err != nil {
		return nil, err
	}

	itr, err := ghinstallation.New(http.DefaultTransport, int64(appId), int64(installationId), []byte(credss[2]))
	if err != nil {
		return nil, err
	}

	ghclient, err := github.NewClient(github.WithHTTPClient(&http.Client{Transport: itr}))
	if err != nil {
		return nil, err
	}

	return &Services{
		Actions:       ghclient.Actions,
		Dependabot:    ghclient.Dependabot,
		Organizations: ghclient.Organizations,
		Users:         ghclient.Users,
		Teams:         ghclient.Teams,
		Repositories:  ghclient.Repositories,
	}, nil
}

// CleanupExpiredServices removes expired clients from cache (optional background cleanup)
func CleanupExpiredServices() {
	globalServicesCache.mu.Lock()
	defer globalServicesCache.mu.Unlock()

	now := time.Now()
	for key, entry := range globalServicesCache.entries {
		if now.Sub(entry.createdAt) >= servicesCacheTimeout {
			delete(globalServicesCache.entries, key)
		}
	}
}
