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

package repository

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// bareRepository declares nothing beyond the repository it points at: no
// settings, permissions, webhooks, branch protection or rulesets.
func bareRepository() *clusterv1alpha1.Repository {
	cr := &clusterv1alpha1.Repository{}
	cr.Spec.ForProvider.Org = "acme"
	meta.SetExternalName(cr, "widgets")
	return cr
}

// repositoryAnswering returns an external client whose repository lookup
// answers with r and whose collaborator and team listings are empty.
func repositoryAnswering(r *github.Repository) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Repositories: &fake.MockRepositoriesClient{
			MockGet: func(context.Context, string, string) (*github.Repository, *github.Response, error) {
				return r, fake.GenerateEmptyResponse(), nil
			},
			MockListCollaborators: func(context.Context, string, string, *github.ListCollaboratorsOptions) ([]*github.User, *github.Response, error) {
				return nil, fake.GenerateEmptyResponse(), nil
			},
			MockListTeams: func(context.Context, string, string, *github.ListOptions) ([]*github.Team, *github.Response, error) {
				return nil, fake.GenerateEmptyResponse(), nil
			},
		},
	}}}
}

// minimalObserveCases are GitHub responses stripped of every optional field.
// The settings GitHub leaves out are nil pointers that Observe must not
// dereference.
var minimalObserveCases = map[string]struct {
	reason string
	gh     *github.Repository
	want   managed.ExternalObservation
	ready  xpv2.ConditionReason
}{
	"EmptyRepository": {
		reason: "A repository with no fields is public, which differs from the default of private.",
		gh:     &github.Repository{},
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false},
	},
	"PrivateRepositoryWithNoOtherFields": {
		reason: "A private repository with no other fields matches a spec that declares nothing.",
		gh:     &github.Repository{Private: github.Ptr(true)},
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
		ready:  xpv2.ReasonAvailable,
	},
}

func TestObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			cr := bareRepository()
			got, err := repositoryAnswering(tc.gh).Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Reason != tc.ready {
				t.Errorf("\n%s\nReady reason = %q, want %q", tc.reason, c.Reason, tc.ready)
			}
		})
	}
}

func TestNamespacedObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			e := scopebridge.New[*namespacedv1alpha1.Repository](repositoryAnswering(tc.gh),
				func() *clusterv1alpha1.Repository { return &clusterv1alpha1.Repository{} })
			cr := ddtest.Convert(t, bareRepository(), &namespacedv1alpha1.Repository{})
			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s", tc.reason, diff)
			}
			if c := cr.GetCondition(xpv2.TypeReady); c.Reason != tc.ready {
				t.Errorf("\n%s\nReady reason = %q, want %q", tc.reason, c.Reason, tc.ready)
			}
		})
	}
}
