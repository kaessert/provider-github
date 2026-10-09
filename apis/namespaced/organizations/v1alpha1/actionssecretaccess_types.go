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

	"github.com/crossplane/provider-github/apis/common/driftdetection"
)

// ActionsSecretAccessParameters are the configurable fields of an ActionsSecretAccess.
// +kubebuilder:validation:XValidation:rule="!has(self.visibility) || self.visibility != 'selected' || (has(self.selectedRepositories) && size(self.selectedRepositories) > 0)",message="selectedRepositories is required when visibility is selected"
type ActionsSecretAccessParameters struct {
	// Org is the name of the GitHub organization that owns this secret.
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1.Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization.
	// +optional
	OrgRef *xpv2.NamespacedReference `json:"orgRef,omitempty"`

	// OrgSelector selects a reference to an Organization.
	// +optional
	OrgSelector *xpv2.NamespacedSelector `json:"orgSelector,omitempty"`

	// Visibility declares which repositories may use the secret: all, private,
	// or the selected list. The provider enforces the list when visibility is
	// "selected" but cannot change visibility itself; a mismatch with GitHub
	// sets Ready=False with the reason.
	// +kubebuilder:validation:Enum=all;private;selected
	// +optional
	Visibility string `json:"visibility,omitempty"`

	// SelectedRepositories lists the repositories that may use the secret.
	// Only used (and required) when Visibility is "selected".
	// +optional
	SelectedRepositories []SecretSelectedRepo `json:"selectedRepositories"`
}

// ActionsSecretAccessObservation are the observable fields of an ActionsSecretAccess.
type ActionsSecretAccessObservation struct {
	// ID is the external name of the secret on GitHub.
	ID string `json:"id,omitempty"`

	// Org is the organization the secret was found under.
	Org string `json:"org,omitempty"`

	// Visibility is the visibility GitHub reports for the secret.
	Visibility string `json:"visibility,omitempty"`

	// SelectedRepositories lists the repositories that may use the secret on
	// GitHub. GitHub is queried for the list only while
	// forProvider.visibility is "selected", so the field is empty otherwise.
	// +optional
	SelectedRepositories []SecretSelectedRepoObservation `json:"selectedRepositories"`
}

// An ActionsSecretAccessSpec defines the desired state of an ActionsSecretAccess.
type ActionsSecretAccessSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              ActionsSecretAccessParameters `json:"forProvider"`

	// DriftDetection configures which forProvider fields are owned outside
	// Crossplane and how drift in those fields is detected and corrected.
	// Absent configuration means drift detection is enabled with no ignored
	// paths.
	// +optional
	DriftDetection *driftdetection.DriftDetection `json:"driftDetection,omitempty"`
}

// An ActionsSecretAccessStatus represents the observed state of an ActionsSecretAccess.
type ActionsSecretAccessStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 ActionsSecretAccessObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// An ActionsSecretAccess manages which repositories can use an existing GitHub Actions organization secret.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,github}
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.managementPolicies) || !('*' in self.spec.managementPolicies || 'Create' in self.spec.managementPolicies || 'Update' in self.spec.managementPolicies) || has(self.spec.forProvider.visibility)",message="visibility is required unless managementPolicies is Observe-only"
type ActionsSecretAccess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ActionsSecretAccessSpec   `json:"spec"`
	Status ActionsSecretAccessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ActionsSecretAccessList contains a list of ActionsSecretAccess
type ActionsSecretAccessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ActionsSecretAccess `json:"items"`
}

// ActionsSecretAccess type metadata.
var (
	ActionsSecretAccessKind             = reflect.TypeOf(ActionsSecretAccess{}).Name()
	ActionsSecretAccessGroupKind        = schema.GroupKind{Group: Group, Kind: ActionsSecretAccessKind}.String()
	ActionsSecretAccessKindAPIVersion   = ActionsSecretAccessKind + "." + SchemeGroupVersion.String()
	ActionsSecretAccessGroupVersionKind = SchemeGroupVersion.WithKind(ActionsSecretAccessKind)
)

func init() {
	SchemeBuilder.Register(&ActionsSecretAccess{}, &ActionsSecretAccessList{})
}
