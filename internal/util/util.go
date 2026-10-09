/*
 Copyright 2022 The Crossplane Authors.

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

package util

import (
	"slices"
	// nolint:gosec
	"crypto/sha1"
	"encoding/hex"
	"reflect"
	"sort"
	"strings"

	"github.com/crossplane/provider-github/apis/organizations/v1alpha1"
	"github.com/google/go-cmp/cmp"
	pointer "k8s.io/utils/ptr"
)

func SortByKey(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	keys := make([]string, 0, len(m))

	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		out[k] = m[k]
	}

	return out
}

func DiffPermissions(a map[string]string, b map[string]string) (map[string]string, map[string]string, map[string]string) {
	inANotInB := make(map[string]string)
	inBNotInA := make(map[string]string)
	diffs := make(map[string]string)

	for entity, va := range a {
		vb, ok := b[entity]
		if !ok {
			inANotInB[entity] = va
		} else if va != vb {
			diffs[entity] = vb
		}
	}

	for entity, vb := range b {
		_, ok := a[entity]
		if !ok {
			inBNotInA[entity] = vb
		}
	}

	return inANotInB, inBNotInA, diffs
}

func MergeMaps(m1 map[string]string, m2 map[string]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range m1 {
		merged[k] = v
	}
	for key, value := range m2 {
		merged[key] = value
	}
	return merged
}

func DiffRepoWebhooks(a, b map[string]v1alpha1.RepositoryWebhook) (map[string]v1alpha1.RepositoryWebhook, map[string]v1alpha1.RepositoryWebhook, map[string]v1alpha1.RepositoryWebhook) {
	inANotInB := make(map[string]v1alpha1.RepositoryWebhook)
	inBNotInA := make(map[string]v1alpha1.RepositoryWebhook)
	diffs := make(map[string]v1alpha1.RepositoryWebhook)

	for entity, va := range a {
		vb, ok := b[entity]
		if !ok {
			inANotInB[entity] = va
		} else if !reflect.DeepEqual(va, vb) {
			diffs[entity] = vb
		}
	}

	for entity, vb := range b {
		_, ok := a[entity]
		if !ok {
			inBNotInA[entity] = vb
		}
	}

	return inANotInB, inBNotInA, diffs
}

// DiffProtectedBranches compares two maps of BranchProtectionRule, map 'a’ and map 'b’.
// It returns three maps:
// inANotInB: entities (keys) that are present in 'a' but not in 'b' mapped to their values in 'a'
// inBNotInA: entities (keys) that are present in 'b' but not in 'a' mapped to their values in 'b'
// diffs: entities (keys) that are present in both 'a' and 'b' but have different values, mapped to their values in 'b'
func DiffProtectedBranches(a, b map[string]v1alpha1.BranchProtectionRule) (
	map[string]v1alpha1.BranchProtectionRule,
	map[string]v1alpha1.BranchProtectionRule,
	map[string]v1alpha1.BranchProtectionRule,
) {
	inANotInB := make(map[string]v1alpha1.BranchProtectionRule)
	inBNotInA := make(map[string]v1alpha1.BranchProtectionRule)
	diffs := make(map[string]v1alpha1.BranchProtectionRule)

	for entity, va := range a {
		vb, ok := b[entity]
		if !ok {
			inANotInB[entity] = va
		} else if !cmp.Equal(va, vb) {
			diffs[entity] = vb
		}
	}

	for entity, vb := range b {
		_, ok := a[entity]
		if !ok {
			inBNotInA[entity] = vb
		}
	}

	return inANotInB, inBNotInA, diffs

}

// DiffRepositoryRulesets compares two maps of RepositoryRuleset, 'a' and 'b'.
// It returns three maps:
// inANotInB: entities (keys) that are present in 'a' but not in 'b' mapped to their values in 'a'
// inBNotInA: entities (keys) that are present in 'b' but not in 'a' mapped to their values in 'b'
// diffs: entities (keys) that are present in both 'a' and 'b' but have different values, mapped to their values in 'b'
func DiffRepositoryRulesets(a, b map[string]v1alpha1.RepositoryRuleset) (
	map[string]v1alpha1.RepositoryRuleset,
	map[string]v1alpha1.RepositoryRuleset,
	map[string]v1alpha1.RepositoryRuleset) {
	inANotInB := make(map[string]v1alpha1.RepositoryRuleset)
	inBNotInA := make(map[string]v1alpha1.RepositoryRuleset)
	diffs := make(map[string]v1alpha1.RepositoryRuleset)

	for entity, va := range a {
		vb, ok := b[entity]
		if !ok {
			inANotInB[entity] = va
		} else if !reflect.DeepEqual(va, vb) {
			diffs[entity] = vb
		}
	}

	for entity, vb := range b {
		_, ok := a[entity]
		if !ok {
			inBNotInA[entity] = vb
		}
	}

	return inANotInB, inBNotInA, diffs
}

// DefaultToStringSlice is a helper function that checks if the provided slice is
// nil and returns an empty string slice in that case. If the slice is not nil,
// the same slice is returned. The purpose of this function is to avoid nil
// dereference issues in slices and provide a safe way to default to an empty slice.
func DefaultToStringSlice(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}

// SortAndReturnPointer sorts a slice of strings and returns
// a pointer to that sorted slice.
func SortAndReturnPointer(s []string) *[]string {
	sort.Strings(s)
	return &s
}

// SortAndReturn sorts a slice of strings in lexicographical order
// and returns the sorted slice.
func SortAndReturn(s []string) []string {
	sort.Strings(s)
	return s
}

// SortRequiredStatusChecks sorts a slice of RequiredStatusCheck pointers in-place
// by the Context field in ascending order.
func SortRequiredStatusChecks(checks []*v1alpha1.RequiredStatusCheck) {
	sort.Slice(checks, func(i, j int) bool {
		return checks[i].Context < checks[j].Context
	})
}

// SortRulesRequiredStatusChecks sorts a slice of RequiredStatusCheck pointers in-place
// by the Context field in ascending order.
func SortRulesRequiredStatusChecks(checks []*v1alpha1.RulesRequiredStatusChecksParameters) {
	sort.Slice(checks, func(i, j int) bool {
		return checks[i].Context < checks[j].Context
	})
}

// SortRulesBypassActors sorts a slice of RulesetByPassActors pointers in-place
// by the ActorId field in ascending order.
func SortRulesBypassActors(actors []*v1alpha1.RulesetByPassActors) {
	sort.Slice(actors, func(i, j int) bool {
		return *actors[i].ActorId < *actors[j].ActorId
	})

}

// BoolDerefToPointer dereferences the pointer to bool 'ptr',
// uses 'def' as a default if 'ptr' is nil, and returns a new pointer to the resulting bool.
func BoolDerefToPointer(ptr *bool, def bool) *bool {
	b := pointer.Deref(ptr, def)
	return &b
}

// StringDerefToPointer is a helper function that dereferences a pointer to a string 'ptr',
// and returns a new pointer to the resulting string. If 'ptr' is nil, it uses 'def' as a default value.
func StringDerefToPointer(ptr *string, def string) *string {
	s := pointer.Deref(ptr, def)
	return &s
}

// IntDerefToPointer is a helper function that dereferences a pointer to an int 'ptr',
// and returns a new pointer to the resulting int. If 'ptr' is nil, it uses 'def' as a default value.
func IntDerefToPointer(ptr *int, def int) *int {
	i := pointer.Deref(ptr, def)
	return &i
}

// GenerateSHA1Hash hashes a string to SHA-1
func GenerateSHA1Hash(s string) string {
	// Create a SHA-1 hash of the string
	// nolint:gosec
	hash := sha1.New()
	hash.Write([]byte(s))
	hashedBytes := hash.Sum(nil)

	// Encode the hash as a hexadecimal string
	hashedString := hex.EncodeToString(hashedBytes)

	return hashedString
}

// ToLowerSlice converts all the strings in the slice to lowercase.
func ToLowerSlice(input []string) []string {
	for i, v := range input {
		input[i] = strings.ToLower(v)
	}
	return input
}

// EqualUnordered reports whether a and b hold the same strings,
// ignoring order. nil and empty are equal.
func EqualUnordered(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
}
