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
	"testing"

	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/clients/fake"
)

// observeOnlyTeam is what an Observe-only import declares: the organization and
// the external name, and nothing else.
func observeOnlyTeam(policies ...xpv2.ManagementAction) *v1alpha1.Team {
	cr := &v1alpha1.Team{}
	cr.Spec.ForProvider.Org = "acme"
	cr.Spec.ManagementPolicies = policies
	meta.SetExternalName(cr, statusTeamName)
	return cr
}

// externalHoldingTeam serves a team with a description, closed privacy, a
// parent and members that the spec may or may not declare.
func externalHoldingTeam() *external {
	e := teamExternal("held on GitHub")
	e.github.Teams.(*fake.MockTeamsClient).MockGetTeamBySlug = func(context.Context, string, string) (*github.Team, *github.Response, error) {
		return &github.Team{
			Privacy:     github.Ptr("closed"),
			Description: github.Ptr("held on GitHub"),
			Parent:      &github.Team{Slug: github.Ptr("parent-team"), Name: github.Ptr("Parent Team")},
		}, nil, nil
	}
	return e
}

// An omitted description, privacy, parent and members are not compared under an
// Observe-only policy.
func TestObserveOnlyOmittedFieldsNoDrift(t *testing.T) {
	cr := observeOnlyTeam(xpv2.ManagementActionObserve)
	got, err := externalHoldingTeam().Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || !got.ResourceUpToDate {
		t.Fatalf("Observe = %+v, want exists and up to date", got)
	}
	assertReady(t, cr, corev1.ConditionTrue, xpv2.ReasonAvailable)
}

// Under a policy that writes, omitted fields keep their defaults and report
// drift, as before.
func TestObserveWritesAllowedOmittedFieldsStillDrift(t *testing.T) {
	for name, policies := range map[string][]xpv2.ManagementAction{
		"Default":       nil,
		"All":           {xpv2.ManagementActionAll},
		"ObserveUpdate": {xpv2.ManagementActionObserve, xpv2.ManagementActionUpdate},
	} {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyTeam(policies...)
			got, err := externalHoldingTeam().Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and not up to date", got)
			}
			assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
		})
	}
}

// A field the Observe-only spec does declare is still compared, an explicitly
// empty member list included.
func TestObserveOnlyDeclaredFieldsStillDrift(t *testing.T) {
	cases := map[string]func(*v1alpha1.Team){
		"Description": func(cr *v1alpha1.Team) { cr.Spec.ForProvider.Description = "declared elsewhere" },
		"Privacy":     func(cr *v1alpha1.Team) { cr.Spec.ForProvider.Privacy = github.Ptr("secret") },
		"Parent":      func(cr *v1alpha1.Team) { cr.Spec.ForProvider.Parent = github.Ptr("other-parent") },
		"EmptyMembers": func(cr *v1alpha1.Team) {
			cr.Spec.ForProvider.Members = []v1alpha1.TeamMemberUser{}
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			cr := observeOnlyTeam(xpv2.ManagementActionObserve)
			mod(cr)
			got, err := externalHoldingTeam().Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.ResourceExists || got.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and not up to date", got)
			}
			assertReady(t, cr, corev1.ConditionFalse, xpv2.ReasonUnavailable)
		})
	}
}
