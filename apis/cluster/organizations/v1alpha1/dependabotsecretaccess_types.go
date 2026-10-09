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

// DependabotSecretAccessParameters are the configurable fields of a DependabotSecretAccess.
// +kubebuilder:validation:XValidation:rule="!has(self.visibility) || self.visibility != 'selected' || (has(self.selectedRepositories) && size(self.selectedRepositories) > 0)",message="selectedRepositories is required when visibility is selected"
type DependabotSecretAccessParameters struct {
	// Org is the name of the GitHub organization that owns this secret.
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1.Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization.
	// +optional
	OrgRef *xpv2.Reference `json:"orgRef,omitempty"`

	// OrgSelector selects a reference to an Organization.
	// +optional
	OrgSelector *xpv2.Selector `json:"orgSelector,omitempty"`

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

// DependabotSecretAccessObservation are the observable fields of a DependabotSecretAccess.
type DependabotSecretAccessObservation struct {
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

// A DependabotSecretAccessSpec defines the desired state of a DependabotSecretAccess.
type DependabotSecretAccessSpec struct {
	xpv2.ClusterManagedResourceSpec `json:",inline"`
	ForProvider                     DependabotSecretAccessParameters `json:"forProvider"`

	// DriftDetection configures which forProvider fields are owned outside
	// Crossplane and how drift in those fields is detected and corrected.
	// Absent configuration means drift detection is enabled with no ignored
	// paths.
	// +optional
	DriftDetection *driftdetection.DriftDetection `json:"driftDetection,omitempty"`
}

// A DependabotSecretAccessStatus represents the observed state of a DependabotSecretAccess.
type DependabotSecretAccessStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 DependabotSecretAccessObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A DependabotSecretAccess manages which repositories can use an existing Dependabot organization secret.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,github}
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.managementPolicies) || !('*' in self.spec.managementPolicies || 'Create' in self.spec.managementPolicies || 'Update' in self.spec.managementPolicies) || has(self.spec.forProvider.visibility)",message="visibility is required unless managementPolicies is Observe-only"
type DependabotSecretAccess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DependabotSecretAccessSpec   `json:"spec"`
	Status DependabotSecretAccessStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DependabotSecretAccessList contains a list of DependabotSecretAccess
type DependabotSecretAccessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DependabotSecretAccess `json:"items"`
}

// DependabotSecretAccess type metadata.
var (
	DependabotSecretAccessKind             = reflect.TypeOf(DependabotSecretAccess{}).Name()
	DependabotSecretAccessGroupKind        = schema.GroupKind{Group: Group, Kind: DependabotSecretAccessKind}.String()
	DependabotSecretAccessKindAPIVersion   = DependabotSecretAccessKind + "." + SchemeGroupVersion.String()
	DependabotSecretAccessGroupVersionKind = SchemeGroupVersion.WithKind(DependabotSecretAccessKind)
)

func init() {
	SchemeBuilder.Register(&DependabotSecretAccess{}, &DependabotSecretAccessList{})
}
