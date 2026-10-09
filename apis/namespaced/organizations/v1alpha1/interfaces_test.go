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

package v1alpha1

import "github.com/crossplane/crossplane-runtime/v2/pkg/resource"

// Every managed resource satisfies the runtime interfaces the managed
// reconciler and the usage tracker rely on. Nothing else binds a concrete type
// to them at compile time.
var (
	_ resource.Managed       = &ActionsSecretAccess{}
	_ resource.ModernManaged = &ActionsSecretAccess{}
	_ resource.Managed       = &DependabotSecretAccess{}
	_ resource.ModernManaged = &DependabotSecretAccess{}
	_ resource.Managed       = &Membership{}
	_ resource.ModernManaged = &Membership{}
	_ resource.Managed       = &Organization{}
	_ resource.ModernManaged = &Organization{}
	_ resource.Managed       = &OrganizationVariable{}
	_ resource.ModernManaged = &OrganizationVariable{}
	_ resource.Managed       = &OrganizationWebhook{}
	_ resource.ModernManaged = &OrganizationWebhook{}
	_ resource.Managed       = &Repository{}
	_ resource.ModernManaged = &Repository{}
	_ resource.Managed       = &RunnerGroup{}
	_ resource.ModernManaged = &RunnerGroup{}
	_ resource.Managed       = &Team{}
	_ resource.ModernManaged = &Team{}
)
