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

package runnergroup

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
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// bareRunnerGroup declares nothing beyond the group it points at.
func bareRunnerGroup(visibility string) *clusterv1alpha1.RunnerGroup {
	cr := &clusterv1alpha1.RunnerGroup{}
	cr.Spec.ForProvider.Org = testOrg
	cr.Spec.ForProvider.Visibility = visibility
	meta.SetExternalName(cr, testGroupName)
	return cr
}

func groupsAnswering(list *github.RunnerGroups, access *github.ListRepositories) *external {
	e := newExternal(&fake.MockActionsClient{
		MockListOrganizationRunnerGroups: func(context.Context, string, *github.ListOrgRunnerGroupOptions) (*github.RunnerGroups, *github.Response, error) {
			return list, fake.GenerateEmptyResponse(), nil
		},
		MockListRepositoryAccessRunnerGroup: func(context.Context, string, int64, *github.ListOptions) (*github.ListRepositories, *github.Response, error) {
			return access, fake.GenerateEmptyResponse(), nil
		},
	}, nil)
	return &e
}

// minimalObserveCases are GitHub responses stripped of every optional field.
// Observe must not dereference what GitHub left out.
var minimalObserveCases = map[string]struct {
	reason     string
	list       *github.RunnerGroups
	access     *github.ListRepositories
	visibility string
	want       managed.ExternalObservation
	ready      xpv2.ConditionReason
}{
	"EmptyList": {
		reason: "A listing without groups means the group does not exist.",
		list:   &github.RunnerGroups{},
		want:   managed.ExternalObservation{ResourceExists: false},
	},
	"GroupWithOnlyName": {
		reason: "A group carrying only its name matches a spec that declares nothing.",
		list:   &github.RunnerGroups{RunnerGroups: []*github.RunnerGroup{{Name: github.Ptr(testGroupName)}}},
		want:   managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
		ready:  xpv2.ReasonAvailable,
	},
	"EmptyRepositoryAccessList": {
		reason: "A selected group with an empty access list matches a spec that selects no repository.",
		list: &github.RunnerGroups{RunnerGroups: []*github.RunnerGroup{
			{Name: github.Ptr(testGroupName), Visibility: github.Ptr("selected")},
		}},
		access:     &github.ListRepositories{},
		visibility: "selected",
		want:       managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true},
		ready:      xpv2.ReasonAvailable,
	},
}

func TestObserveMinimalResponse(t *testing.T) {
	for name, tc := range minimalObserveCases {
		t.Run(name, func(t *testing.T) {
			cr := bareRunnerGroup(tc.visibility)
			got, err := groupsAnswering(tc.list, tc.access).Observe(context.Background(), cr)
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
			e := scopebridge.New[*namespacedv1alpha1.RunnerGroup](groupsAnswering(tc.list, tc.access),
				func() *clusterv1alpha1.RunnerGroup { return &clusterv1alpha1.RunnerGroup{} })
			cr := ddtest.Convert(t, bareRunnerGroup(tc.visibility), &namespacedv1alpha1.RunnerGroup{})
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
