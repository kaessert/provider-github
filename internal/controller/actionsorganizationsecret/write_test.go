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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/clients/fake"
)

func requireWrote(t *testing.T, o *ghOrg, visibility string) github.SecretOrgRequest {
	t.Helper()
	if len(o.puts) == 0 {
		t.Fatal("no PUT reached GitHub")
	}
	put := o.puts[len(o.puts)-1]
	if put.KeyID == "" || put.EncryptedValue == "" || put.Visibility != visibility {
		t.Errorf("PUT = key_id %q, encrypted_value set: %t, visibility %q; want all three, visibility %q",
			put.KeyID, put.EncryptedValue != "", put.Visibility, visibility)
	}
	return put
}

// Create sends one PUT carrying the sealed value, the key id and the
// visibility; GitHub can open the box to the plaintext, and the plaintext is
// not in the request.
func TestCreateSealsTheValue(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			got, err := s.client.Create(context.Background(), s.cr)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if diff := cmp.Diff(managed.ExternalCreation{}, got); diff != "" {
				t.Errorf("Create: connection details -want, +got:\n%s", diff)
			}
			if len(o.puts) != 1 {
				t.Fatalf("PUTs = %d, want 1", len(o.puts))
			}
			put := requireWrote(t, o, "all")
			if put.KeyID != "key-1" {
				t.Errorf("key_id = %q, want the current key", put.KeyID)
			}
			if put.EncryptedValue == testValue || strings.Contains(put.EncryptedValue, testValue) {
				t.Error("the request carries the plaintext")
			}
			if diff := cmp.Diff([]string{testValue}, o.plaintexts); diff != "" {
				t.Errorf("value GitHub decrypted: -want, +got:\n%s", diff)
			}
		})
}

// Every write records what a later Observe compares: the Kubernetes Secret's
// identity and the updated_at read back after the write.
func TestWriteRecordsLastWrite(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			if _, err := s.client.Create(context.Background(), s.cr); err != nil {
				t.Fatalf("Create: %v", err)
			}
			want := &clusterv1alpha1.ActionsOrganizationSecretWriteRecord{
				SecretUID:             testSourceUID,
				SecretResourceVersion: "5",
				UpdatedAt:             o.secret.UpdatedAt.UTC().Format(time.RFC3339),
			}
			if diff := cmp.Diff(want, s.status(t).LastWrite); diff != "" {
				t.Errorf("status.lastWrite: -want, +got:\n%s", diff)
			}
			// The write settles the resource: the next Observe finds it current.
			requireObservation(t, s, upToDate)
		})
}

// After an Update the resource converges: its own write does not read as a
// remote change, and a further Observe writes nothing.
func TestUpdateConverges(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("private")(o)
		cr.Spec.ForProvider.Visibility = "all"
		cr.Status.LastWrite.SecretResourceVersion = "4"
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, notUpToDate)
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		requireWrote(t, o, "all")
		requireObservation(t, s, upToDate)
		requireObservation(t, s, upToDate)
		if len(o.puts) != 1 {
			t.Errorf("PUTs = %d after converging, want 1", len(o.puts))
		}
	})
}

// A visibility-only change still carries the value: GitHub answers 422 to a PUT
// without it, so the value is re-sealed and re-sent.
func TestUpdateVisibilityChangeResendsTheValue(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("all")(o)
		cr.Spec.ForProvider.Visibility = "private"
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		requireWrote(t, o, "private")
		if diff := cmp.Diff([]string{testValue}, o.plaintexts); diff != "" {
			t.Errorf("value GitHub decrypted: -want, +got:\n%s", diff)
		}
	})
}

// The public key is fetched for every write, never held across writes.
func TestEveryWriteFetchesThePublicKey(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			for i := 1; i <= 2; i++ {
				if _, err := s.client.Update(context.Background(), s.cr); err != nil {
					t.Fatalf("write %d: %v", i, err)
				}
				if o.keyFetches != i {
					t.Errorf("public key fetches after %d writes = %d, want %d", i, o.keyFetches, i)
				}
			}
		})
}

// GitHub rotates the key. A PUT naming the old one is rejected with a 422 and is
// retried once with a freshly fetched key.
func TestWriteRetriesOnceOnStaleKey(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			o.staleKeyID = "stale"
			if _, err := s.client.Create(context.Background(), s.cr); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if len(o.puts) != 2 || o.puts[0].KeyID != "stale" || o.puts[1].KeyID != "key-1" {
				t.Errorf("PUT key ids = %v, want the stale one then the current one", putKeys(o))
			}
			if o.keyFetches != 2 {
				t.Errorf("public key fetches = %d, want 2", o.keyFetches)
			}
			if o.secret == nil {
				t.Error("the retried write did not create the secret")
			}
		})
}

