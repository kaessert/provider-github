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
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
)

var (
	upToDate    = managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}
	notUpToDate = managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: false}
)

type crFunc = func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret

// existing is a secret last written by the provider and untouched since: its
// records match the Kubernetes Secret as the fixture holds it (resourceVersion 5).
func existing(visibility string, ids ...int64) crFunc {
	return func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.existing(visibility, ids...)
		return newCR(withVisibility(visibility), withLastWrite("5", o.secret.UpdatedAt.Time))
	}
}

func requireObservation(t *testing.T, s scope, want managed.ExternalObservation) {
	t.Helper()
	got, err := s.client.Observe(context.Background(), s.cr)
	if err != nil {
		t.Fatalf("Observe: unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Observe: -want, +got:\n%s", diff)
	}
}

func requireReady(t *testing.T, s scope, status corev1.ConditionStatus, reason xpv2.ConditionReason) {
	t.Helper()
	if c := s.cond(); c.Status != status || c.Reason != reason {
		t.Errorf("Ready = %s/%s, want %s/%s", c.Status, c.Reason, status, reason)
	}
}

// A secret that does not exist is reported absent, not an error, so the
// reconciler goes on to Create. The external name is the CR's own, so there is
// no guard ahead of the lookup: absence is GitHub's 404.
func TestObserveNotFound(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, _ *ghOrg, s scope, _ client.Client) {
			requireObservation(t, s, managed.ExternalObservation{ResourceExists: false})
		})
}

func TestObserveServerError(t *testing.T) {
	boom := &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusInternalServerError}, Message: "server error"}
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			o.getErr = boom
			got, err := s.client.Observe(context.Background(), s.cr)
			if !errors.Is(err, boom) {
				t.Errorf("Observe: error = %v, want the GitHub error", err)
			}
			if got.ResourceExists {
				t.Error("Observe: ResourceExists = true on a server error")
			}
			if c := s.cond(); c.Status == corev1.ConditionTrue {
				t.Errorf("Ready = %s after a failed Observe, want no Available condition", c.Status)
			}
		})
}

func TestObserveUpToDate(t *testing.T) {
	forEachScope(t, existing("all"), func(t *testing.T, _ *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, upToDate)
		requireReady(t, s, corev1.ConditionTrue, xpv2.ReasonAvailable)
	})
}

// GitHub upper-cases the name. The case difference is not drift, and the
// external name is not rewritten to chase it.
func TestObserveNameCaseIsNotDrift(t *testing.T) {
	forEachScope(t, existing("all"), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		if o.secret.Name == testName {
			t.Fatal("fixture: GitHub's name must differ from the external name in case")
		}
		requireObservation(t, s, upToDate)
		if got := s.status(t).AtProvider; got.ID != testName || got.Name != testGHName {
			t.Errorf("atProvider id/name = %q/%q, want %q/%q", got.ID, got.Name, testName, testGHName)
		}
		if got := s.cr.GetAnnotations()["crossplane.io/external-name"]; got != testName {
			t.Errorf("external name = %q, want it left at %q", got, testName)
		}
	})
}

func TestObserveVisibilityDrift(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("private")(o)
		cr.Spec.ForProvider.Visibility = "all"
		return cr
	}, func(t *testing.T, _ *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, notUpToDate)
		requireReady(t, s, corev1.ConditionFalse, xpv2.ReasonUnavailable)
	})
}

// A declared list is compared case-insensitively and in any order, and is
// compared only while both the spec and GitHub say selected.
func TestObserveSelectedRepositories(t *testing.T) {
	cases := map[string]struct {
		cr   crFunc
		want managed.ExternalObservation
	}{
		"SameInAnyOrderAndCase": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("selected", testRepoAID, testRepoBID)(o)
				withRepos("REPO-B", testRepoA)(cr)
				return cr
			},
			want: upToDate,
		},
		"Differs": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("selected", testRepoAID, testRepoBID)(o)
				withRepos(testRepoA)(cr)
				return cr
			},
			want: notUpToDate,
		},
		"OmittedListIsNotCompared": {
			cr:   existing("selected", testRepoAID),
			want: upToDate,
		},
		"DeclaredEmptyListIsCompared": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("selected", testRepoAID)(o)
				withRepos()(cr)
				return cr
			},
			want: notUpToDate,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			forEachScope(t, tc.cr, func(t *testing.T, _ *ghOrg, s scope, _ client.Client) {
				requireObservation(t, s, tc.want)
			})
		})
	}
}

