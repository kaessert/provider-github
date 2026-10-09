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

// Package driftcmp compares a value built from a spec with the same value
// built from GitHub. GitHub reports "none" as an absent list, while a spec may
// declare it as an empty one (`bypassActors: []` is what Helm, Kustomize and
// kubectl users write), so the two must compare equal or the object reports
// drift, and is updated, on every poll.
package driftcmp

import (
	"slices"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/utils/ptr"
)

// Equal reports whether a and b hold the same data, treating a nil and an
// empty slice or map as the same, wherever they sit in the value. A pointer to
// a string slice is equal to nil when the slice it points to is empty.
func Equal(a, b any) bool {
	return cmp.Equal(a, b,
		cmpopts.EquateEmpty(),
		cmp.Comparer(func(x, y *[]string) bool {
			return slices.Equal(ptr.Deref(x, nil), ptr.Deref(y, nil))
		}),
	)
}
