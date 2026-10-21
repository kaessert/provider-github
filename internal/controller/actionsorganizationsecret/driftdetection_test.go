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

package actionsorganizationsecret

import (
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/provider-github/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/controller/scopebridge"
	"github.com/provider-github/provider-github/internal/driftdetection/ddtest"
)

// A resource with no spec.driftDetection block must reconcile exactly as it did
// before the block existed: the decorator wired at both of this kind's
// reconciler sites has to be indistinguishable from the bare client, in the
// cluster-scoped variant and in the namespaced variant served through the
// scope bridge.
func TestDriftDetectionAbsentIsPassThrough(t *testing.T) {
	scenarios := map[string]crFunc{
		"UpToDate": existing("all"),
		"Drift": func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
			cr := existing("private")(o)
			cr.Spec.ForProvider.Visibility = "all"
			return cr
		},
		"NotFound": fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
	}

	// The harness builds the resource before the client it observes it with, once
	// for the bare run and once for the wrapped one, so each pair shares an
	// organization.
	for name, setup := range scenarios {
		t.Run("Cluster/"+name, func(t *testing.T) {
			var o *ghOrg
			ddtest.RequirePassThrough(t,
				func() managed.ExternalClient { return o.external(newKube(t, valueSecret(testValue, "5"))) },
				func() resource.Managed {
					o = newOrg(t)
					return setup(o)
				})
		})
		t.Run("Namespaced/"+name, func(t *testing.T) {
			var o *ghOrg
			ddtest.RequirePassThrough(t,
				func() managed.ExternalClient {
					e := o.external(newKube(t, valueSecret(testValue, "5")))
					e.secretNamespace = testNamespace
					return scopebridge.New[*namespacedv1alpha1.ActionsOrganizationSecret](e,
						func() *clusterv1alpha1.ActionsOrganizationSecret { return &clusterv1alpha1.ActionsOrganizationSecret{} })
				},
				func() resource.Managed {
					o = newOrg(t)
					return ddtest.Convert(t, setup(o), &namespacedv1alpha1.ActionsOrganizationSecret{})
				})
		})
	}
}
