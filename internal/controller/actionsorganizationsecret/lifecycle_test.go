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
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/provider-github/provider-github/apis/namespaced/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/clients/fake"
	"github.com/provider-github/provider-github/internal/controller/scopebridge"
	"github.com/provider-github/provider-github/internal/driftdetection/ddtest"
)

// requireNoValueMaterial fails when the plaintext or any ciphertext GitHub was
// sent appears on the resource (spec, status, annotations, labels, conditions),
// in an error, or in connection details.
func requireNoValueMaterial(t *testing.T, o *ghOrg, s scope, err error, details map[string][]byte) {
	t.Helper()
	secrets := []string{testValue}
	for _, p := range o.puts {
		if p.EncryptedValue != "" {
			secrets = append(secrets, p.EncryptedValue)
		}
	}
	raw, jerr := json.Marshal(s.cr)
	if jerr != nil {
		t.Fatal(jerr)
	}
	surfaces := map[string]string{"resource": string(raw)}
	if err != nil {
		surfaces["error"] = err.Error()
	}
	for k, v := range details {
		surfaces["connection detail "+k] = string(v)
	}
	for where, text := range surfaces {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s holds value material %q", where, secret)
			}
		}
	}
	if len(details) != 0 {
		t.Errorf("connection details = %v, want none: GitHub returns nothing secret", details)
	}
}

// Neither the plaintext nor the sealed box is left on the resource, in an error
// or in connection details, on any path through a write.
func TestNoValueMaterialLeaks(t *testing.T) {
	forbidden := rejection(http.StatusForbidden, "Resource not accessible by integration")
	cases := map[string]struct {
		setup   crFunc
		arrange func(o *ghOrg)
		update  bool
	}{
		"Create": {setup: fixed(newCRNoMods)},
		"Update": {setup: existing("private"), update: true},
		"CreateAfterStaleKey": {
			setup:   fixed(newCRNoMods),
			arrange: func(o *ghOrg) { o.staleKeyID = "stale" },
		},
		"RejectedByGitHub": {
			setup:   fixed(newCRNoMods),
			arrange: func(o *ghOrg) { o.putErr = forbidden },
		},
		"StaleTwice": {
			setup:   fixed(newCRNoMods),
			arrange: func(o *ghOrg) { o.putErr = staleKeyError() },
		},
		"MissingRepository": {
			setup: fixed(func() *clusterv1alpha1.ActionsOrganizationSecret {
				return newCR(withVisibility("selected"), withRepos("no-such-repo"))
			}),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			forEachScope(t, tc.setup, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
				if tc.arrange != nil {
					tc.arrange(o)
				}
				var (
					err     error
					details map[string][]byte
				)
				if tc.update {
					var got managed.ExternalUpdate
					got, err = s.client.Update(context.Background(), s.cr)
					details = got.ConnectionDetails
				} else {
					var got managed.ExternalCreation
					got, err = s.client.Create(context.Background(), s.cr)
					details = got.ConnectionDetails
				}
				requireNoValueMaterial(t, o, s, err, details)
				// A later Observe leaves nothing behind either.
				if _, oerr := s.client.Observe(context.Background(), s.cr); oerr != nil {
					requireNoValueMaterial(t, o, s, oerr, nil)
				} else {
					requireNoValueMaterial(t, o, s, nil, nil)
				}
			})
		})
	}
}

// The value must also not travel in the annotations the reconciler maintains.
func TestNoValueMaterialInAnnotations(t *testing.T) {
	forEachScope(t, fixed(newCRNoMods), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		if _, err := s.client.Create(context.Background(), s.cr); err != nil {
			t.Fatal(err)
		}
		for k, v := range s.cr.GetAnnotations() {
			if strings.Contains(k+v, testValue) || strings.Contains(k+v, o.puts[0].EncryptedValue) {
				t.Errorf("annotation %s holds value material", k)
			}
		}
		if got := meta.GetExternalName(s.cr); got != testName {
			t.Errorf("external name = %q, want it left at %q", got, testName)
		}
	})
}

