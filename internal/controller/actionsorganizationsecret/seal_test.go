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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v90/github"
	"golang.org/x/crypto/nacl/box"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	"github.com/provider-github/provider-github/internal/clients/fake"
)

// The sealed value opens with the private key of the pair it was sealed to and
// yields the plaintext, whatever the plaintext holds.
func TestSealRoundTrips(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(pub[:])

	cases := map[string]string{
		"Plain":     "hunter2",
		"Empty":     "",
		"Multiline": "-----BEGIN KEY-----\nabc\n-----END KEY-----\n",
		"Unicode":   "pässwörd-🔑",
		"Long":      string(make([]byte, 4096)),
	}
	for name, plaintext := range cases {
		t.Run(name, func(t *testing.T) {
			sealed, err := seal(key, []byte(plaintext))
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			raw, err := base64.StdEncoding.DecodeString(sealed)
			if err != nil {
				t.Fatalf("sealed value is not base64: %v", err)
			}
			got, ok := box.OpenAnonymous(nil, raw, pub, priv)
			if !ok {
				t.Fatal("the private key does not open the sealed value")
			}
			if diff := cmp.Diff(plaintext, string(got)); diff != "" {
				t.Errorf("opened value: -want, +got:\n%s", diff)
			}
		})
	}
}

// A key other than the one sealed to cannot open the box, and sealing the same
// plaintext twice gives two different boxes.
func TestSealIsAnonymousAndFresh(t *testing.T) {
	pub, _, _ := box.GenerateKey(rand.Reader)
	otherPub, otherPriv, _ := box.GenerateKey(rand.Reader)
	key := base64.StdEncoding.EncodeToString(pub[:])

	a, err := seal(key, []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := seal(key, []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two seals of one value are identical")
	}
	raw, _ := base64.StdEncoding.DecodeString(a)
	if _, ok := box.OpenAnonymous(nil, raw, otherPub, otherPriv); ok {
		t.Error("a different key pair opened the box")
	}
}

func TestSealRejectsMalformedKeys(t *testing.T) {
	cases := map[string]string{
		"Empty":       "",
		"NotBase64":   "!!!not base64!!!",
		"TooShort":    base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"TooLong":     base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"UrlEncoding": "____",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := seal(key, []byte("v")); err == nil || got != "" {
				t.Errorf("seal = %q, %v; want an error and no value", got, err)
			}
		})
	}
}

func rejection(status int, msg string, details ...string) error {
	er := &github.ErrorResponse{Response: &http.Response{StatusCode: status}, Message: msg}
	for _, d := range details {
		er.Errors = append(er.Errors, github.Error{Message: d})
	}
	return er
}

const staleText = "Provided key 123 is not the latest version available. Call `GET /secrets/public-key` and resign the data using the latest key."

func TestIsStaleKey(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"NilError":               {err: nil, want: false},
		"PlainError":             {err: http.ErrServerClosed, want: false},
		"StaleInMessage":         {err: rejection(422, staleText), want: true},
		"StaleInErrorsList":      {err: rejection(422, "Validation Failed", "other", staleText), want: true},
		"WrappedStale":           {err: wrapErr(rejection(422, staleText)), want: true},
		"OtherValidationFailure": {err: rejection(422, "Validation Failed", "encrypted_value is not base64"), want: false},
		"NameSyntax":             {err: rejection(422, "Secret names can only contain alphanumeric characters"), want: false},
		"StaleTextButForbidden":  {err: rejection(403, staleText), want: false},
		"StaleTextButNotFound":   {err: rejection(404, staleText), want: false},
		"NoHTTPResponse":         {err: &github.ErrorResponse{Message: staleText}, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isStaleKey(tc.err); got != tc.want {
				t.Errorf("isStaleKey = %t, want %t", got, tc.want)
			}
		})
	}
}

type wrapped struct{ error }

func (w wrapped) Unwrap() error { return w.error }

func wrapErr(err error) error { return wrapped{err} }

// The stale-key rule is read from the 422 body however GitHub phrases it: in the
// message or in the errors list. Anything but a 422 is not retried.
func TestPutRetriesOnlyOnAStale422(t *testing.T) {
	cases := map[string]struct {
		first       error
		wantPuts    int
		wantFetches int
		wantErr     bool
	}{
		"StaleInMessage":    {first: rejection(422, staleText), wantPuts: 2, wantFetches: 2},
		"StaleInErrorsList": {first: rejection(422, "Validation Failed", staleText), wantPuts: 2, wantFetches: 2},
		"OtherValidation":   {first: rejection(422, "Validation Failed", "bad"), wantPuts: 1, wantFetches: 1, wantErr: true},
		"Forbidden":         {first: rejection(403, staleText), wantPuts: 1, wantFetches: 1, wantErr: true},
		"ServerError":       {first: rejection(500, "boom"), wantPuts: 1, wantFetches: 1, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			forEachScope(t, fixed(newCRNoMods), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
				a := o.actions()
				accepted := a.MockCreateOrUpdateOrgSecret
				calls := 0
				a.MockCreateOrUpdateOrgSecret = func(ctx context.Context, org, name string, req github.SecretOrgRequest) (*github.Response, error) {
					calls++
					if calls == 1 {
						o.puts = append(o.puts, req)
						return fake.GenerateEmptyResponse(), tc.first
					}
					return accepted(ctx, org, name, req)
				}
				s.ext.github.Actions = a

				_, err := s.client.Create(context.Background(), s.cr)
				if (err != nil) != tc.wantErr {
					t.Errorf("Create error = %v, want error: %t", err, tc.wantErr)
				}
				if len(o.puts) != tc.wantPuts || o.keyFetches != tc.wantFetches {
					t.Errorf("PUTs = %d, key fetches = %d; want %d and %d", len(o.puts), o.keyFetches, tc.wantPuts, tc.wantFetches)
				}
			})
		})
	}
}

// A failing public key request stops the write before any PUT, on the first
// fetch and on the refetch after a stale key.
func TestPutPublicKeyFailures(t *testing.T) {
	boom := rejection(500, "key service down")
	cases := map[string]struct {
		failOnFetch int
		staleFirst  bool
		wantPuts    int
	}{
		"FirstFetch":   {failOnFetch: 1, wantPuts: 0},
		"RefetchAfter": {failOnFetch: 2, staleFirst: true, wantPuts: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			forEachScope(t, fixed(newCRNoMods), func(t *testing.T, o *ghOrg, s scope, _ client.Client) {
				if tc.staleFirst {
					o.staleKeyID = "stale"
				}
				a := o.actions()
				fetch := a.MockGetOrgPublicKey
				n := 0
				a.MockGetOrgPublicKey = func(ctx context.Context, org string) (*github.PublicKey, *github.Response, error) {
					n++
					if n == tc.failOnFetch {
						return nil, fake.GenerateEmptyResponse(), boom
					}
					return fetch(ctx, org)
				}
				s.ext.github.Actions = a

				_, err := s.client.Create(context.Background(), s.cr)
				if err == nil {
					t.Fatal("Create: no error")
				}
				if len(o.puts) != tc.wantPuts {
					t.Errorf("PUTs = %d, want %d", len(o.puts), tc.wantPuts)
				}
				if o.secret != nil {
					t.Error("the secret exists although the write failed")
				}
			})
		})
	}
}

func newCRNoMods() *clusterv1alpha1.ActionsOrganizationSecret { return newCR() }
