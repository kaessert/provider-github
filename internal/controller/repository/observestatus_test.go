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

	"github.com/google/go-github/v90/github"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/clients/fake"
)

// atProvider.id mirrors the external name, the repository name.
func TestObserveStatusSetsAtProviderID(t *testing.T) {
	e := &external{github: &ghclient.Client{Services: &ghclient.Services{
		Repositories: &fake.MockRepositoriesClient{
			MockGet: func(context.Context, string, string) (*github.Repository, *github.Response, error) {
				// The archived flag differs, so Observe returns right after the read.
				return &github.Repository{Name: github.Ptr("widgets"), Archived: github.Ptr(true)}, fake.GenerateEmptyResponse(), nil
			},
		},
	}}}

	cr := &v1alpha1.Repository{}
	cr.Spec.ForProvider.Org = "acme"
	meta.SetExternalName(cr, "widgets")
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || got.ResourceUpToDate {
		t.Errorf("Observe = %+v, want exists and not up to date", got)
	}
	if id := cr.Status.AtProvider.ID; id != "widgets" {
		t.Errorf("atProvider.id = %q, want %q", id, "widgets")
	}
}
