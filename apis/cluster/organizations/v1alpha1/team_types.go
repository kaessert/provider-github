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

// TeamParameters are the configurable fields of a Team.
type TeamParameters struct {
	Description string `json:"description,omitempty"`

	// +optional
	Members []TeamMemberUser `json:"members"`

	// Org is the Organization for the Membership
	// +immutable
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1.Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization
	// +optional
	OrgRef *xpv2.Reference `json:"orgRef,omitempty"`

	// OrgSlector selects a reference to an Organization
	// +optional
	OrgSelector *xpv2.Selector `json:"orgSelector,omitempty"`

	// Parent is the parent team of a Team
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1.Team
	Parent *string `json:"parent,omitempty"`

	// ParentRef is a reference to a parent team
	// +optional
	ParentRef *xpv2.Reference `json:"parentRef,omitempty"`

	// ParentSlector selects a reference to an a parent team
	// +optional
	ParentSelector *xpv2.Selector `json:"parentSelector,omitempty"`

	// Privacy represents the visibility of the team (secret, closed)
	Privacy *string `json:"privacy,omitempty"`
}

type TeamMemberUser struct {
	// Name is the name of the user
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1.Membership
	User string `json:"user,omitempty"`

	// Name is a reference to an Membership
	// +optional
	UserRef *xpv2.Reference `json:"userRef,omitempty"`

	// NameSelector selects a reference to an Organization
	// +optional
	UserSelector *xpv2.Selector `json:"userSelector,omitempty"`

	// Role is the role of the user
	Role string `json:"role"`
}

type TeamMemberTeam struct {
	// Team is the name of the team
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/cluster/organizations/v1alpha1.Team
	Team string `json:"team,omitempty"`

	// TeamRef is a reference to a Team
	// +optional
	TeamRef *xpv2.Reference `json:"teamRef,omitempty"`

	// TeamSelector selects a reference to a Team
	// +optional
	TeamSelector *xpv2.Selector `json:"teamSelector,omitempty"`

	// Role is the role of the team
	Role string `json:"role"`
}

// TeamMemberUserObservation is a member of a team and the role GitHub reports for them.
type TeamMemberUserObservation struct {
	// User is the login of the member.
	User string `json:"user,omitempty"`

	// Role is the role of the member on the team.
	Role string `json:"role,omitempty"`
}

// TeamObservation are the observable fields of a Team.
type TeamObservation struct {
	// ID is the external name of the team on GitHub.
	ID string `json:"id,omitempty"`

	// Description is the description GitHub reports for the team.
	Description string `json:"description,omitempty"`

	// Members lists the direct members of the team and their roles. Members
	// that the team only inherits from its child teams are not listed.
	// +optional
	Members []TeamMemberUserObservation `json:"members"`

	// Org is the organization the team was found under.
	Org string `json:"org,omitempty"`

	// Parent is the name of the parent team, when the team has one.
	Parent *string `json:"parent,omitempty"`

	// Privacy represents the visibility of the team (secret, closed)
	Privacy *string `json:"privacy,omitempty"`

	ObservableField string `json:"observableField,omitempty"`
}

// A TeamSpec defines the desired state of a Team.
type TeamSpec struct {
	xpv2.ClusterManagedResourceSpec `json:",inline"`
	ForProvider                     TeamParameters `json:"forProvider"`

	// DriftDetection configures which forProvider fields are owned outside
	// Crossplane and how drift in those fields is detected and corrected.
	// Absent configuration means drift detection is enabled with no ignored
	// paths.
	// +optional
	DriftDetection *driftdetection.DriftDetection `json:"driftDetection,omitempty"`
}

// A TeamStatus represents the observed state of a Team.
type TeamStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 TeamObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A Team is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="MEMBERSHIP-PARTIAL",type="string",JSONPath=".status.conditions[?(@.type=='TeamMembershipPartial')].status"
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
