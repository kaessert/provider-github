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

package driftcmp

import "testing"

type inner struct {
	Names *[]string
}

type outer struct {
	List  []string
	Items []*inner
	Inner *inner
	Map   map[string][]int64
}

func TestEqual(t *testing.T) {
	empty := []string{}
	cases := map[string]struct {
		a, b any
		want bool
	}{
		"NilSliceAndEmptySlice": {
			a:    outer{List: nil},
			b:    outer{List: []string{}},
			want: true,
		},
		"NilMapAndEmptyMap": {
			a:    map[string]string(nil),
			b:    map[string]string{},
			want: true,
		},
		"NilSliceAndEmptySliceInsideMap": {
			a:    outer{Map: map[string][]int64{"s": nil}},
			b:    outer{Map: map[string][]int64{"s": {}}},
			want: true,
		},
		"NilPointerAndPointerToEmptySlice": {
			a:    outer{Inner: &inner{Names: nil}},
			b:    outer{Inner: &inner{Names: &empty}},
			want: true,
		},
		"NilPointerAndPointerToNilSlice": {
			a:    outer{Inner: &inner{}},
			b:    outer{Inner: &inner{Names: new([]string)}},
			want: true,
		},
		"EmptyItemsAndNilItems": {
			a:    outer{Items: []*inner{}},
			b:    outer{},
			want: true,
		},
		"DifferentElements": {
			a:    outer{List: []string{"a"}},
			b:    outer{List: []string{"b"}},
			want: false,
		},
		"EmptyAndNonEmptySlice": {
			a:    outer{List: []string{}},
			b:    outer{List: []string{"a"}},
			want: false,
		},
		"PointerToNonEmptySliceAndNil": {
			a:    outer{Inner: &inner{Names: &[]string{"a"}}},
			b:    outer{Inner: &inner{}},
			want: false,
		},
		"MapWithDifferentValue": {
			a:    map[string]string{"a": "x"},
			b:    map[string]string{"a": "y"},
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Equal(tc.a, tc.b); got != tc.want {
				t.Errorf("Equal(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
