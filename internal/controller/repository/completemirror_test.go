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
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/crossplane/provider-github/internal/clients/fake"
	"github.com/crossplane/provider-github/internal/controller/scopebridge"
	"github.com/crossplane/provider-github/internal/driftdetection/ddtest"
)

// mirrored is the part of status.atProvider that reflects only what GitHub
// holds, whatever the spec says.
type mirrored struct {
	Permissions           v1alpha1.RepositoryPermissionsObservation
	Webhooks              []v1alpha1.RepositoryWebhookObservation
	BranchProtectionRules []v1alpha1.BranchProtectionRuleObservation
	RepositoryRules       []v1alpha1.RepositoryRulesetObservation
	Topics                []string
}

func mirrorOf(ap v1alpha1.RepositoryObservation) mirrored {
	return mirrored{
		Permissions:           ap.Permissions,
		Webhooks:              ap.Webhooks,
		BranchProtectionRules: ap.BranchProtectionRules,
		RepositoryRules:       ap.RepositoryRules,
		Topics:                ap.Topics,
	}
}

// observed is what one Observe returned and left in status.atProvider.
type observed struct {
	obs managed.ExternalObservation
	ap  v1alpha1.RepositoryObservation
}

// observeBoth observes a copy of cr through the cluster-scoped client and
// another through the namespaced variant served by the scope bridge.
func observeBoth(t *testing.T, repos *fake.MockRepositoriesClient, cr *v1alpha1.Repository) map[string]observed {
	t.Helper()
	cr.Spec.ForProvider.Org = "acme"
	e := &external{github: clientFor(repos), secretNamespace: "default"}
	out := map[string]observed{}

	ns := ddtest.Convert(t, cr.DeepCopy(), &namespacedv1alpha1.Repository{})
	ns.Namespace = "default"
	bridged := scopebridge.New[*namespacedv1alpha1.Repository](e, func() *v1alpha1.Repository { return &v1alpha1.Repository{} })
	nsObs, err := bridged.Observe(context.Background(), ns)
	if err != nil {
		t.Fatalf("namespaced Observe: %v", err)
	}
	data, err := json.Marshal(ns.Status.AtProvider)
	if err != nil {
		t.Fatal(err)
	}
	var nsAP v1alpha1.RepositoryObservation
	if err := json.Unmarshal(data, &nsAP); err != nil {
		t.Fatal(err)
	}
	out["Namespaced"] = observed{nsObs, nsAP}

	obs, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("cluster Observe: %v", err)
	}
	out["Cluster"] = observed{obs, cr.Status.AtProvider}
	return out
}

// A section that differs from the spec does not stop the sections after it
// from being read and mirrored: status.atProvider holds what GitHub has for
// all of them, the same as when nothing differs, and the repository is still
// reported out of date.
func TestObserveMirrorsLaterSectionsWhenAnEarlierOneDrifts(t *testing.T) {
	inSync := observeBoth(t, upToDateRepositories(nil), repository())["Cluster"]
	if !inSync.obs.ResourceUpToDate {
		t.Fatalf("baseline: Observe = %+v, want up to date", inSync.obs)
	}
	want := mirrorOf(inSync.ap)
	if len(want.Webhooks) == 0 || len(want.BranchProtectionRules) == 0 || len(want.RepositoryRules) == 0 {
		t.Fatalf("baseline mirror is empty: %+v", want)
	}

	cases := map[string]repositoryModifier{
		"ArchivedFlag":  withArchived(true),
		"Collaborators": withExtraUser("missing-user", "pull"),
		"Teams":         withTeamPermission(),
		"Webhooks":      withDifferentWebhookEvents(),
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			for scope, got := range observeBoth(t, upToDateRepositories(nil), repository(mod)) {
				t.Run(scope, func(t *testing.T) {
					if !got.obs.ResourceExists || got.obs.ResourceUpToDate {
						t.Fatalf("Observe = %+v, want exists and not up to date", got.obs)
					}
					if diff := cmp.Diff(want, mirrorOf(got.ap)); diff != "" {
						t.Errorf("atProvider: -in sync, +drifted:\n%s", diff)
					}
				})
			}
		})
	}
}

// A repository GitHub holds archived, with a spec that does not archive it, is
// out of date; the access it still lets Observe read is mirrored all the same.
func TestObserveMirrorsArchivedRepositoryTheSpecDoesNotArchive(t *testing.T) {
	for scope, got := range observeBoth(t, archivedRepositories(), repository()) {
		t.Run(scope, func(t *testing.T) {
			if !got.obs.ResourceExists || got.obs.ResourceUpToDate {
				t.Fatalf("Observe = %+v, want exists and not up to date", got.obs)
			}
			if len(got.ap.Permissions.Users) == 0 || len(got.ap.Permissions.Teams) == 0 {
				t.Errorf("permissions = %+v, want the users and teams GitHub has", got.ap.Permissions)
			}
			if got.ap.Archived == nil || !*got.ap.Archived {
				t.Errorf("archived = %v, want true", got.ap.Archived)
			}
		})
	}
}
