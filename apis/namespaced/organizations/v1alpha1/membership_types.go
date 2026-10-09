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

// MembershipParameters are the configurable fields of a Membership.
type MembershipParameters struct {
	Role string `json:"role"`

	// Org is the Organization for the Membership
	// +immutable
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1.Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization
	// +optional
	OrgRef *xpv2.NamespacedReference `json:"orgRef,omitempty"`

	// OrgSlector selects a reference to an Organization
	// +optional
	OrgSelector *xpv2.NamespacedSelector `json:"orgSelector,omitempty"`
}

// MembershipObservation are the observable fields of a Membership.
type MembershipObservation struct {
	ObservableField string `json:"observableField,omitempty"`
}

// A MembershipSpec defines the desired state of a Membership.
type MembershipSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              MembershipParameters `json:"forProvider"`
}

// A MembershipStatus represents the observed state of a Membership.
type MembershipStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 MembershipObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A Membership is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,github}
type Membership struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MembershipSpec   `json:"spec"`
	Status MembershipStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MembershipList contains a list of Membership
type MembershipList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Membership `json:"items"`
}

// Membership type metadata.
var (
	MembershipKind             = reflect.TypeOf(Membership{}).Name()
	MembershipGroupKind        = schema.GroupKind{Group: Group, Kind: MembershipKind}.String()
	MembershipKindAPIVersion   = MembershipKind + "." + SchemeGroupVersion.String()
	MembershipGroupVersionKind = SchemeGroupVersion.WithKind(MembershipKind)
)

func init() {
	SchemeBuilder.Register(&Membership{}, &MembershipList{})
}
