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

package refresolver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/fake"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// member is one entry of an atomic list: a reference that resolves to Value, or
// a value the user wrote.
type member struct {
	Ref   *string `json:"ref,omitempty"`
	Value *string `json:"value,omitempty"`
}

type forProvider struct {
	A       *string  `json:"a,omitempty"`
	ARef    *string  `json:"aRef,omitempty"`
	B       *string  `json:"b,omitempty"`
	BRef    *string  `json:"bRef,omitempty"`
	Parents []member `json:"parents,omitempty"`
	Note    *string  `json:"note,omitempty"`
}

type spec struct {
	ForProvider forProvider `json:"forProvider"`
}

// referrer is a managed resource whose generated resolver is the function in
// resolve, which stands in for the generated ResolveReferences method.
type referrer struct {
	fake.Managed
	Spec    spec                  `json:"spec"`
	resolve func(fp *forProvider) `json:"-"`
}

func (m *referrer) DeepCopyObject() runtime.Object {
	out := &referrer{resolve: m.resolve}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		panic(err)
	}
	return out
}

func (m *referrer) ResolveReferences(context.Context, client.Reader) error {
	m.resolve(&m.Spec.ForProvider)
	return nil
}

// slots are the optional string fields of a referrer that a generated resolver
// fills from a reference and that may be kept empty.
func slots(mg resource.Managed) []**string {
	m, ok := mg.(*referrer)
	if !ok {
		return nil
	}
	return []**string{&m.Spec.ForProvider.A, &m.Spec.ForProvider.B}
}

// plain is a managed resource that does not reference anything.
type plain struct{ fake.Managed }

// broken is a managed resource whose resolution fails.
type broken struct{ fake.Managed }

func (*broken) ResolveReferences(context.Context, client.Reader) error {
	return errors.New("referenced resource not found")
}

// server is the client of the test. It records every patch of the managed
// resource and applies a merge patch to the stored JSON the way the API server
// does, so that what the resource holds afterwards can be read back.
type server struct {
	client.Client
	stored  []byte
	types   []types.PatchType
	patches []string
	err     error
}

func (s *server) Patch(_ context.Context, obj client.Object, patch client.Patch, _ ...client.PatchOption) error {
	if s.err != nil {
		return s.err
	}
	data, err := patch.Data(obj)
	if err != nil {
		return err
	}
	s.types = append(s.types, patch.Type())
	s.patches = append(s.patches, string(data))
	var base, p map[string]any
	if err := json.Unmarshal(s.stored, &base); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	out, err := json.Marshal(mergePatch(base, p))
	s.stored = out
	return err
}

// mergePatch applies the JSON merge patch p (RFC 7386) to base.
func mergePatch(base, p map[string]any) map[string]any {
	for k, v := range p {
		switch pv := v.(type) {
		case nil:
			delete(base, k)
		case map[string]any:
			bm, _ := base[k].(map[string]any)
			if bm == nil {
				bm = map[string]any{}
			}
			base[k] = mergePatch(bm, pv)
		default:
			base[k] = v
		}
	}
	return base
}

// stored creates a server that holds m.
func stored(t *testing.T, m *referrer) *server {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return &server{stored: b}
}

func (s *server) read(t *testing.T) *referrer {
	t.Helper()
	out := &referrer{}
	if err := json.Unmarshal(s.stored, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOneReferenceChangesOtherKept(t *testing.T) {
	// The first pass resolved both references. The user then points BRef at
	// another resource, and the second pass resolves only B anew; A is already
	// what its reference says.
	resolve := func(fp *forProvider) {
		if fp.ARef != nil {
			fp.A = ptr.To("a-" + *fp.ARef)
		}
		if fp.BRef != nil {
			fp.B = ptr.To("b-" + *fp.BRef)
		}
	}
	m := &referrer{resolve: resolve}
	m.Spec.ForProvider = forProvider{A: ptr.To("a-one"), ARef: ptr.To("one"), B: ptr.To("b-two"), BRef: ptr.To("three")}

	kube := stored(t, m)
	if err := New(kube).ResolveReferences(context.Background(), m); err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}

	if len(kube.types) != 1 || kube.types[0] != types.MergePatchType {
		t.Fatalf("patch types = %v, want one %q", kube.types, types.MergePatchType)
	}
	if want := `{"spec":{"forProvider":{"b":"b-three"}}}`; kube.patches[0] != want {
		t.Errorf("patch = %s, want %s", kube.patches[0], want)
	}
	got := kube.read(t).Spec.ForProvider
	if ptr.Deref(got.A, "<nil>") != "a-one" || ptr.Deref(got.B, "<nil>") != "b-three" {
		t.Errorf("stored A=%s B=%s, want a-one and b-three", ptr.Deref(got.A, "<nil>"), ptr.Deref(got.B, "<nil>"))
	}
}

func TestAtomicListKeepsUserMember(t *testing.T) {
	// The list is atomic: a merge patch replaces it whole. It holds a reference
	// and a member the user wrote, and resolving the reference must not cost the
	// user's member.
	resolve := func(fp *forProvider) {
		for i := range fp.Parents {
			if fp.Parents[i].Ref != nil {
				fp.Parents[i].Value = ptr.To("resolved-" + *fp.Parents[i].Ref)
			}
		}
	}
	m := &referrer{resolve: resolve}
	m.Spec.ForProvider.Parents = []member{{Ref: ptr.To("one")}, {Value: ptr.To("written-by-user")}}

	kube := stored(t, m)
	if err := New(kube).ResolveReferences(context.Background(), m); err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}

	got := kube.read(t).Spec.ForProvider.Parents
	if len(got) != 2 || ptr.Deref(got[0].Value, "<nil>") != "resolved-one" || ptr.Deref(got[1].Value, "<nil>") != "written-by-user" {
		t.Errorf("stored list = %+v, want the resolved member and the user's", got)
	}
}

