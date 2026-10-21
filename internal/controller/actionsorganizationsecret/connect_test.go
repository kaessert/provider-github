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
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1"
	clusterapis "github.com/provider-github/provider-github/apis/cluster/v1alpha1"
	namespacedv1alpha1 "github.com/provider-github/provider-github/apis/namespaced/organizations/v1alpha1"
	namespacedapis "github.com/provider-github/provider-github/apis/namespaced/v1alpha1"
	"github.com/provider-github/provider-github/internal/controller/scopebridge"
)

const (
	testCredsSecret = "gh-creds"
	testCredsKey    = "creds"
	testPCName      = "default"
	testCredsNS     = "crossplane-system"
)

// appCredentials returns credentials in the "appID,installationID,PEM" layout
// with a real, if throwaway, RSA key, so the client can be constructed without
// any call to GitHub.
func appCredentials(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return "123,456," + string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func connectKube(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, clusterapis.SchemeBuilder.AddToScheme, namespacedapis.SchemeBuilder.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("build scheme: %v", err)
		}
	}
	return kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func credsSecret(namespace, creds string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testCredsSecret, Namespace: namespace},
		Data:       map[string][]byte{testCredsKey: []byte(creds)},
	}
}

func credsSelector(namespace string) xpv2.CommonCredentialSelectors {
	return xpv2.CommonCredentialSelectors{SecretRef: &xpv2.SecretKeySelector{
		SecretReference: xpv2.SecretReference{Name: testCredsSecret, Namespace: namespace},
		Key:             testCredsKey,
	}}
}

type stubTracker struct{ err error }

func (s stubTracker) Track(context.Context, resource.LegacyManaged) error { return s.err } //nolint:staticcheck // LegacyManaged is what cluster-scoped managed resources implement.

func clusterCRWithPC(pc string) *clusterv1alpha1.ActionsOrganizationSecret {
	cr := newCR()
	cr.SetProviderConfigReference(&xpv2.Reference{Name: pc})
	return cr
}

func TestConnectClusterSuccess(t *testing.T) {
	kube := connectKube(t,
		&clusterapis.ProviderConfig{
			ObjectMeta: metav1.ObjectMeta{Name: testPCName},
			Spec: clusterapis.ProviderConfigSpec{Credentials: clusterapis.ProviderCredentials{
				Source: xpv2.CredentialsSourceSecret, CommonCredentialSelectors: credsSelector(testCredsNS),
			}},
		},
		credsSecret(testCredsNS, appCredentials(t)))
	c := &connector{kube: kube, usage: stubTracker{}}

	got, err := c.Connect(context.Background(), clusterCRWithPC(testPCName))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	e, ok := got.(*external)
	if !ok || e.github == nil || e.kube == nil {
		t.Fatalf("Connect returned %T, want an *external holding a GitHub client and the Kubernetes client", got)
	}
	if e.secretNamespace != "" {
		t.Errorf("secretNamespace = %q, want none for a cluster-scoped resource", e.secretNamespace)
	}
}

func TestConnectClusterProviderConfigNotFound(t *testing.T) {
	c := &connector{kube: connectKube(t), usage: stubTracker{}}
	_, err := c.Connect(context.Background(), clusterCRWithPC("missing"))
	if err == nil || !strings.Contains(err.Error(), errGetPC) {
		t.Errorf("Connect: error = %v, want one wrapping %q", err, errGetPC)
	}
}

// The cluster-scoped variant fetches the ProviderConfig the resource names,
// directly by name: another config's credentials are never used.
func TestConnectClusterUsesTheNamedProviderConfig(t *testing.T) {
	kube := connectKube(t,
		&clusterapis.ProviderConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "other"},
			Spec: clusterapis.ProviderConfigSpec{Credentials: clusterapis.ProviderCredentials{
				Source: xpv2.CredentialsSourceSecret, CommonCredentialSelectors: credsSelector("nowhere"),
			}},
		},
		&clusterapis.ProviderConfig{
			ObjectMeta: metav1.ObjectMeta{Name: testPCName},
			Spec: clusterapis.ProviderConfigSpec{Credentials: clusterapis.ProviderCredentials{
				Source: xpv2.CredentialsSourceSecret, CommonCredentialSelectors: credsSelector(testCredsNS),
			}},
		},
		credsSecret(testCredsNS, appCredentials(t)))
	c := &connector{kube: kube, usage: stubTracker{}}

	if _, err := c.Connect(context.Background(), clusterCRWithPC(testPCName)); err != nil {
		t.Errorf("Connect with the named ProviderConfig: %v", err)
	}
	if _, err := c.Connect(context.Background(), clusterCRWithPC("other")); err == nil {
		t.Error("Connect with a ProviderConfig whose credentials Secret is absent: no error")
	}
}

