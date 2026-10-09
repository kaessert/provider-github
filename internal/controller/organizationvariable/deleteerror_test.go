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

package organizationvariable

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

// deletionAnswering returns an external client whose variable deletion answers
// with err, counting the calls it receives.
func deletionAnswering(err error, calls *int) *external {
	e := newExternalWithActions(&fake.MockActionsClient{
		MockDeleteOrgVariable: func(context.Context, string, string) (*github.Response, error) {
			*calls++
			return fake.GenerateEmptyResponse(), err
		},
	}, nil)
	return &e
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
			var calls int
			got, err := deletionAnswering(tc.apiErr, &calls).Delete(context.Background(), newCR())
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want error: %t", tc.reason, err, tc.wantErr)
			}
			if tc.apiErr != nil && !errors.Is(err, tc.apiErr) {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want the GitHub error", tc.reason, err)
			}
			if calls != 1 {
				t.Errorf("\n%s\nDeleteOrgVariable calls = %d, want 1", tc.reason, calls)
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
			e := scopebridge.New[*namespacedv1alpha1.OrganizationVariable](deletionAnswering(tc.apiErr, &calls),
				func() *clusterv1alpha1.OrganizationVariable { return &clusterv1alpha1.OrganizationVariable{} })
			cr := ddtest.Convert(t, newCR(), &namespacedv1alpha1.OrganizationVariable{})
			got, err := e.Delete(context.Background(), cr)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want error: %t", tc.reason, err, tc.wantErr)
			}
			if tc.apiErr != nil && !errors.Is(err, tc.apiErr) {
				t.Errorf("\n%s\ne.Delete(...): error = %v, want the GitHub error", tc.reason, err)
			}
			if calls != 1 {
				t.Errorf("\n%s\nDeleteOrgVariable calls = %d, want 1", tc.reason, calls)
			}
			if diff := cmp.Diff(managed.ExternalDelete{}, got); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