func TestUnchangedSendsNoPatch(t *testing.T) {
	m := &referrer{resolve: func(*forProvider) {}}
	m.Spec.ForProvider = forProvider{A: ptr.To("a-one"), ARef: ptr.To("one")}

	kube := stored(t, m)
	if err := New(kube).ResolveReferences(context.Background(), m); err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
	if len(kube.patches) != 0 {
		t.Errorf("patches sent = %v, want none", kube.patches)
	}
}

// emptying is the generated resolver for an empty value without a reference:
// it turns the value into nil.
func emptying(fp *forProvider) {
	if fp.A != nil && *fp.A == "" && fp.ARef == nil {
		fp.A = nil
	}
	if fp.B != nil && *fp.B == "" && fp.BRef == nil {
		fp.B = nil
	}
	if fp.BRef != nil {
		fp.B = ptr.To("b-" + *fp.BRef)
	}
}

func TestWithKeptEmpty(t *testing.T) {
	cases := map[string]struct {
		opts      []Option
		before    forProvider
		wantA     string
		wantB     string
		wantPatch string
	}{
		"EmptyKept": {
			opts:   []Option{WithKeptEmpty(slots)},
			before: forProvider{A: ptr.To("")},
			wantA:  "",
			wantB:  "<nil>",
		},
		"EmptyDroppedWithoutTheOption": {
			before:    forProvider{A: ptr.To("")},
			wantA:     "<nil>",
			wantB:     "<nil>",
			wantPatch: `{"spec":{"forProvider":{"a":null}}}`,
		},
		"NilStaysNil": {
			opts:   []Option{WithKeptEmpty(slots)},
			before: forProvider{},
			wantA:  "<nil>",
			wantB:  "<nil>",
		},
		"EmptyWithReferenceResolves": {
			opts:      []Option{WithKeptEmpty(slots)},
			before:    forProvider{B: ptr.To(""), BRef: ptr.To("three")},
			wantA:     "<nil>",
			wantB:     "b-three",
			wantPatch: `{"spec":{"forProvider":{"b":"b-three"}}}`,
		},
		"EmptyKeptAndOtherReferenceResolved": {
			opts:      []Option{WithKeptEmpty(slots)},
			before:    forProvider{A: ptr.To(""), BRef: ptr.To("three")},
			wantA:     "",
			wantB:     "b-three",
			wantPatch: `{"spec":{"forProvider":{"b":"b-three"}}}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := &referrer{resolve: emptying}
			m.Spec.ForProvider = tc.before
			kube := stored(t, m)
			if err := New(kube, tc.opts...).ResolveReferences(context.Background(), m); err != nil {
				t.Fatalf("ResolveReferences: %v", err)
			}
			if tc.wantPatch == "" && len(kube.patches) != 0 {
				t.Errorf("patches sent = %v, want none", kube.patches)
			}
			if tc.wantPatch != "" && (len(kube.patches) != 1 || kube.patches[0] != tc.wantPatch) {
				t.Errorf("patches sent = %v, want %s", kube.patches, tc.wantPatch)
			}
			got := kube.read(t).Spec.ForProvider
			// An absent key and a nil pointer read back the same; the stored
			// object is what the API server would hold.
			if ptr.Deref(got.A, "<nil>") != tc.wantA || ptr.Deref(got.B, "<nil>") != tc.wantB {
				t.Errorf("stored A=%s B=%s, want A=%s B=%s", ptr.Deref(got.A, "<nil>"), ptr.Deref(got.B, "<nil>"), tc.wantA, tc.wantB)
			}
		})
	}
}

func TestWithKeptEmptyOtherType(t *testing.T) {
	// slots returns none for a managed resource of another type, and a nil slot
	// is ignored.
	o := WithKeptEmpty(func(resource.Managed) []**string { return []**string{nil} })
	m := &referrer{resolve: emptying}
	m.Spec.ForProvider.A = ptr.To("")
	kube := stored(t, m)
	if err := New(kube, o).ResolveReferences(context.Background(), m); err != nil {
		t.Fatalf("ResolveReferences: %v", err)
	}
}

func TestWithoutReferencesOrWithErrors(t *testing.T) {
	cases := map[string]struct {
		mg      resource.Managed
		patchEr error
		want    string
	}{
		"NoReferences":      {mg: &plain{}},
		"ResolutionFails":   {mg: &broken{}, want: "cannot resolve references"},
		"PatchFails":        {mg: changing(), patchEr: errors.New("patch refused"), want: "cannot patch the managed resource"},
		"PatchSucceedsOnce": {mg: changing()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kube := &server{stored: []byte(`{}`), err: tc.patchEr}
			err := New(kube).ResolveReferences(context.Background(), tc.mg)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("ResolveReferences: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("ResolveReferences: got %v, want %q", err, tc.want)
			}
		})
	}
}

// changing returns a managed resource whose resolution changes its spec.
func changing() resource.Managed {
	return &referrer{resolve: func(fp *forProvider) { fp.Note = ptr.To("resolved") }}
}
