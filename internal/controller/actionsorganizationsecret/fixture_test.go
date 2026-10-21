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
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v90/github"
	"golang.org/x/crypto/nacl/box"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/provider-github/provider-github/apis/namespaced/organizations/v1alpha1"
	ghclient "github.com/provider-github/provider-github/internal/clients"
	"github.com/provider-github/provider-github/internal/clients/fake"
	"github.com/provider-github/provider-github/internal/controller/scopebridge"
	"github.com/provider-github/provider-github/internal/driftdetection/ddtest"
)

const (
	testOrg        = "acme"
	testName       = "my_secret"
	testGHName     = "MY_SECRET"
	testValue      = "s3cret-value-do-not-leak"
	testNamespace  = "tenant-a"
	testSourceName = "value-source"
	testSourceKey  = "value"
	testSourceUID  = "uid-1"

	testRepoA         = "repo-a"
	testRepoB         = "repo-b"
	testRepoC         = "repo-c"
	testRepoAID int64 = 1001
	testRepoBID int64 = 1002
	testRepoCID int64 = 1003
)

var testBase = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// ghOrg is a stateful stand-in for the GitHub organization the controller
// talks to. It models what the controller depends on: a PUT replaces the
// secret whole, needs the sealed value, key id and visibility, and drops
// repository ids it does not know; updated_at advances on every accepted PUT.
type ghOrg struct {
	t *testing.T

	// secret is nil while the secret does not exist.
	secret   *github.Secret
	selected []int64

	// repos is every repository of the organization, by name.
	repos map[string]int64

	pub   *[32]byte
	priv  *[32]byte
	keyID string
	// staleKeyID, when set, is served by the next public key request while
	// keyID stays the current one.
	staleKeyID string

	clock time.Time

	// calls
	puts       []github.SecretOrgRequest
	keyFetches int
	deletes    int
	getSecrets int
	listCalls  int
	// plaintexts holds what each accepted PUT carried, opened with the private key.
	plaintexts []string
	// putErr, when set, answers every PUT.
	putErr error
	// getErr, when set, answers every secret lookup.
	getErr error
	// deleteErr, when set, answers every delete.
	deleteErr error
}

func newOrg(t *testing.T) *ghOrg {
	t.Helper()
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &ghOrg{
		t:     t,
		pub:   pub,
		priv:  priv,
		keyID: "key-1",
		clock: testBase,
		repos: map[string]int64{testRepoA: testRepoAID, testRepoB: testRepoBID, testRepoC: testRepoCID},
	}
}

func (o *ghOrg) tick() github.Timestamp {
	o.clock = o.clock.Add(2 * time.Second)
	return github.Timestamp{Time: o.clock}
}

// existing makes the secret exist with the given visibility and repository ids.
func (o *ghOrg) existing(visibility string, ids ...int64) *ghOrg {
	now := o.tick()
	o.secret = &github.Secret{Name: testGHName, CreatedAt: now, UpdatedAt: now, Visibility: visibility}
	o.selected = ids
	return o
}

func (o *ghOrg) repoName(id int64) (string, bool) {
	for n, i := range o.repos {
		if i == id {
			return n, true
		}
	}
	return "", false
}

func (o *ghOrg) selectedNames() []string {
	var out []string
	for _, id := range o.selected {
		if n, ok := o.repoName(id); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func staleKeyError() error {
	return &github.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
		Message:  "Provided key stale is not the latest version available. Call `GET /secrets/public-key` and resign the data using the latest key.",
	}
}

func unprocessable(msg string) error {
	return &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}, Message: msg}
}

