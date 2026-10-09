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

// deletionAnswering returns an external client whose repository deletion
// answers with err, counting the calls it receives.
func deletionAnswering(err error, calls *int) *external {
	return &external{github: &ghclient.Client{Services: &ghclient.Services{
		Repositories: &fake.MockRepositoriesClient{
			MockDelete: func(context.Context, string, string) (*github.Response, error) {
				*calls++
				return fake.GenerateEmptyResponse(), err
			},
		},
	}}}
}

func forceDeleted(force bool) *clusterv1alpha1.Repository {
	cr := bareRepository()
	cr.Spec.ForProvider.ForceDelete = &force
	return cr
}

var deleteCases = map[string]struct {
	reason    string
	cr        func() *clusterv1alpha1.Repository
	apiErr    error
	wantCalls int
	wantErr   bool
}{
	"Deleted": {
		reason:    "A deletion GitHub accepts must succeed.",
		cr:        func() *clusterv1alpha1.Repository { return forceDeleted(true) },
		wantCalls: 1,
	},
	"ServerError": {
		reason:    "A server error from GitHub must be returned so the delete is retried.",
		cr:        func() *clusterv1alpha1.Repository { return forceDeleted(true) },
		apiErr:    githubServerError(),
		wantCalls: 1,
		wantErr:   true,
	},
	"ForceDeleteUnset": {
		reason:  "A repository is only deleted when forceDelete is set; otherwise GitHub is not called.",
		cr:      bareRepository,
		apiErr:  githubServerError(),
		wantErr: true,
	},
	"ForceDeleteFalse": {
		reason:  "A repository with forceDelete false is not deleted and GitHub is not called.",
		cr:      func() *clusterv1alpha1.Repository { return forceDeleted(false) },
		apiErr:  githubServerError(),
		wantErr: true,
	},
}

func TestDeleteServerError(t *testing.T) {
	for name, tc := range deleteCases {
		t.Run(name, func(t *testing.T) {
			var calls int
			got, err := deletionAnswering(tc.apiErr, &calls).Delete(context.Background(), tc.cr())
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want error: %t", tc.reason, err, tc.wantErr)
			}
			if tc.wantCalls > 0 && tc.wantErr && !errors.Is(err, tc.apiErr) {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want the GitHub error", tc.reason, err)
			}
			if calls != tc.wantCalls {
				t.Errorf("\n%s\nRepositories.Delete calls = %d, want %d", tc.reason, calls, tc.wantCalls)
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
			var calls int
			e := scopebridge.New[*namespacedv1alpha1.Repository](deletionAnswering(tc.apiErr, &calls),
				func() *clusterv1alpha1.Repository { return &clusterv1alpha1.Repository{} })
			cr := ddtest.Convert(t, tc.cr(), &namespacedv1alpha1.Repository{})
			got, err := e.Delete(context.Background(), cr)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want error: %t", tc.reason, err, tc.wantErr)
			}
			if tc.wantCalls > 0 && tc.wantErr && !errors.Is(err, tc.apiErr) {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want the GitHub error", tc.reason, err)
			}
			if calls != tc.wantCalls {
				t.Errorf("\n%s\nRepositories.Delete calls = %d, want %d", tc.reason, calls, tc.wantCalls)
			}
			if diff := cmp.Diff(managed.ExternalDelete{}, got); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