func TestConnectClusterUsageTrackingFailure(t *testing.T) {
	boom := errors.New("boom")
	c := &connector{kube: connectKube(t), usage: stubTracker{err: boom}}
	_, err := c.Connect(context.Background(), clusterCRWithPC(testPCName))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), errTrackPCUsage) {
		t.Errorf("Connect: error = %v, want the tracker's error wrapped as %q", err, errTrackPCUsage)
	}
}

func TestConnectClusterRejectsOtherKinds(t *testing.T) {
	c := &connector{kube: connectKube(t), usage: stubTracker{}}
	if _, err := c.Connect(context.Background(), &namespacedv1alpha1.ActionsOrganizationSecret{}); err == nil {
		t.Error("Connect: no error for a resource that is not a cluster-scoped ActionsOrganizationSecret")
	}
}

func newNamespacedConnector(t *testing.T, objs ...client.Object) *namespacedConnector {
	t.Helper()
	kube := connectKube(t, objs...)
	return &namespacedConnector{
		kube:  kube,
		usage: resource.NewProviderConfigUsageTracker(kube, &namespacedapis.ProviderConfigUsage{}),
	}
}

func namespacedCR(kind, name string) *namespacedv1alpha1.ActionsOrganizationSecret {
	cr := &namespacedv1alpha1.ActionsOrganizationSecret{}
	cr.SetName("secret")
	cr.SetNamespace(testNamespace)
	cr.SetUID("cr-uid")
	cr.Spec.ForProvider.Org = testOrg
	cr.SetProviderConfigReference(&xpv2.ProviderConfigReference{Kind: kind, Name: name})
	return cr
}

func namespacedPC() *namespacedapis.ProviderConfig {
	return &namespacedapis.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: testPCName, Namespace: testNamespace},
		Spec: namespacedapis.ProviderConfigSpec{Credentials: namespacedapis.ProviderCredentials{
			Source: xpv2.CredentialsSourceSecret, CommonCredentialSelectors: credsSelector("ignored"),
		}},
	}
}

// A namespaced resource is served by the cluster-scoped client through the
// scope bridge, with its value Secret pinned to its own namespace.
func TestConnectNamespacedKindRouting(t *testing.T) {
	creds := appCredentials(t)
	cases := map[string]struct {
		objs    []client.Object
		cr      *namespacedv1alpha1.ActionsOrganizationSecret
		wantErr string
	}{
		"ProviderConfig": {
			objs: []client.Object{namespacedPC(), credsSecret(testNamespace, creds)},
			cr:   namespacedCR("ProviderConfig", testPCName),
		},
		"ClusterProviderConfig": {
			objs: []client.Object{
				&namespacedapis.ClusterProviderConfig{
					ObjectMeta: metav1.ObjectMeta{Name: testPCName},
					Spec: namespacedapis.ProviderConfigSpec{Credentials: namespacedapis.ProviderCredentials{
						Source: xpv2.CredentialsSourceSecret, CommonCredentialSelectors: credsSelector(testCredsNS),
					}},
				},
				credsSecret(testCredsNS, creds),
			},
			cr: namespacedCR("ClusterProviderConfig", testPCName),
		},
		"UnsupportedKind": {
			objs:    []client.Object{namespacedPC(), credsSecret(testNamespace, creds)},
			cr:      namespacedCR("SomethingElse", testPCName),
			wantErr: errNewClient,
		},
		"ProviderConfigNotFound": {
			cr:      namespacedCR("ProviderConfig", "missing"),
			wantErr: errNewClient,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newNamespacedConnector(t, tc.objs...)
			got, err := c.Connect(context.Background(), tc.cr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("Connect: error = %v, want one wrapping %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if _, ok := got.(*scopebridge.External[*namespacedv1alpha1.ActionsOrganizationSecret, *clusterv1alpha1.ActionsOrganizationSecret]); !ok {
				t.Errorf("Connect returned %T, want the scope bridge", got)
			}
		})
	}
}

func TestConnectNamespacedRejectsOtherKinds(t *testing.T) {
	c := newNamespacedConnector(t)
	if _, err := c.Connect(context.Background(), newCR()); err == nil {
		t.Error("Connect: no error for a resource that is not a namespaced ActionsOrganizationSecret")
	}
}