// An Observe-only import declares neither value nor visibility: nothing is
// compared, nothing needs the Kubernetes Secret, and the secret is mirrored.
func TestObserveOnlyComparesNothing(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.existing("private")
		return newCR(withPolicies(xpv2.ManagementActionObserve), withoutValueRef(), withVisibility(""))
	}, func(t *testing.T, o *ghOrg, s scope, kube client.Client) {
		if err := kube.Delete(context.Background(), valueSecret(testValue, "5")); err != nil {
			t.Fatal(err)
		}
		requireObservation(t, s, upToDate)
		requireReady(t, s, corev1.ConditionTrue, xpv2.ReasonAvailable)
		if got := s.status(t).AtProvider.Visibility; got != "private" {
			t.Errorf("atProvider.visibility = %q, want private", got)
		}
	})
}

// A resource being deleted must reach Delete even when the Kubernetes Secret
// that held the value is already gone.
func TestObserveDeletingNeedsNoValue(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.existing("all")
		cr := newCR()
		now := metav1.Now()
		cr.SetDeletionTimestamp(&now)
		return cr
	}, func(t *testing.T, _ *ghOrg, s scope, kube client.Client) {
		if err := kube.Delete(context.Background(), valueSecret(testValue, "5")); err != nil {
			t.Fatal(err)
		}
		requireObservation(t, s, upToDate)
	})
}

// The mirror holds everything GitHub reports and nothing of the value.
func TestObservePopulatesAtProvider(t *testing.T) {
	forEachScope(t, existing("selected", testRepoBID, testRepoAID), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, upToDate)
		want := clusterv1alpha1.ActionsOrganizationSecretObservation{
			ID:                   testName,
			Org:                  testOrg,
			Name:                 testGHName,
			Visibility:           "selected",
			SelectedRepositories: []clusterv1alpha1.SecretSelectedRepoObservation{{Repo: testRepoA}, {Repo: testRepoB}},
			CreatedAt:            o.secret.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:            o.secret.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if diff := cmp.Diff(want, s.status(t).AtProvider); diff != "" {
			t.Errorf("atProvider: -want, +got:\n%s", diff)
		}
	})
}

// The list is read only while GitHub reports selected, and a list mirrored
// earlier is dropped once it no longer applies.
func TestObserveMirrorsNoListUnlessSelected(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("private")(o)
		cr.Status.AtProvider.SelectedRepositories = []clusterv1alpha1.SecretSelectedRepoObservation{{Repo: testRepoA}}
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, upToDate)
		if got := s.status(t).AtProvider.SelectedRepositories; len(got) != 0 {
			t.Errorf("atProvider.selectedRepositories = %v, want none while visibility is private", got)
		}
		if o.listCalls != 0 {
			t.Errorf("repository list requested %d times for a private secret, want 0", o.listCalls)
		}
	})
}

// A response carrying only the name and nothing else is a valid answer: zero
// timestamps and an empty visibility must not panic or error.
func TestObserveMinimalResponse(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.secret = &github.Secret{Name: testGHName}
		return newCR(withVisibility(""), withPolicies(xpv2.ManagementActionObserve), withoutValueRef())
	}, func(t *testing.T, _ *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, upToDate)
		if got := s.status(t).AtProvider; got.CreatedAt != "" || got.UpdatedAt != "" || got.Visibility != "" || got.Name != testGHName {
			t.Errorf("atProvider = %+v, want only the name mirrored", got)
		}
	})
}
