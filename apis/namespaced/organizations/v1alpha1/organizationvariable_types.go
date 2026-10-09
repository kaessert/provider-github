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

package v1alpha1

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// VariableSelectedRepo references a repository that has access to an
// organization variable whose visibility is "selected".
type VariableSelectedRepo struct {
	// Name of the repository.
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1.Repository
	Repo string `json:"repo,omitempty"`

	// RepoRef is a reference to a Repository.
	// +optional
	RepoRef *xpv2.NamespacedReference `json:"repoRef,omitempty"`

	// RepoSelector selects a reference to a Repository.
	// +optional
	RepoSelector *xpv2.NamespacedSelector `json:"repoSelector,omitempty"`
}

// OrganizationVariableParameters are the configurable fields of a OrganizationVariable.
type OrganizationVariableParameters struct {
	// Org is the name of the GitHub organization that owns this variable.
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1.Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization.
	// +optional
	OrgRef *xpv2.NamespacedReference `json:"orgRef,omitempty"`

	// OrgSelector selects a reference to an Organization.
	// +optional
	OrgSelector *xpv2.NamespacedSelector `json:"orgSelector,omitempty"`

	// Value of the variable.
	Value string `json:"value"`

	// Visibility controls which repositories can access this variable.
	// +kubebuilder:validation:Enum=all;private;selected
	Visibility string `json:"visibility"`

	// SelectedRepositories lists repositories that have access to the
	// variable. Only used (and required) when Visibility is "selected".
	// +optional
	SelectedRepositories []VariableSelectedRepo `json:"selectedRepositories,omitempty"`
}

// OrganizationVariableObservation are the observable fields of a OrganizationVariable.
type OrganizationVariableObservation struct {
}

// A OrganizationVariableSpec defines the desired state of a OrganizationVariable.
type OrganizationVariableSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              OrganizationVariableParameters `json:"forProvider"`
}

// A OrganizationVariableStatus represents the observed state of a OrganizationVariable.
type OrganizationVariableStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 OrganizationVariableObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A OrganizationVariable is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,github}
type OrganizationVariable struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OrganizationVariableSpec   `json:"spec"`
	Status OrganizationVariableStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OrganizationVariableList contains a list of OrganizationVariable
type OrganizationVariableList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OrganizationVariable `json:"items"`
}

// OrganizationVariable type metadata.
var (
	OrganizationVariableKind             = reflect.TypeOf(OrganizationVariable{}).Name()
	OrganizationVariableGroupKind        = schema.GroupKind{Group: Group, Kind: OrganizationVariableKind}.String()
	OrganizationVariableKindAPIVersion   = OrganizationVariableKind + "." + SchemeGroupVersion.String()
	OrganizationVariableGroupVersionKind = SchemeGroupVersion.WithKind(OrganizationVariableKind)
)

func init() {
	SchemeBuilder.Register(&OrganizationVariable{}, &OrganizationVariableList{})
}
