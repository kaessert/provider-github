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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// countingGitHub serves a GitHub organization and counts every request that
// reaches it, so a test can assert that none did.
func countingGitHub(t *testing.T) (*external, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"test-org","name":"test-org"}`))
	}))
	t.Cleanup(srv.Close)

	base := srv.URL + "/"
	gh, err := github.NewClient(github.WithURLs(&base, &base))
	if err != nil {
		t.Fatalf("github.NewClient(): %v", err)
	}
	return &external{github: ghclient.NewClient(&ghclient.Services{
		Actions:       gh.Actions,
		Dependabot:    gh.Dependabot,
		Organizations: gh.Organizations,
		Users:         gh.Users,
		Teams:         gh.Teams,
		Repositories:  gh.Repositories,
	}, nil)}, &hits
}

func deleting(cr *clusterv1alpha1.Organization) *clusterv1alpha1.Organization {
	now := metav1.NewTime(time.Now())
	cr.SetDeletionTimestamp(&now)
	return cr
}

// Once a deletion is requested the organization is reported absent, so the
// finalizer clears within one reconcile and nothing is sent to GitHub.
func TestObserveDeletedReportsAbsentWithoutGitHubCall(t *testing.T) {
	e, hits := countingGitHub(t)
	cr := deleting(bareOrganization())

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("e.Observe(...): unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: false}, got); diff != "" {
		t.Errorf("e.Observe(...): -want, +got:\n%s", diff)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("GitHub received %d request(s), want 0", n)
	}
}

func TestNamespacedObserveDeletedReportsAbsentWithoutGitHubCall(t *testing.T) {
	inner, hits := countingGitHub(t)
	e := scopebridge.New[*namespacedv1alpha1.Organization](
		inner, func() *clusterv1alpha1.Organization { return &clusterv1alpha1.Organization{} })
	cr := ddtest.Convert(t, deleting(bareOrganization()), &namespacedv1alpha1.Organization{})

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("e.Observe(...): unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: false}, got); diff != "" {
		t.Errorf("e.Observe(...): -want, +got:\n%s", diff)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("GitHub received %d request(s), want 0", n)
	}
}

// Delete sends nothing to GitHub in either scope.
func TestDeleteSendsNoRequest(t *testing.T) {
	inner, hits := countingGitHub(t)
	if _, err := inner.Delete(context.Background(), deleting(bareOrganization())); err != nil {
		t.Fatalf("e.Delete(...): unexpected error: %v", err)
	}

	e := scopebridge.New[*namespacedv1alpha1.Organization](
		inner, func() *clusterv1alpha1.Organization { return &clusterv1alpha1.Organization{} })
	cr := ddtest.Convert(t, deleting(bareOrganization()), &namespacedv1alpha1.Organization{})
	if _, err := e.Delete(context.Background(), cr); err != nil {
		t.Fatalf("e.Delete(...): unexpected error: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("GitHub received %d request(s), want 0", n)
	}
}

// A resource that is not being deleted is still observed against GitHub.
func TestObserveNotDeletedStillCallsGitHub(t *testing.T) {
	e, hits := countingGitHub(t)

	got, err := e.Observe(context.Background(), bareOrganization())
	if err != nil {
		t.Fatalf("e.Observe(...): unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, got); diff != "" {
		t.Errorf("e.Observe(...): -want, +got:\n%s", diff)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("GitHub received %d request(s), want 1", n)
	}
}
