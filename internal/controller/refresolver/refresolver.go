/*
Copyright 2025 The Crossplane Authors.

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

// Package refresolver holds the reference resolver that the controllers of the
// kinds with generated references install in place of the one
// crossplane-runtime ships.
//
// The runtime resolver sends what resolution changed as a server-side apply
// patch under one field owner. Server-side apply removes every field its owner
// held before and no longer sends, so a field the resolver filled on an earlier
// pass is dropped when a later pass changes only another one. A list that holds
// a reference is replaced whole by the first pass that fills it, which makes the
// resolver the owner of the entire list, user-written members included, and a
// later pass that omits the list removes it. Where a rule requires the field or
// forbids its removal, the API server then rejects the patch and the managed
// resource stays at Synced=False; where none does, the field is silently gone
// and the next pass resolves it again. This resolver sends the same difference
// as a plain JSON merge patch, which carries no field ownership: it writes the
// fields resolution changed and leaves every other field as it is.
package refresolver

import (
	"context"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	errResolveReferences = "cannot resolve references"
	errNotObject         = "managed resource is not a client.Object"
	errPreparePatch      = "cannot prepare the merge patch for the resolved references"
	errPatchManaged      = "cannot patch the managed resource with the resolved references"

	// emptyPatch is the merge patch of two equal objects.
	emptyPatch = "{}"
)

// referenceResolvable is implemented by every managed resource that carries
// cross-resource references; the generated ResolveReferences method fills the
// referencing fields from the referenced resources.
type referenceResolvable interface {
	ResolveReferences(ctx context.Context, r client.Reader) error
}

// Resolver resolves the references of a managed resource and persists what
// changed with a merge patch.
type Resolver struct {
	client client.Client
	slots  func(mg resource.Managed) []**string
}

var _ managed.ReferenceResolver = &Resolver{}

// An Option configures a Resolver.
type Option func(*Resolver)

// WithKeptEmpty makes the resolver keep a reference field that the user set to
// the empty string. slots returns the address of every optional string pointer
// of mg that a generated resolver fills from a reference, and only the ones that
// are to be kept when empty; a managed resource of another type returns none.
//
// The generated ResolveReferences turns a resolved empty value into a nil
// pointer, which the diff then reads as a field to remove. Where the empty
// string is a value of its own, such as the way a user clears a column or
// removes an optional association, it must survive resolution: a slot that is
// the empty string before resolution and nil after is set back to the empty
// string. A slot that is nil before resolution still resolves and is not
// compared.
func WithKeptEmpty(slots func(mg resource.Managed) []**string) Option {
	return func(r *Resolver) { r.slots = slots }
}

// New returns a Resolver that reads the referenced resources and patches the
// referencing one through c.
func New(c client.Client, opts ...Option) *Resolver {
	r := &Resolver{client: c}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ResolveReferences resolves the references of mg. It is a no-op for a managed
// resource without references and for one whose references resolve to what the
// spec already holds.
func (r *Resolver) ResolveReferences(ctx context.Context, mg resource.Managed) error {
	rr, ok := mg.(referenceResolvable)
	if !ok {
		return nil
	}
	existing, ok := mg.DeepCopyObject().(client.Object)
	if !ok {
		return errors.New(errNotObject)
	}

	kept := r.emptySlots(mg)

	if err := rr.ResolveReferences(ctx, r.client); err != nil {
		return errors.Wrap(err, errResolveReferences)
	}

	for _, s := range kept {
		if *s == nil {
			empty := ""
			*s = &empty
		}
	}

	patch := client.MergeFrom(existing)
	data, err := patch.Data(mg)
	if err != nil {
		return errors.Wrap(err, errPreparePatch)
	}
	if string(data) == emptyPatch {
		return nil
	}

	return errors.Wrap(r.client.Patch(ctx, mg, patch), errPatchManaged)
}

// emptySlots returns the slots of mg that hold the empty string now.
func (r *Resolver) emptySlots(mg resource.Managed) []**string {
	if r.slots == nil {
		return nil
	}
	var kept []**string
	for _, s := range r.slots(mg) {
		if s != nil && *s != nil && **s == "" {
			kept = append(kept, s)
		}
	}
	return kept
}
