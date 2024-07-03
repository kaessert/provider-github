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

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

// TeamParameters are the configurable fields of a Team.
type TeamParameters struct {
	Description string           `json:"description,omitempty"`
	Members     []TeamMemberUser `json:"members,omitempty"`

	// Org is the Organization for the Membership
	// +immutable
	// +crossplane:generate:reference:type=Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization
	// +optional
	OrgRef *xpv1.Reference `json:"orgRef,omitempty"`

	// OrgSlector selects a reference to an Organization
	// +optional
	OrgSelector *xpv1.Selector `json:"orgSelector,omitempty"`

	// Parent is the parent team of a Team
	// +crossplane:generate:reference:type=Team
	Parent *string `json:"parent,omitempty"`

	// ParentRef is a reference to a parent team
	// +optional
	ParentRef *xpv1.Reference `json:"parentRef,omitempty"`

	// ParentSlector selects a reference to an a parent team
	// +optional
	ParentSelector *xpv1.Selector `json:"parentSelector,omitempty"`

	// Privacy represents the visibility of the team (secret, closed)
	Privacy *string `json:"privacy,omitempty"`
}

type TeamMemberUser struct {
	// Name is the name of the user
	// +crossplane:generate:reference:type=Membership
	User string `json:"user,omitempty"`

	// Name is a reference to an Membership
	// +optional
	UserRef *xpv1.Reference `json:"userRef,omitempty"`

	// NameSelector selects a reference to an Organization
	// +optional
	UserSelector *xpv1.Selector `json:"userSelector,omitempty"`

	// Role is the role of the user
	Role string `json:"role"`
}

type TeamMemberTeam struct {
	// Team is the name of the team
	// +crossplane:generate:reference:type=Team
	Team string `json:"team,omitempty"`

	// TeamRef is a reference to a Team
	// +optional
	TeamRef *xpv1.Reference `json:"teamRef,omitempty"`

	// TeamSelector selects a reference to a Team
	// +optional
	TeamSelector *xpv1.Selector `json:"teamSelector,omitempty"`

	// Role is the role of the team
	Role string `json:"role"`
}

// TeamObservation are the observable fields of a Team.
type TeamObservation struct {
	ObservableField string `json:"observableField,omitempty"`
}

// A TeamSpec defines the desired state of a Team.
type TeamSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       TeamParameters `json:"forProvider"`
}

// A TeamStatus represents the observed state of a Team.
type TeamStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          TeamObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A Team is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,github}
type Team struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TeamSpec   `json:"spec"`
	Status TeamStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TeamList contains a list of Team
type TeamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Team `json:"items"`
}

// Team type metadata.
var (
	TeamKind             = reflect.TypeOf(Team{}).Name()
	TeamGroupKind        = schema.GroupKind{Group: Group, Kind: TeamKind}.String()
	TeamKindAPIVersion   = TeamKind + "." + SchemeGroupVersion.String()
	TeamGroupVersionKind = SchemeGroupVersion.WithKind(TeamKind)
)

func init() {
	SchemeBuilder.Register(&Team{}, &TeamList{})
}
