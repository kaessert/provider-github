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
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/crossplane/provider-github/internal/clients"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// Deleting an Organization resource leaves the GitHub organization alone and
// only marks the resource Deleting. The client has no services wired, so any
// GitHub call would panic: Delete cannot produce a server error.

func TestDeleteNoAPICallNoServerError(t *testing.T) {
	e := &external{github: &ghclient.Client{Services: &ghclient.Services{}}}
	cr := bareOrganization()

	got, err := e.Delete(context.Background(), cr)
	if err != nil {
		t.Errorf("e.Delete(...): unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalDelete{}, got); diff != "" {
		t.Errorf("e.Delete(...): -want, +got:\n%s", diff)
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonDeleting {
		t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.ReasonDeleting)
	}
}

func TestNamespacedDeleteNoAPICallNoServerError(t *testing.T) {
	e := scopebridge.New[*namespacedv1alpha1.Organization](
		&external{github: &ghclient.Client{Services: &ghclient.Services{}}},
		func() *clusterv1alpha1.Organization { return &clusterv1alpha1.Organization{} })
	cr := ddtest.Convert(t, bareOrganization(), &namespacedv1alpha1.Organization{})

	got, err := e.Delete(context.Background(), cr)
	if err != nil {
		t.Errorf("e.Delete(...): unexpected error: %v", err)
	}
	if diff := cmp.Diff(managed.ExternalDelete{}, got); diff != "" {
		t.Errorf("e.Delete(...): -want, +got:\n%s", diff)
	}
	if c := cr.GetCondition(xpv2.TypeReady); c.Reason != xpv2.ReasonDeleting {
		t.Errorf("Ready reason = %q, want %q", c.Reason, xpv2.ReasonDeleting)
	}
}