func (o *ghOrg) actions() *fake.MockActionsClient {
	return &fake.MockActionsClient{
		MockGetOrgPublicKey: func(context.Context, string) (*github.PublicKey, *github.Response, error) {
			o.keyFetches++
			id := o.keyID
			if o.staleKeyID != "" {
				id, o.staleKeyID = o.staleKeyID, ""
			}
			return &github.PublicKey{KeyID: github.Ptr(id), Key: github.Ptr(base64.StdEncoding.EncodeToString(o.pub[:]))}, fake.GenerateEmptyResponse(), nil
		},
		MockGetOrgSecret: func(context.Context, string, string) (*github.Secret, *github.Response, error) {
			o.getSecrets++
			if o.getErr != nil {
				return nil, fake.GenerateEmptyResponse(), o.getErr
			}
			if o.secret == nil {
				return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
			}
			s := *o.secret
			return &s, fake.GenerateEmptyResponse(), nil
		},
		MockListSelectedReposForOrgSecret: func(context.Context, string, string, *github.ListOptions) (*github.SelectedReposList, *github.Response, error) {
			o.listCalls++
			if o.secret == nil {
				return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
			}
			list := &github.SelectedReposList{}
			for _, id := range o.selected {
				if n, ok := o.repoName(id); ok {
					list.Repositories = append(list.Repositories, &github.Repository{ID: github.Ptr(id), Name: github.Ptr(n)})
				}
			}
			return list, fake.GenerateEmptyResponse(), nil
		},
		MockCreateOrUpdateOrgSecret: func(_ context.Context, _, name string, req github.SecretOrgRequest) (*github.Response, error) {
			o.puts = append(o.puts, req)
			if o.putErr != nil {
				return fake.GenerateEmptyResponse(), o.putErr
			}
			if req.KeyID == "" || req.EncryptedValue == "" || req.Visibility == "" {
				return fake.GenerateEmptyResponse(), unprocessable("required field missing")
			}
			if req.KeyID != o.keyID {
				return fake.GenerateEmptyResponse(), staleKeyError()
			}
			sealed, err := base64.StdEncoding.DecodeString(req.EncryptedValue)
			if err != nil {
				return fake.GenerateEmptyResponse(), unprocessable("encrypted_value is not base64")
			}
			plain, ok := box.OpenAnonymous(nil, sealed, o.pub, o.priv)
			if !ok {
				return fake.GenerateEmptyResponse(), unprocessable("encrypted_value cannot be decrypted")
			}
			o.plaintexts = append(o.plaintexts, string(plain))

			now := o.tick()
			created := now
			if o.secret != nil {
				created = o.secret.CreatedAt
			}
			o.secret = &github.Secret{Name: strings.ToUpper(name), CreatedAt: created, UpdatedAt: now, Visibility: req.Visibility}
			o.selected = nil
			if req.Visibility == "selected" {
				for _, id := range req.SelectedRepositoryIDs {
					if _, known := o.repoName(id); known {
						o.selected = append(o.selected, id)
					}
				}
			}
			return fake.GenerateEmptyResponse(), nil
		},
		MockDeleteOrgSecret: func(context.Context, string, string) (*github.Response, error) {
			o.deletes++
			if o.deleteErr != nil {
				return fake.GenerateEmptyResponse(), o.deleteErr
			}
			if o.secret == nil {
				return fake.GenerateEmptyResponse(), fake.Generate404Response()
			}
			o.secret, o.selected = nil, nil
			return fake.GenerateEmptyResponse(), nil
		},
	}
}

func (o *ghOrg) repositories() *fake.MockRepositoriesClient {
	return &fake.MockRepositoriesClient{
		MockGet: func(_ context.Context, _, repo string) (*github.Repository, *github.Response, error) {
			id, ok := o.repos[repo]
			if !ok {
				return nil, fake.GenerateEmptyResponse(), fake.Generate404Response()
			}
			return &github.Repository{ID: github.Ptr(id), Name: github.Ptr(repo)}, fake.GenerateEmptyResponse(), nil
		},
	}
}

// valueSecret is the Kubernetes Secret that valueSecretRef points at.
func valueSecret(value, resourceVersion string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSourceName, Namespace: testNamespace, UID: types.UID(testSourceUID), ResourceVersion: resourceVersion},
		Data:       map[string][]byte{testSourceKey: []byte(value)},
	}
}

