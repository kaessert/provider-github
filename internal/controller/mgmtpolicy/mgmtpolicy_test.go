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

package mgmtpolicy

import (
	"testing"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func TestWritesDeclared(t *testing.T) {
	cases := map[string]struct {
		policies xpv2.ManagementPolicies
		want     bool
	}{
		"Default":         {policies: nil, want: true},
		"All":             {policies: xpv2.ManagementPolicies{xpv2.ManagementActionAll}, want: true},
		"ObserveCreate":   {policies: xpv2.ManagementPolicies{xpv2.ManagementActionObserve, xpv2.ManagementActionCreate}, want: true},
		"ObserveUpdate":   {policies: xpv2.ManagementPolicies{xpv2.ManagementActionObserve, xpv2.ManagementActionUpdate}, want: true},
		"Observe":         {policies: xpv2.ManagementPolicies{xpv2.ManagementActionObserve}, want: false},
		"ObserveDelete":   {policies: xpv2.ManagementPolicies{xpv2.ManagementActionObserve, xpv2.ManagementActionDelete}, want: false},
		"ObserveLateInit": {policies: xpv2.ManagementPolicies{xpv2.ManagementActionObserve, xpv2.ManagementActionLateInitialize}, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := WritesDeclared(tc.policies); got != tc.want {
				t.Errorf("WritesDeclared(%v) = %v, want %v", tc.policies, got, tc.want)
			}
		})
	}
}
