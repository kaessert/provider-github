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

package clients

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/v1alpha1"
)

func namespacedSpec(secretNamespace string) namespacedv1alpha1.ProviderConfigSpec {
	creds := func(name string) namespacedv1alpha1.ProviderCredentials {
		return namespacedv1alpha1.ProviderCredentials{
			Source: xpv2.CredentialsSourceSecret,
			CommonCredentialSelectors: xpv2.CommonCredentialSelectors{
				SecretRef: &xpv2.SecretKeySelector{
					SecretReference: xpv2.SecretReference{Name: name, Namespace: secretNamespace},
					Key:             "creds",
				},
			},
		}
	}
	return namespacedv1alpha1.ProviderConfigSpec{
		Credentials:           creds("primary"),
		AdditionalCredentials: []namespacedv1alpha1.ProviderCredentials{creds("extra")},
	}
}

func namespacedKube(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := namespacedv1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("add namespaced API to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&namespacedv1alpha1.ProviderConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "tenant-pc", Namespace: "tenant"},
			Spec:       namespacedSpec("some-other-namespace"),
		},
		&namespacedv1alpha1.ClusterProviderConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "shared-pc"},
			Spec:       namespacedSpec("crossplane-system"),
		},
	).Build()
}

// A namespaced ProviderConfig is writable by its namespace's tenants, so the
// credential Secret namespace it carries is dead input: it is replaced by the
// namespace of the resource that references it.
func TestNamespacedConfigSpecProviderConfigPinsSecretNamespace(t *testing.T) {
	spec, err := namespacedConfigSpec(context.Background(), namespacedKube(t),
		&xpv2.ProviderConfigReference{Kind: "ProviderConfig", Name: "tenant-pc"}, "tenant")
	if err != nil {
		t.Fatalf("namespacedConfigSpec: %v", err)
	}

	all := []string{spec.Credentials.SecretRef.Namespace, spec.AdditionalCredentials[0].SecretRef.Namespace}
	for i, ns := range all {
		if ns != "tenant" {
			t.Errorf("credential %d secret namespace = %q, want %q", i, ns, "tenant")
		}
	}
	if got := spec.Credentials.SecretRef.Name; got != "primary" {
		t.Errorf("secret name = %q, want %q", got, "primary")
	}
}

// A ClusterProviderConfig is cluster-scoped and can only name a Secret by an
// explicit namespace, so that namespace is kept.
func TestNamespacedConfigSpecClusterProviderConfigKeepsSecretNamespace(t *testing.T) {
	spec, err := namespacedConfigSpec(context.Background(), namespacedKube(t),
		&xpv2.ProviderConfigReference{Kind: "ClusterProviderConfig", Name: "shared-pc"}, "tenant")
	if err != nil {
		t.Fatalf("namespacedConfigSpec: %v", err)
	}
	if got := spec.Credentials.SecretRef.Namespace; got != "crossplane-system" {
		t.Errorf("secret namespace = %q, want %q", got, "crossplane-system")
	}
	if got := spec.AdditionalCredentials[0].SecretRef.Namespace; got != "crossplane-system" {
		t.Errorf("additional secret namespace = %q, want %q", got, "crossplane-system")
	}
}

// The ProviderConfig is looked up in the resource's own namespace only.
func TestNamespacedConfigSpecProviderConfigInOtherNamespaceNotFound(t *testing.T) {
	_, err := namespacedConfigSpec(context.Background(), namespacedKube(t),
		&xpv2.ProviderConfigReference{Kind: "ProviderConfig", Name: "tenant-pc"}, "other-tenant")
	if err == nil || !strings.Contains(err.Error(), errGetNamespacedPC) {
		t.Fatalf("err = %v, want it to contain %q", err, errGetNamespacedPC)
	}
}

// An unknown or empty Kind is an error, never a guess.
func TestNamespacedConfigSpecUnsupportedKind(t *testing.T) {
	for _, kind := range []string{"", "Secret", "providerconfig"} {
		_, err := namespacedConfigSpec(context.Background(), namespacedKube(t),
			&xpv2.ProviderConfigReference{Kind: kind, Name: "tenant-pc"}, "tenant")
		if err == nil || !strings.Contains(err.Error(), errUnsupportedPCKind) {
			t.Errorf("kind %q: err = %v, want it to contain %q", kind, err, errUnsupportedPCKind)
		}
	}
}

func TestNamespacedConfigSpecNilReference(t *testing.T) {
	_, err := namespacedConfigSpec(context.Background(), namespacedKube(t), nil, "tenant")
	if err == nil || !strings.Contains(err.Error(), errNoProviderConfigRef) {
		t.Fatalf("err = %v, want it to contain %q", err, errNoProviderConfigRef)
	}
}
