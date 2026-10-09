/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package clients

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	apisv1alpha1 "github.com/crossplane/provider-github/apis/cluster/v1alpha1"
)

func newFakeKubeWithSecret(t *testing.T, name, namespace string, data map[string][]byte) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Data:       data,
		},
	)
}

func pcWithSecretRef(secretName, namespace, key string) *apisv1alpha1.ProviderConfig {
	return &apisv1alpha1.ProviderConfig{
		Spec: apisv1alpha1.ProviderConfigSpec{
			Credentials: apisv1alpha1.ProviderCredentials{
				Source: xpv2.CredentialsSourceSecret,
				CommonCredentialSelectors: xpv2.CommonCredentialSelectors{
					SecretRef: &xpv2.SecretKeySelector{
						SecretReference: xpv2.SecretReference{Name: secretName, Namespace: namespace},
						Key:             key,
					},
				},
			},
		},
	}
}

// A missing Secret key produces an error that names the secret reference
// and key, not a generic "invalid format for credentials" from deeper in
// the stack. The error message is the operator's only diagnostic.
func TestResolveAllCredentialsMissingKeyNamesTheSecret(t *testing.T) {
	kube := newFakeKubeWithSecret(t, "github-secret", "crossplane-system", map[string][]byte{
		"creds-good": []byte("1,2,PEM"),
	}).Build()

	pc := pcWithSecretRef("github-secret", "crossplane-system", "missing-key")

	_, err := resolveAllCredentials(context.Background(), kube, pc)
	if err == nil {
		t.Fatal("expected error for missing secret key, got nil")
	}

	// Operators have to know the exact source of the failure to fix it; the
	// message must mention both the Secret reference and the key name.
	msg := err.Error()
	for _, want := range []string{"github-secret", "crossplane-system", "missing-key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// An existing Secret key holding zero bytes is treated the same as a
// missing key — the error must still name the secret reference.
func TestResolveAllCredentialsEmptyValueNamesTheSecret(t *testing.T) {
	kube := newFakeKubeWithSecret(t, "github-secret", "crossplane-system", map[string][]byte{
		"creds-empty": {},
	}).Build()

	pc := pcWithSecretRef("github-secret", "crossplane-system", "creds-empty")

	_, err := resolveAllCredentials(context.Background(), kube, pc)
	if err == nil {
		t.Fatal("expected error for empty secret value, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"github-secret", "creds-empty"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// Negative control: the empty-bytes validation must not reject legitimate
// non-empty credentials.
func TestResolveAllCredentialsGoodKeyReturnsBytes(t *testing.T) {
	kube := newFakeKubeWithSecret(t, "github-secret", "crossplane-system", map[string][]byte{
		"creds-good": []byte("1,2,PEM"),
	}).Build()

	pc := pcWithSecretRef("github-secret", "crossplane-system", "creds-good")

	resolved, err := resolveAllCredentials(context.Background(), kube, pc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "1,2,PEM" {
		t.Errorf("resolved = %v, want [%q]", resolved, "1,2,PEM")
	}
}
