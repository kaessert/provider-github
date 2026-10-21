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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	ddapi "github.com/provider-github/provider-github/apis/common/driftdetection"
	"github.com/provider-github/provider-github/internal/driftdetection"
)

// The value cannot be read back, so drift in it is read from two records of
// the provider's last write: the Kubernetes Secret's identity and GitHub's
// updated_at.
func TestObserveValueDrift(t *testing.T) {
	cases := map[string]struct {
		cr   func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret
		want managed.ExternalObservation
	}{
		"RecordsMatch": {
			cr:   existing("all"),
			want: upToDate,
		},
		"NoRecordYet": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				o.existing("all")
				return newCR()
			},
			want: notUpToDate,
		},
		"IncompleteRecord": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("all")(o)
				cr.Status.LastWrite.SecretUID = ""
				return cr
			},
			want: notUpToDate,
		},
		"UnreadableRecord": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("all")(o)
				cr.Status.LastWrite.UpdatedAt = "not a time"
				return cr
			},
			want: notUpToDate,
		},
		"SecretEdited": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("all")(o)
				cr.Status.LastWrite.SecretResourceVersion = "4"
				return cr
			},
			want: notUpToDate,
		},
		"SecretReplaced": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("all")(o)
				cr.Status.LastWrite.SecretUID = "uid-of-a-deleted-secret"
				return cr
			},
			want: notUpToDate,
		},
		"WrittenByAnotherParty": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("all")(o)
				o.secret.UpdatedAt.Time = o.secret.UpdatedAt.Add(time.Minute)
				return cr
			},
			want: notUpToDate,
		},
		"RecordAheadOfGitHub": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("all")(o)
				o.secret.UpdatedAt.Time = o.secret.UpdatedAt.Add(-time.Minute)
				return cr
			},
			want: upToDate,
		},
		"NoValueReferenceUnderWritingPolicy": {
			cr: func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				cr := existing("all")(o)
				withoutValueRef()(cr)
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

// A Kubernetes Secret that cannot be read stops the reconcile with an error: an
// up-to-date verdict cannot be given without it.
func TestObserveValueSecretMissing(t *testing.T) {
	forEachScope(t, existing("all"), func(t *testing.T, _ *ghOrg, s scope, kube client.Client) {
		if err := kube.Delete(context.Background(), valueSecret(testValue, "5")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.client.Observe(context.Background(), s.cr); err == nil {
			t.Error("Observe: no error although the referenced Secret does not exist")
		}
	})
}

// Under a policy that does not write there is no value to keep current: the
// records are not consulted and the Kubernetes Secret is not read.
func TestObserveNonWritingPolicySkipsValue(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.existing("all")
		return newCR(withPolicies(xpv2.ManagementActionObserve, xpv2.ManagementActionDelete))
	}, func(t *testing.T, _ *ghOrg, s scope, kube client.Client) {
		if err := kube.Delete(context.Background(), valueSecret(testValue, "5")); err != nil {
			t.Fatal(err)
		}
		requireObservation(t, s, upToDate)
	})
}

// With drift detection off or in warn mode the decorator reports the resource
// up to date, so the reconciler never calls Update: nothing is written.
func TestDriftDetectionModesNeverWriteTheValue(t *testing.T) {
	for _, mode := range []driftdetection.Mode{driftdetection.ModeWarn, driftdetection.ModeDisabled} {
		t.Run(string(mode), func(t *testing.T) {
			forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				o.existing("all")
				cr := newCR() // no record: not up to date under mode enabled
				cr.Spec.DriftDetection = &ddapi.DriftDetection{Mode: ddapi.Mode(mode)}
				return cr
			}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
				got, err := driftdetection.WrapClient(s.client).Observe(context.Background(), s.cr)
				if err != nil {
					t.Fatalf("Observe: %v", err)
				}
				if !got.ResourceExists || !got.ResourceUpToDate {
					t.Errorf("Observe = %+v, want exists and up to date so no write follows", got)
				}
				if len(o.puts) != 0 {
					t.Errorf("%d writes under drift detection mode %s, want none", len(o.puts), mode)
				}
			})
		})
	}
}

// Nothing derived from the value is kept anywhere on the resource.
func TestStatusHoldsNoValueMaterial(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			if _, err := s.client.Create(context.Background(), s.cr); err != nil {
				t.Fatalf("Create: %v", err)
			}
			raw, err := json.Marshal(s.cr)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), testValue) {
				t.Error("the resource holds the plaintext value")
			}
			for _, p := range o.puts {
				if strings.Contains(string(raw), p.EncryptedValue) {
					t.Error("the resource holds the sealed value")
				}
			}
		})
}