func putKeys(o *ghOrg) []string {
	ids := make([]string, 0, len(o.puts))
	for _, p := range o.puts {
		ids = append(ids, p.KeyID)
	}
	return ids
}

// A key that is stale twice in a row is not retried again.
func TestWriteStaleKeyIsRetriedOnlyOnce(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			o.putErr = staleKeyError()
			_, err := s.client.Create(context.Background(), s.cr)
			if !errors.Is(err, o.putErr) {
				t.Errorf("Create: error = %v, want GitHub's 422", err)
			}
			if len(o.puts) != 2 {
				t.Errorf("PUTs = %d, want exactly one retry", len(o.puts))
			}
		})
}

// Every other 422 is terminal: an invalid name is GitHub's error, not retried.
func TestWriteOtherRejectionsAreTerminal(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			o.putErr = unprocessable("Secret names can only contain alphanumeric characters ([a-z], [A-Z], [0-9]) or underscores (_).")
			_, err := s.client.Create(context.Background(), s.cr)
			if !errors.Is(err, o.putErr) {
				t.Errorf("Create: error = %v, want GitHub's 422", err)
			}
			if len(o.puts) != 1 || o.keyFetches != 1 {
				t.Errorf("PUTs = %d, key fetches = %d, want 1 and 1", len(o.puts), o.keyFetches)
			}
			if lw := s.status(t).LastWrite; lw != nil {
				t.Errorf("status.lastWrite = %+v after a rejected write, want none", lw)
			}
		})
}

// A rejection never carries the request body, so the sealed value cannot leave
// through an error.
func TestWriteErrorCarriesNoValue(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			o.putErr = &github.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusForbidden, Request: mustRequest(t)},
				Message:  "Resource not accessible by integration",
			}
			_, err := s.client.Create(context.Background(), s.cr)
			if err == nil {
				t.Fatal("Create: no error")
			}
			if strings.Contains(err.Error(), testValue) {
				t.Error("the error carries the plaintext")
			}
		})
}

func mustRequest(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPut, "https://api.github.com/orgs/acme/actions/secrets/MY_SECRET", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A public key that is not a base64 encoded 32 byte key fails before any PUT.
func TestWriteRejectsMalformedPublicKey(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			a := o.actions()
			a.MockGetOrgPublicKey = func(context.Context, string) (*github.PublicKey, *github.Response, error) {
				return &github.PublicKey{KeyID: github.Ptr("k"), Key: github.Ptr("AAAA")}, fake.GenerateEmptyResponse(), nil
			}
			s.ext.github.Actions = a
			if _, err := s.client.Create(context.Background(), s.cr); err == nil {
				t.Error("Create: no error for a malformed public key")
			}
			if len(o.puts) != 0 {
				t.Errorf("PUTs = %d, want none", len(o.puts))
			}
		})
}

// A write needs the value and the visibility; without either it fails before
// reaching GitHub.
func TestWriteRequiresValueAndVisibility(t *testing.T) {
	cases := map[string]modifier{
		"NoValueReference": withoutValueRef(),
		"NoVisibility":     withVisibility(""),
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR(m) }),
				func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
					if _, err := s.client.Create(context.Background(), s.cr); err == nil {
						t.Error("Create: no error")
					}
					if len(o.puts) != 0 {
						t.Errorf("PUTs = %d, want none", len(o.puts))
					}
				})
		})
	}
}

func TestWriteValueSecretProblems(t *testing.T) {
	t.Run("MissingSecret", func(t *testing.T) {
		forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
			func(t *testing.T, o *ghOrg, s scope, kube client.Client) {
				if err := kube.Delete(context.Background(), valueSecret(testValue, "5")); err != nil {
					t.Fatal(err)
				}
				if _, err := s.client.Create(context.Background(), s.cr); err == nil {
					t.Error("Create: no error for a missing Secret")
				}
				if len(o.puts) != 0 {
					t.Errorf("PUTs = %d, want none", len(o.puts))
				}
			})
	})
	t.Run("MissingKey", func(t *testing.T) {
		forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
			func(t *testing.T, o *ghOrg, s scope, kube client.Client) {
				ks := valueSecret(testValue, "5")
				ks.Data = map[string][]byte{"other": []byte("x")}
				if err := kube.Update(context.Background(), ks); err != nil {
					t.Fatal(err)
				}
				if _, err := s.client.Create(context.Background(), s.cr); err == nil {
					t.Error("Create: no error for a missing key")
				}
				if len(o.puts) != 0 {
					t.Errorf("PUTs = %d, want none", len(o.puts))
				}
			})
	})
}