// A resource with no records is out of date once; the write that follows
// records the Kubernetes Secret's identity and GitHub's own updated_at, and the
// resource is then current and stays so.
func TestMissingRecordsAreOutOfDateOnce(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.existing("all")
		return newCR()
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		if s.status(t).LastWrite != nil {
			t.Fatal("fixture: the resource must start without records")
		}
		requireObservation(t, s, notUpToDate)
		if len(o.puts) != 0 {
			t.Fatalf("Observe wrote %d times, want 0", len(o.puts))
		}
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		lw := s.status(t).LastWrite
		if lw == nil || lw.SecretUID != testSourceUID || lw.SecretResourceVersion != "5" || lw.UpdatedAt == "" {
			t.Fatalf("status.lastWrite = %+v after the write, want all three records", lw)
		}
		requireObservation(t, s, upToDate)
		requireObservation(t, s, upToDate)
		if len(o.puts) != 1 {
			t.Errorf("PUTs = %d, want exactly the one write", len(o.puts))
		}
	})
}

// Someone else writing the secret moves updated_at past the record; the next
// write takes the secret back and the resource converges.
func TestRemoteWriteIsCorrectedOnce(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("all")(o)
		o.secret.UpdatedAt = o.tick()
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, notUpToDate)
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		requireObservation(t, s, upToDate)
		if diff := cmp.Diff([]string{testValue}, o.plaintexts); diff != "" {
			t.Errorf("value GitHub holds: -want, +got:\n%s", diff)
		}
	})
}

// If the read-back after the PUT fails, the write is reported failed, no stale
// record is kept, and the next Observe finds the resource out of date so the
// write is made again.
func TestWriteReadBackFailureLeavesNoRecord(t *testing.T) {
	forEachScope(t, existing("private"), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		a := o.actions()
		get := a.MockGetOrgSecret
		failing := true
		a.MockGetOrgSecret = func(ctx context.Context, org, name string) (*github.Secret, *github.Response, error) {
			if failing {
				return nil, fake.GenerateEmptyResponse(), rejection(http.StatusInternalServerError, "read-back failed")
			}
			return get(ctx, org, name)
		}
		s.ext.github.Actions = a

		if _, err := s.client.Update(context.Background(), s.cr); err == nil {
			t.Fatal("Update: no error although the read-back failed")
		}
		if len(o.puts) != 1 {
			t.Errorf("PUTs = %d, want 1", len(o.puts))
		}
		if lw := s.status(t).LastWrite; lw != nil {
			t.Errorf("status.lastWrite = %+v, want none after a failed read-back", lw)
		}
		failing = false
		requireObservation(t, s, notUpToDate)
	})
}

// Under an Observe-only policy nothing is written and the value is not
// consulted, whatever the records say.
func TestObserveOnlyPolicyNeverWrites(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.existing("all")
		return newCR(withPolicies(xpv2.ManagementActionObserve))
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, upToDate)
		if len(o.puts) != 0 || o.keyFetches != 0 {
			t.Errorf("PUTs = %d, key fetches = %d under Observe-only, want 0 and 0", len(o.puts), o.keyFetches)
		}
	})
}

// Management policy sets that do not declare writes never read the value's
// Kubernetes Secret, and nothing is sent to GitHub.
func TestNonWritingPoliciesNeverConsultTheValue(t *testing.T) {
	cases := map[string][]xpv2.ManagementAction{
		"ObserveDelete":     {xpv2.ManagementActionObserve, xpv2.ManagementActionDelete},
		"ObserveLateInit":   {xpv2.ManagementActionObserve, xpv2.ManagementActionLateInitialize},
		"ObserveOnlyAlone":  {xpv2.ManagementActionObserve},
		"ObserveDeleteLate": {xpv2.ManagementActionObserve, xpv2.ManagementActionDelete, xpv2.ManagementActionLateInitialize},
	}
	for name, policies := range cases {
		t.Run(name, func(t *testing.T) {
			forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				o.existing("all")
				return newCR(withPolicies(policies...))
			}, func(t *testing.T, o *ghOrg, s scope, kube client.Client) {
				if err := kube.Delete(context.Background(), valueSecret(testValue, "5")); err != nil {
					t.Fatal(err)
				}
				requireObservation(t, s, upToDate)
				if len(o.puts) != 0 {
					t.Errorf("PUTs = %d, want 0", len(o.puts))
				}
			})
		})
	}
}

