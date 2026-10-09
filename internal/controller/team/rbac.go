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

// Package team reconciles its managed resource in both API scopes. The markers
// below are the RBAC the controller needs.
//
// Own resources: full access to the managed resource and its status in both API groups.
// +kubebuilder:rbac:groups=organizations.github.crossplane.io,resources=teams,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=organizations.github.crossplane.io,resources=teams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=organizations.github.m.crossplane.io,resources=teams,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=organizations.github.m.crossplane.io,resources=teams/status,verbs=get;update;patch
//
// Referenced resources: the reference resolvers read these kinds.
// +kubebuilder:rbac:groups=organizations.github.crossplane.io,resources=memberships,verbs=get;list;watch
// +kubebuilder:rbac:groups=organizations.github.m.crossplane.io,resources=memberships,verbs=get;list;watch
// +kubebuilder:rbac:groups=organizations.github.crossplane.io,resources=organizations,verbs=get;list;watch
// +kubebuilder:rbac:groups=organizations.github.m.crossplane.io,resources=organizations,verbs=get;list;watch
// +kubebuilder:rbac:groups=organizations.github.crossplane.io,resources=teams,verbs=get;list;watch
// +kubebuilder:rbac:groups=organizations.github.m.crossplane.io,resources=teams,verbs=get;list;watch
package team
