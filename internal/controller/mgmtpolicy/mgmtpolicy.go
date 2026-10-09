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

// Package mgmtpolicy holds the management-policy tests shared by the controllers.
package mgmtpolicy

import (
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// WritesDeclared reports whether the management policies allow the provider to
// create or update the external resource. An empty list is the default, which
// allows everything. Under such a policy a create-time field is always
// declared, even as the empty string; under an Observe-only policy an empty
// field means the user left it out and there is nothing to compare.
func WritesDeclared(policies xpv2.ManagementPolicies) bool {
	if len(policies) == 0 {
		return true
	}
	for _, p := range policies {
		switch p {
		case xpv2.ManagementActionAll, xpv2.ManagementActionCreate, xpv2.ManagementActionUpdate:
			return true
		case xpv2.ManagementActionObserve, xpv2.ManagementActionDelete, xpv2.ManagementActionLateInitialize:
		}
	}
	return false
}