// A failing list of the selected repositories stops Observe, and stops a write
// before the PUT, since the PUT would otherwise replace a list that was never read.
func TestSelectedListReadFailures(t *testing.T) {
	boom := rejection(http.StatusInternalServerError, "list failed")
	failList := func(o *ghOrg, s scope) {
		a := o.actions()
		a.MockListSelectedReposForOrgSecret = func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			return nil, fake.GenerateEmptyResponse(), boom
		}
		s.ext.github.Actions = a
	}
	t.Run("Observe", func(t *testing.T) {
		forEachScope(t, existing("selected", testRepoAID), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			failList(o, s)
			if _, err := s.client.Observe(context.Background(), s.cr); err == nil {
				t.Error("Observe: no error although the list could not be read")
			}
		})
	})
	for name, m := range map[string]modifier{"OmittedList": withVisibility("selected"), "DeclaredList": func(cr *clusterv1alpha1.ActionsOrganizationSecret) {
		withVisibility("selected")(cr)
		withRepos(testRepoA)(cr)
	}} {
		t.Run("Write/"+name, func(t *testing.T) {
			forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
				o.existing("selected", testRepoAID)
				return newCR(m)
			}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
				failList(o, s)
				if _, err := s.client.Update(context.Background(), s.cr); err == nil {
					t.Error("Update: no error although the list could not be read")
				}
				if len(o.puts) != 0 {
					t.Errorf("PUTs = %d, want 0", len(o.puts))
				}
			})
		})
	}
}

// A declared but empty list is the owner's statement that no repository may use
// the secret: it is sent as no ids rather than replaced by GitHub's list.
func TestUpdateDeclaredEmptyListClearsIt(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("selected", testRepoAID)(o)
		withRepos()(cr)
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, notUpToDate)
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if put := requireWrote(t, o, "selected"); len(put.SelectedRepositoryIDs) != 0 {
			t.Errorf("selected_repository_ids = %v, want none", put.SelectedRepositoryIDs)
		}
		requireObservation(t, s, upToDate)
	})
}

// A declared list that is already in effect is resent as it is and the write
// converges with no repository lookups.
func TestUpdateKnownListNeedsNoLookups(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("selected", testRepoAID, testRepoBID)(o)
		cr.Status.LastWrite.SecretResourceVersion = "4"
		withRepos(testRepoB, testRepoA)(cr)
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		repos := o.repositories()
		repos.MockGet = func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
			t.Errorf("repository %q looked up although the secret already lists it", repo)
			return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
		}
		s.ext.github.Repositories = repos
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		put := requireWrote(t, o, "selected")
		if diff := cmp.Diff([]int64{testRepoAID, testRepoBID}, sortedIDs(put.SelectedRepositoryIDs)); diff != "" {
			t.Errorf("selected_repository_ids: -want, +got:\n%s", diff)
		}
		requireObservation(t, s, upToDate)
	})
}

// Delete needs neither the value nor its Kubernetes Secret.
func TestDeleteNeedsNoValue(t *testing.T) {
	forEachScope(t, existing("all"), func(t *testing.T, o *ghOrg, s scope, kube client.Client) {
		if err := kube.Delete(context.Background(), valueSecret(testValue, "5")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.client.Delete(context.Background(), s.cr); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if o.keyFetches != 0 || len(o.puts) != 0 {
			t.Errorf("key fetches = %d, PUTs = %d on Delete, want 0 and 0", o.keyFetches, len(o.puts))
		}
	})
}

// A namespaced resource reads valueSecretRef from its own namespace, whatever
// namespace the selector names, and never from the one it names.
func TestNamespacedReadsTheValueFromItsOwnNamespace(t *testing.T) {
	o := newOrg(t)
	cluster := newCR()
	cluster.Spec.ForProvider.ValueSecretRef.Namespace = "elsewhere"
	ns := ddtest.Convert(t, cluster, &namespacedv1alpha1.ActionsOrganizationSecret{})
	ns.SetNamespace(testNamespace)

	// The same-named Secret in the other namespace holds a different value.
	other := valueSecret("the-wrong-value", "9")
	other.Namespace = "elsewhere"
	kube := newKube(t, valueSecret(testValue, "5"), other)

	e := o.external(kube)
	e.secretNamespace = ns.GetNamespace()
	bridge := scopebridge.New[*namespacedv1alpha1.ActionsOrganizationSecret](e,
		func() *clusterv1alpha1.ActionsOrganizationSecret { return &clusterv1alpha1.ActionsOrganizationSecret{} })

	if _, err := bridge.Create(context.Background(), ns); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if diff := cmp.Diff([]string{testValue}, o.plaintexts); diff != "" {
		t.Errorf("value GitHub decrypted: -want, +got:\n%s", diff)
	}
	if got := ddtest.Convert(t, ns, &clusterv1alpha1.ActionsOrganizationSecret{}).Status.LastWrite; got == nil || got.SecretResourceVersion != "5" {
		t.Errorf("status.lastWrite = %+v, want the record of the Secret in the resource's own namespace", got)
	}
}
