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

package team

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// githubServerError is the error go-github returns for an HTTP 500 answer.
func githubServerError() error {
	return &github.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusInternalServerError},
		Message:  "server error",
	}
}

// deletionAnswering returns an external client whose team deletion answers
// with err, recording the slug it is asked to delete.
func deletionAnswering(err error, slugs *[]string) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Teams: &fake.MockTeamsClient{
			MockDeleteTeamBySlug: func(_ context.Context, _, slug string) (*github.Response, error) {
				*slugs = append(*slugs, slug)
				return fake.GenerateEmptyResponse(), err
			},
		},
	}}}
}

var deleteCases = map[string]struct {
	reason  string
	apiErr  error
	wantErr bool
}{
	"Deleted": {
		reason: "A deletion GitHub accepts must succeed.",
	},
	"ServerError": {
		reason:  "A server error from GitHub must be returned so the delete is retried.",
		apiErr:  githubServerError(),
		wantErr: true,
	},
}

func TestDeleteServerError(t *testing.T) {
	for name, tc := range deleteCases {
		t.Run(name, func(t *testing.T) {
			var slugs []string
			got, err := deletionAnswering(tc.apiErr, &slugs).Delete(context.Background(), bareTeam())
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want error: %t", tc.reason, err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, tc.apiErr) {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want the GitHub error", tc.reason, err)
			}
			if diff := cmp.Diff([]string{"platform"}, slugs); diff != "" {
				t.Errorf("\n%s\nDeleteTeamBySlug slugs: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(managed.ExternalDelete{}, got); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestNamespacedDeleteServerError(t *testing.T) {
	for name, tc := range deleteCases {
		t.Run(name, func(t *testing.T) {
			var slugs []string
			e := scopebridge.New[*namespacedv1alpha1.Team](deletionAnswering(tc.apiErr, &slugs),
				func() *clusterv1alpha1.Team { return &clusterv1alpha1.Team{} })
			cr := ddtest.Convert(t, bareTeam(), &namespacedv1alpha1.Team{})
			got, err := e.Delete(context.Background(), cr)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want error: %t", tc.reason, err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, tc.apiErr) {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want the GitHub error", tc.reason, err)
			}
			if diff := cmp.Diff([]string{"platform"}, slugs); diff != "" {
				t.Errorf("\n%s\nDeleteTeamBySlug slugs: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(managed.ExternalDelete{}, got); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