// A namespaced resource reads the value from its own namespace whatever the
// selector names; a cluster-scoped one reads the namespace in the selector.
func TestValueSecretNamespace(t *testing.T) {
	ref := newCR().Spec.ForProvider.ValueSecretRef.DeepCopy()
	ref.Namespace = "tenant-b"
	kube := newKube(t, valueSecret(testValue, "5"))

	free := &external{kube: kube}
	if _, err := free.valueSecret(context.Background(), ref); err == nil {
		t.Error("a cluster-scoped resource read a Secret from the namespace in the selector, which holds none")
	}
	pinned := &external{kube: kube, secretNamespace: testNamespace}
	ks, err := pinned.valueSecret(context.Background(), ref)
	if err != nil {
		t.Fatalf("valueSecret: %v", err)
	}
	if ks.Namespace != testNamespace {
		t.Errorf("Secret namespace = %q, want %q", ks.Namespace, testNamespace)
	}
}

// A declared list is resolved to repository ids and sent.
func TestCreateSendsDeclaredRepositoryIDs(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret {
		return newCR(withVisibility("selected"), withRepos(testRepoB, testRepoA))
	}), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		if _, err := s.client.Create(context.Background(), s.cr); err != nil {
			t.Fatalf("Create: %v", err)
		}
		put := requireWrote(t, o, "selected")
		if diff := cmp.Diff([]int64{testRepoAID, testRepoBID}, sortedIDs(put.SelectedRepositoryIDs)); diff != "" {
			t.Errorf("selected_repository_ids: -want, +got:\n%s", diff)
		}
		if diff := cmp.Diff([]string{testRepoA, testRepoB}, o.selectedNames()); diff != "" {
			t.Errorf("list on GitHub: -want, +got:\n%s", diff)
		}
	})
}

// GitHub drops an unknown repository id without a word, so a declared name that
// is not a repository of the organization fails the write before the PUT.
func TestWriteMissingRepositoryFailsBeforeThePut(t *testing.T) {
	for name, existingSecret := range map[string]bool{"Create": false, "Update": true} {
		t.Run(name, func(t *testing.T) {
			forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret {
				return newCR(withVisibility("selected"), withRepos(testRepoA, "no-such-repo"))
			}), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
				if existingSecret {
					o.existing("selected", testRepoAID)
				}
				var err error
				if existingSecret {
					_, err = s.client.Update(context.Background(), s.cr)
				} else {
					_, err = s.client.Create(context.Background(), s.cr)
				}
				if err == nil || !strings.Contains(err.Error(), "no-such-repo") {
					t.Errorf("error = %v, want one naming the missing repository", err)
				}
				if len(o.puts) != 0 {
					t.Errorf("PUTs = %d, want none", len(o.puts))
				}
			})
		})
	}
}

// On Update only names the secret does not already list cost a repository
// lookup; the ids of the rest come from the list.
func TestUpdateResolvesOnlyAddedRepositories(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("selected", testRepoAID)(o)
		withRepos(testRepoA, testRepoC)(cr)
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		var looked []string
		repos := o.repositories()
		get := repos.MockGet
		repos.MockGet = func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
			looked = append(looked, repo)
			return get(ctx, owner, repo)
		}
		s.ext.github.Repositories = repos
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if diff := cmp.Diff([]string{testRepoC}, looked); diff != "" {
			t.Errorf("repositories looked up: -want, +got:\n%s", diff)
		}
		if diff := cmp.Diff([]string{testRepoA, testRepoC}, o.selectedNames()); diff != "" {
			t.Errorf("list on GitHub: -want, +got:\n%s", diff)
		}
	})
}

// A repository renamed on GitHub while the spec keeps the old name would never
// converge; the write fails instead.
func TestUpdateRenamedRepositoryFails(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("selected", testRepoAID)(o)
		withRepos("old-name")(cr)
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		// repo-a was renamed; GitHub still resolves the old name to the same id.
		delete(o.repos, testRepoA)
		o.repos["new-name"] = testRepoAID
		repos := o.repositories()
		repos.MockGet = func(context.Context, string, string) (*github.Repository, *github.Response, error) {
			return &github.Repository{ID: github.Ptr(testRepoAID), Name: github.Ptr("new-name")}, fake.GenerateEmptyResponse(), nil
		}
		s.ext.github.Repositories = repos
		if _, err := s.client.Update(context.Background(), s.cr); err == nil {
			t.Error("Update: no error for a repository that was renamed")
		}
		if len(o.puts) != 0 {
			t.Errorf("PUTs = %d, want none", len(o.puts))
		}
	})
}

