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

// Package scopebridge lets one external-client implementation serve both API
// scopes of a Kind. The cluster-scoped and namespaced variants of a Kind carry
// the same forProvider and atProvider schema, so the namespaced external client
// is the cluster one wrapped in a conversion at the boundary.
//
// The inner client reads the spec and writes metadata and status. Spec changes
// it makes are not copied back: the cluster-scoped form cannot represent every
// namespaced spec field (a NamespacedReference carries a namespace), and no
// external client in this provider writes its spec.
package scopebridge

import (
	"context"
	"encoding/json"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
)

const (
	errNotNamespaced = "managed resource is not the namespaced variant this client serves"
	errToCluster     = "cannot convert namespaced resource to its cluster-scoped form"
	errFromCluster   = "cannot copy cluster-scoped result back to the namespaced resource"
)

// External serves a namespaced managed resource N with an external client
// written for its cluster-scoped twin C.
type External[N resource.ModernManaged, C resource.LegacyManaged] struct { //nolint:staticcheck // LegacyManaged is what cluster-scoped managed resources implement; the cluster form is exactly that.
	inner      managed.ExternalClient
	newCluster func() C
}

// New wraps inner, which operates on C, so it can reconcile N. newCluster
// returns an empty C.
func New[N resource.ModernManaged, C resource.LegacyManaged](inner managed.ExternalClient, newCluster func() C) *External[N, C] { //nolint:staticcheck // LegacyManaged is what cluster-scoped managed resources implement; the cluster form is exactly that.
	return &External[N, C]{inner: inner, newCluster: newCluster}
}

// Observe implements managed.ExternalClient.
func (e *External[N, C]) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	ns, c, err := e.open(mg)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	obs, err := e.inner.Observe(ctx, c)
	return obs, e.close(ns, c, err)
}

// Create implements managed.ExternalClient.
func (e *External[N, C]) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	ns, c, err := e.open(mg)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	cre, err := e.inner.Create(ctx, c)
	return cre, e.close(ns, c, err)
}

// Update implements managed.ExternalClient.
func (e *External[N, C]) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	ns, c, err := e.open(mg)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	upd, err := e.inner.Update(ctx, c)
	return upd, e.close(ns, c, err)
}

// Delete implements managed.ExternalClient.
func (e *External[N, C]) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	ns, c, err := e.open(mg)
	if err != nil {
		return managed.ExternalDelete{}, err
	}
	del, err := e.inner.Delete(ctx, c)
	return del, e.close(ns, c, err)
}

// Disconnect implements managed.ExternalClient.
func (e *External[N, C]) Disconnect(ctx context.Context) error {
	return e.inner.Disconnect(ctx)
}

// open converts the namespaced resource handed in by the reconciler to the
// cluster-scoped form the inner client expects.
func (e *External[N, C]) open(mg resource.Managed) (N, C, error) {
	var zeroN N
	var zeroC C
	ns, ok := mg.(N)
	if !ok {
		return zeroN, zeroC, errors.New(errNotNamespaced)
	}
	data, err := json.Marshal(ns)
	if err != nil {
		return zeroN, zeroC, errors.Wrap(err, errToCluster)
	}
	c := e.newCluster()
	if err := json.Unmarshal(data, c); err != nil {
		return zeroN, zeroC, errors.Wrap(err, errToCluster)
	}
	// The cluster form is never serialised or sent to the API server; it has
	// no API version of its own on this path.
	c.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{})

	// A namespaced resource names its connection Secret by name only; the
	// Secret lives in the resource's own namespace.
	if ref := c.GetWriteConnectionSecretToReference(); ref != nil {
		ref.Namespace = ns.GetNamespace()
	}
	return ns, c, nil
}

// close copies what the inner client may have changed on c — status and
// conditions, and metadata such as the external-name annotation — back onto ns,
// and returns innerErr unless the copy itself fails.
func (e *External[N, C]) close(ns N, c C, innerErr error) error {
	if err := copyState(ns, c); err != nil {
		return errors.Wrap(err, errFromCluster)
	}
	return innerErr
}

// copyState copies the ObjectMeta and Status of src onto dst. Both are pointers
// to the generated managed resource structs, whose Status types differ by scope
// but serialise identically.
func copyState(dst, src any) error {
	d, s := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()

	dstMeta, srcMeta := d.FieldByName("ObjectMeta"), s.FieldByName("ObjectMeta")
	dstStatus, srcStatus := d.FieldByName("Status"), s.FieldByName("Status")
	if !dstMeta.IsValid() || !srcMeta.IsValid() || !dstStatus.IsValid() || !srcStatus.IsValid() {
		return errors.New("resource has no ObjectMeta or Status")
	}

	om, ok := srcMeta.Interface().(metav1.ObjectMeta)
	if !ok {
		return errors.New("ObjectMeta is not a metav1.ObjectMeta")
	}
	dstMeta.Set(reflect.ValueOf(*om.DeepCopy()))

	data, err := json.Marshal(srcStatus.Interface())
	if err != nil {
		return err
	}
	dstStatus.SetZero()
	return json.Unmarshal(data, dstStatus.Addr().Interface())
}

var _ managed.ExternalClient = &External[resource.ModernManaged, resource.LegacyManaged]{} //nolint:staticcheck // LegacyManaged is what cluster-scoped managed resources implement; the cluster form is exactly that.