func newKube(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

type modifier func(*clusterv1alpha1.ActionsOrganizationSecret)

func withVisibility(v string) modifier {
	return func(cr *clusterv1alpha1.ActionsOrganizationSecret) { cr.Spec.ForProvider.Visibility = v }
}

func withRepos(names ...string) modifier {
	return func(cr *clusterv1alpha1.ActionsOrganizationSecret) {
		cr.Spec.ForProvider.SelectedRepositories = []clusterv1alpha1.SecretSelectedRepo{}
		for _, n := range names {
			cr.Spec.ForProvider.SelectedRepositories = append(cr.Spec.ForProvider.SelectedRepositories, clusterv1alpha1.SecretSelectedRepo{Repo: n})
		}
	}
}

func withPolicies(p ...xpv2.ManagementAction) modifier {
	return func(cr *clusterv1alpha1.ActionsOrganizationSecret) { cr.Spec.ManagementPolicies = p }
}

func withLastWrite(rv string, updatedAt time.Time) modifier {
	return func(cr *clusterv1alpha1.ActionsOrganizationSecret) {
		cr.Status.LastWrite = &clusterv1alpha1.ActionsOrganizationSecretWriteRecord{
			SecretUID:             testSourceUID,
			SecretResourceVersion: rv,
			UpdatedAt:             updatedAt.UTC().Format(time.RFC3339),
		}
	}
}

func withoutValueRef() modifier {
	return func(cr *clusterv1alpha1.ActionsOrganizationSecret) { cr.Spec.ForProvider.ValueSecretRef = nil }
}

func newCR(m ...modifier) *clusterv1alpha1.ActionsOrganizationSecret {
	cr := &clusterv1alpha1.ActionsOrganizationSecret{}
	cr.Spec.ForProvider.Org = testOrg
	cr.Spec.ForProvider.Visibility = "all"
	cr.Spec.ForProvider.ValueSecretRef = &xpv2.SecretKeySelector{
		SecretReference: xpv2.SecretReference{Name: testSourceName, Namespace: testNamespace},
		Key:             testSourceKey,
	}
	meta.SetExternalName(cr, testName)
	for _, f := range m {
		f(cr)
	}
	return cr
}

func (o *ghOrg) external(kube client.Client) *external {
	return &external{
		github: &ghclient.Client{Services: &ghclient.Services{Actions: o.actions(), Repositories: o.repositories()}},
		kube:   kube,
	}
}

// scope is one API scope's view of the controller under test: the client the
// reconciler would call, the resource it would hand it, and the status the
// resource ends up with, read in the cluster-scoped form.
type scope struct {
	// ext is the external client under test, shared with client, so a test can
	// swap what it talks to.
	ext    *external
	client managed.ExternalClient
	cr     resource.Managed
	status func(t *testing.T) clusterv1alpha1.ActionsOrganizationSecretStatus
	cond   func() xpv2.Condition
}

// forEachScope runs fn once per API scope, each with a fresh GitHub and a fresh
// Kubernetes Secret, so a test that mutates either cannot affect the other.
// setup arranges the organization and returns the resource the test wants.
func forEachScope(t *testing.T, setup func(o *ghOrg) *clusterv1alpha1.ActionsOrganizationSecret, fn func(t *testing.T, o *ghOrg, s scope, kube client.Client)) {
	t.Helper()
	t.Run("Cluster", func(t *testing.T) {
		o := newOrg(t)
		cr := setup(o)
		kube := newKube(t, valueSecret(testValue, "5"))
		e := o.external(kube)
		fn(t, o, scope{
			ext:    e,
			client: e,
			cr:     cr,
			status: func(*testing.T) clusterv1alpha1.ActionsOrganizationSecretStatus { return cr.Status },
			cond:   func() xpv2.Condition { return cr.GetCondition(xpv2.TypeReady) },
		}, kube)
	})
	t.Run("Namespaced", func(t *testing.T) {
		o := newOrg(t)
		cr := setup(o)
		ns := ddtest.Convert(t, cr, &namespacedv1alpha1.ActionsOrganizationSecret{})
		ns.SetNamespace(testNamespace)
		kube := newKube(t, valueSecret(testValue, "5"))
		e := o.external(kube)
		e.secretNamespace = testNamespace
		fn(t, o, scope{
			ext: e,
			client: scopebridge.New[*namespacedv1alpha1.ActionsOrganizationSecret](e,
				func() *clusterv1alpha1.ActionsOrganizationSecret { return &clusterv1alpha1.ActionsOrganizationSecret{} }),
			cr: ns,
			status: func(t *testing.T) clusterv1alpha1.ActionsOrganizationSecretStatus {
				return ddtest.Convert(t, ns, &clusterv1alpha1.ActionsOrganizationSecret{}).Status
			},
			cond: func() xpv2.Condition { return ns.GetCondition(xpv2.TypeReady) },
		}, kube)
	})
}

// fixed returns a setup that ignores the organization and returns cr.
func fixed(cr func() *clusterv1alpha1.ActionsOrganizationSecret) func(*ghOrg) *clusterv1alpha1.ActionsOrganizationSecret {
	return func(*ghOrg) *clusterv1alpha1.ActionsOrganizationSecret { return cr() }
}

func sortedIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