// A PUT replaces the list. When the spec leaves selectedRepositories out, the
// list another resource owns is read and sent back so it is not wiped.
func TestUpdateOmittedListKeepsTheCurrentOne(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("selected", testRepoBID, testRepoAID)(o)
		cr.Status.LastWrite.SecretResourceVersion = "4"
		return cr
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		put := requireWrote(t, o, "selected")
		if diff := cmp.Diff([]int64{testRepoAID, testRepoBID}, sortedIDs(put.SelectedRepositoryIDs)); diff != "" {
			t.Errorf("selected_repository_ids: -want, +got:\n%s", diff)
		}
		if diff := cmp.Diff([]string{testRepoA, testRepoB}, o.selectedNames()); diff != "" {
			t.Errorf("list on GitHub: -want, +got:\n%s", diff)
		}
	})
}

// A new secret with no declared list starts with an empty one.
func TestCreateOmittedListStartsEmpty(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR(withVisibility("selected")) }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			if _, err := s.client.Create(context.Background(), s.cr); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if put := requireWrote(t, o, "selected"); len(put.SelectedRepositoryIDs) != 0 {
				t.Errorf("selected_repository_ids = %v, want none", put.SelectedRepositoryIDs)
			}
		})
}

// Ids are sent only under visibility selected.
func TestWriteSendsNoRepositoryIDsUnlessSelected(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		o.existing("selected", testRepoAID)
		return newCR(withVisibility("private"), withRepos(testRepoA))
	}, func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		if _, err := s.client.Update(context.Background(), s.cr); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if put := requireWrote(t, o, "private"); len(put.SelectedRepositoryIDs) != 0 {
			t.Errorf("selected_repository_ids = %v, want none", put.SelectedRepositoryIDs)
		}
		if o.listCalls != 0 {
			t.Errorf("repository list requested %d times, want 0", o.listCalls)
		}
	})
}

// org is identity: an Update writes to the organization in the spec and carries
// nothing of it in the body, and a differing org in the observation is never
// drift.
func TestOrgIsNotComparedOrSent(t *testing.T) {
	forEachScope(t, func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
		cr := existing("all")(o)
		cr.Status.AtProvider.Org = "previous-org"
		return cr
	}, func(t *testing.T, _ *ghOrg, s scope, _ client.Client) {
		requireObservation(t, s, upToDate)
	})
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			var org string
			a := o.actions()
			put := a.MockCreateOrUpdateOrgSecret
			a.MockCreateOrUpdateOrgSecret = func(ctx context.Context, got, name string, req github.SecretOrgRequest) (*github.Response, error) {
				org = got
				return put(ctx, got, name, req)
			}
			s.ext.github.Actions = a
			if _, err := s.client.Update(context.Background(), s.cr); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if org != testOrg {
				t.Errorf("PUT addressed org %q, want %q", org, testOrg)
			}
		})
}

func TestDeleteSuccess(t *testing.T) {
	forEachScope(t, existing("all"), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		got, err := s.client.Delete(context.Background(), s.cr)
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if diff := cmp.Diff(managed.ExternalDelete{}, got); diff != "" {
			t.Errorf("Delete: -want, +got:\n%s", diff)
		}
		if o.deletes != 1 || o.secret != nil {
			t.Errorf("deletes = %d, secret gone: %t; want 1 and true", o.deletes, o.secret == nil)
		}
	})
}

func TestDeleteNotFound(t *testing.T) {
	forEachScope(t, fixed(func() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }),
		func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
			if _, err := s.client.Delete(context.Background(), s.cr); err != nil {
				t.Errorf("Delete: error = %v, want none for a secret that is already gone", err)
			}
			if o.deletes != 1 {
				t.Errorf("deletes = %d, want 1", o.deletes)
			}
		})
}

func TestDeleteServerError(t *testing.T) {
	boom := &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusInternalServerError}, Message: "server error"}
	forEachScope(t, existing("all"), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
		o.deleteErr = boom
		_, err := s.client.Delete(context.Background(), s.cr)
		if !errors.Is(err, boom) {
			t.Errorf("Delete: error = %v, want the GitHub error", err)
		}
		if o.secret == nil {
			t.Error("the secret is gone although the delete failed")
		}
	})
}
