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

package scopebridge

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	clusterv1alpha1 "github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1"
	namespacedv1alpha1 "github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1"
)

// fakeInner is an external client for the cluster-scoped ActionsSecretAccess.
type fakeInner struct {
	observe func(*clusterv1alpha1.ActionsSecretAccess) (managed.ExternalObservation, error)
	create  func(*clusterv1alpha1.ActionsSecretAccess) (managed.ExternalCreation, error)
}

func (f *fakeInner) Observe(_ context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	return f.observe(mg.(*clusterv1alpha1.ActionsSecretAccess))
}

func (f *fakeInner) Create(_ context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	return f.create(mg.(*clusterv1alpha1.ActionsSecretAccess))
}

func (f *fakeInner) Update(context.Context, resource.Managed) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, nil
}

func (f *fakeInner) Delete(context.Context, resource.Managed) (managed.ExternalDelete, error) {
	return managed.ExternalDelete{}, nil
}

func (f *fakeInner) Disconnect(context.Context) error { return nil }

func bridged(inner managed.ExternalClient) *External[*namespacedv1alpha1.ActionsSecretAccess, *clusterv1alpha1.ActionsSecretAccess] {
	return New[*namespacedv1alpha1.ActionsSecretAccess](inner, func() *clusterv1alpha1.ActionsSecretAccess {
		return &clusterv1alpha1.ActionsSecretAccess{}
	})
}

func namespacedObject() *namespacedv1alpha1.ActionsSecretAccess {
	cr := &namespacedv1alpha1.ActionsSecretAccess{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "access",
			Namespace:   "tenant",
			Annotations: map[string]string{"keep": "me"},
			Finalizers:  []string{"finalizer.managedresource.crossplane.io"},
		},
		Spec: namespacedv1alpha1.ActionsSecretAccessSpec{
			ForProvider: namespacedv1alpha1.ActionsSecretAccessParameters{
				Org:        "acme",
				Visibility: "selected",
				SelectedRepositories: []namespacedv1alpha1.SecretSelectedRepo{
					{Repo: "one"},
					{Repo: "two", RepoRef: &xpv2.NamespacedReference{Name: "two-ref", Namespace: "elsewhere"}},
				},
			},
		},
	}
	cr.SetGroupVersionKind(namespacedv1alpha1.ActionsSecretAccessGroupVersionKind)
	cr.SetProviderConfigReference(&xpv2.ProviderConfigReference{Kind: "ProviderConfig", Name: "tenant-pc"})
	cr.SetWriteConnectionSecretToReference(&xpv2.LocalSecretReference{Name: "conn"})
	cr.SetManagementPolicies(xpv2.ManagementPolicies{xpv2.ManagementActionObserve})
	return cr
}

// The inner client sees the cluster-scoped form of the resource: the same
// forProvider, an unqualified provider config reference and a connection Secret
// reference carrying the resource's namespace.
func TestObserveInnerSeesClusterForm(t *testing.T) {
	var seen *clusterv1alpha1.ActionsSecretAccess
	inner := &fakeInner{observe: func(c *clusterv1alpha1.ActionsSecretAccess) (managed.ExternalObservation, error) {
		seen = c.DeepCopy()
		return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
	}}

	obs, err := bridged(inner).Observe(context.Background(), namespacedObject())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.ResourceExists || !obs.ResourceUpToDate {
		t.Errorf("observation = %+v, want it returned unchanged", obs)
	}

	if seen.Name != "access" || seen.Namespace != "tenant" {
		t.Errorf("identity = %s/%s, want tenant/access", seen.Namespace, seen.Name)
	}
	if got := seen.Annotations["keep"]; got != "me" {
		t.Errorf("annotation keep = %q, want %q", got, "me")
	}
	p := seen.Spec.ForProvider
	if p.Org != "acme" || p.Visibility != "selected" || len(p.SelectedRepositories) != 2 {
		t.Errorf("forProvider = %+v, want it carried over", p)
	}
	if ref := p.SelectedRepositories[1].RepoRef; ref == nil || ref.Name != "two-ref" {
		t.Errorf("repoRef = %+v, want name two-ref", ref)
	}
	if ref := seen.GetProviderConfigReference(); ref == nil || ref.Name != "tenant-pc" {
		t.Errorf("providerConfigRef = %+v, want name tenant-pc", ref)
	}
	if ref := seen.GetWriteConnectionSecretToReference(); ref == nil || ref.Name != "conn" || ref.Namespace != "tenant" {
		t.Errorf("writeConnectionSecretToRef = %+v, want tenant/conn", ref)
	}
	if got := seen.GetObjectKind().GroupVersionKind(); !got.Empty() {
		t.Errorf("cluster form GVK = %v, want it empty", got)
	}
}

