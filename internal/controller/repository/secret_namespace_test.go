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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
)

// A namespaced resource reads a webhook secret from its own namespace only,
// whatever namespace the secret key reference names.
func TestGetRepoWebhookSecretFromRefSecretNamespacePinned(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	kube := kubefake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "hook", Namespace: "tenant"},
		Data:       map[string][]byte{"token": []byte("s3cret")},
	}).Build()

	webhook := &v1alpha1.RepositoryWebhook{
		SecretKeyRef: &xpv2.SecretKeySelector{
			SecretReference: xpv2.SecretReference{Name: "hook", Namespace: "other-tenant"},
			Key:             "token",
		},
	}

	free := &external{kube: kube}
	if _, err := free.getRepoWebhookSecretFromRef(context.Background(), webhook); err == nil {
		t.Fatal("cluster-scoped resource read a Secret from the namespace in the reference, which holds none")
	}

	pinned := &external{kube: kube, secretNamespace: "tenant"}
	got, err := pinned.getRepoWebhookSecretFromRef(context.Background(), webhook)
	if err != nil {
		t.Fatalf("getRepoWebhookSecretFromRef: %v", err)
	}
	if got == nil || *got != "s3cret" {
		t.Errorf("secret = %v, want s3cret", got)
	}
}