// What the inner client writes — status, conditions, the external name — lands
// on the namespaced resource. Its spec is the user's input and is never
// rewritten, so what the cluster form cannot represent survives.
func TestObserveResultCopiedBack(t *testing.T) {
	inner := &fakeInner{observe: func(c *clusterv1alpha1.ActionsSecretAccess) (managed.ExternalObservation, error) {
		c.SetConditions(xpv2.Available())
		meta.SetExternalName(c, "secret-name")
		c.Spec.ForProvider.SelectedRepositories[1].RepoRef = nil
		return managed.ExternalObservation{ResourceExists: true}, nil
	}}

	cr := namespacedObject()
	if _, err := bridged(inner).Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if got := cr.GetCondition(xpv2.TypeReady).Status; got != "True" {
		t.Errorf("Ready = %q, want True", got)
	}
	if got := meta.GetExternalName(cr); got != "secret-name" {
		t.Errorf("external name = %q, want secret-name", got)
	}
	if got := cr.Annotations["keep"]; got != "me" {
		t.Errorf("annotation keep = %q, want it preserved", got)
	}
	if len(cr.Finalizers) != 1 {
		t.Errorf("finalizers = %v, want them preserved", cr.Finalizers)
	}
	if ref := cr.GetProviderConfigReference(); ref == nil || ref.Kind != "ProviderConfig" || ref.Name != "tenant-pc" {
		t.Errorf("providerConfigRef = %+v, want Kind ProviderConfig preserved", ref)
	}
	if ref := cr.GetWriteConnectionSecretToReference(); ref == nil || ref.Name != "conn" {
		t.Errorf("writeConnectionSecretToRef = %+v, want it preserved", ref)
	}
	if got := cr.GetObjectKind().GroupVersionKind(); got != namespacedv1alpha1.ActionsSecretAccessGroupVersionKind {
		t.Errorf("GVK = %v, want the namespaced Kind preserved", got)
	}
	if ref := cr.Spec.ForProvider.SelectedRepositories[1].RepoRef; ref == nil || ref.Namespace != "elsewhere" {
		t.Errorf("repoRef = %+v, want its namespace preserved", ref)
	}
	if got := cr.GetManagementPolicies(); len(got) != 1 || got[0] != xpv2.ManagementActionObserve {
		t.Errorf("managementPolicies = %v, want [Observe]", got)
	}
}

// An inner error is returned as is, and what the inner client set before
// failing is kept, exactly as it would be on the cluster-scoped resource.
func TestCreateErrorReturnedAndStateCopiedBack(t *testing.T) {
	want := errors.New("boom")
	inner := &fakeInner{create: func(c *clusterv1alpha1.ActionsSecretAccess) (managed.ExternalCreation, error) {
		c.SetConditions(xpv2.Creating())
		return managed.ExternalCreation{}, want
	}}

	cr := namespacedObject()
	_, err := bridged(inner).Create(context.Background(), cr)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if got := cr.GetCondition(xpv2.TypeReady).Reason; got != xpv2.ReasonCreating {
		t.Errorf("Ready reason = %q, want %q", got, xpv2.ReasonCreating)
	}
}

func TestObserveWrongType(t *testing.T) {
	_, err := bridged(&fakeInner{}).Observe(context.Background(), &namespacedv1alpha1.Team{})
	if err == nil {
		t.Fatal("Observe with another Kind returned no error")
	}
}

type webhookInner struct{ fakeInner }

func (w *webhookInner) Observe(_ context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr := mg.(*clusterv1alpha1.OrganizationWebhook)
	cr.Status.AtProvider.ID = 42
	return managed.ExternalObservation{ResourceExists: true}, nil
}

// atProvider is part of the status the inner client writes.
func TestObserveAtProviderCopiedBack(t *testing.T) {
	b := New[*namespacedv1alpha1.OrganizationWebhook](&webhookInner{}, func() *clusterv1alpha1.OrganizationWebhook {
		return &clusterv1alpha1.OrganizationWebhook{}
	})

	cr := &namespacedv1alpha1.OrganizationWebhook{}
	if _, err := b.Observe(context.Background(), cr); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := cr.Status.AtProvider.ID; got != 42 {
		t.Errorf("atProvider.id = %d, want 42", got)
	}
}
