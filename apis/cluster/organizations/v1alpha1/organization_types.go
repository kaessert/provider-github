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

// ActionsConfiguration are the configurable fields of an Organization Actions.
type ActionsConfiguration struct {
	// EnabledRepos is the set of repositories allowed to run GitHub
	// Actions at the organization level (equivalent to GitHub's
	// "selected_repository_ids" list when the org's Actions permission
	// policy is "selected"). The provider reconciles the org's enabled
	// list to match this field via a single atomic PUT.
	//
	// The field is tri-state:
	//   - omitted / nil: the provider does not manage the enabled list;
	//     whatever is already configured on GitHub is left untouched.
	//   - empty slice ([]): the provider sets the enabled list to empty,
	//     disabling Actions for every repository in the org.
	//   - non-empty: the provider sets the enabled list to exactly these
	//     repositories, adding any missing and removing any extras.
	//
	// To stop managing the list, remove the field. Setting it to [] is
	// an explicit "wipe" and will affect every repo in the org.
	// +optional
	EnabledRepos []ActionEnabledRepo `json:"enabledRepos,omitempty"`
}

type ActionEnabledRepo struct {
	// Name of the repository
	// +crossplane:generate:reference:type=Repository
	Repo string `json:"repo,omitempty"`

	// RepoRef is a reference to the Repositories
	// +optional
	RepoRef *xpv2.Reference `json:"repoRef,omitempty"`

	// RepoSelector selects a reference to an Repositories
	// +optional
	RepoSelector *xpv2.Selector `json:"repoSelector,omitempty"`
}

type SecretSelectedRepo struct {
	// Name of the repository
	// +crossplane:generate:reference:type=Repository
	Repo string `json:"repo,omitempty"`

	// RepoRef is a reference to the Repositories
	// +optional
	RepoRef *xpv2.Reference `json:"repoRef,omitempty"`

	// RepoSelector selects a reference to a Repository
	// +optional
	RepoSelector *xpv2.Selector `json:"repoSelector,omitempty"`
}

type OrgSecret struct {
	// Name of the GitHub secret
	Name string `json:"name"`

	// List of repositories that have access to the secret.
	RepositoryAccessList []SecretSelectedRepo `json:"repositoryAccessList,omitempty"`
}

type SecretConfiguration struct {
	// List of GitHub Actions secrets
	//
	// DEPRECATED, use ActionsSecretAccess instead.
	// +optional
	ActionsSecrets []OrgSecret `json:"actionsSecrets,omitempty"`

	// List of Dependabot secrets
	//
	// DEPRECATED, use DependabotSecretAccess instead.
	// +optional
	DependabotSecrets []OrgSecret `json:"dependabotSecrets,omitempty"`
}

// OrganizationParameters are the configurable fields of a Organization.
type OrganizationParameters struct {
	Description string               `json:"description"`
	Actions     ActionsConfiguration `json:"actions,omitempty"`

	// Configuration for Organization Secrets.
	// +optional
	Secrets *SecretConfiguration `json:"secrets,omitempty"`
}

// OrganizationObservation are the observable fields of a Organization.
type OrganizationObservation struct {
	Description string `json:"description,omitempty"`
}

// A OrganizationSpec defines the desired state of a Organization.
type OrganizationSpec struct {
	xpv2.ClusterManagedResourceSpec `json:",inline"`
	ForProvider                     OrganizationParameters `json:"forProvider"`
}

// A OrganizationStatus represents the observed state of a Organization.
type OrganizationStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 OrganizationObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A Organization is an example API type.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,github}
type Organization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OrganizationSpec   `json:"spec"`
	Status OrganizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OrganizationList contains a list of Organization
type OrganizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Organization `json:"items"`
}

// Organization type metadata.
var (
	OrganizationKind             = reflect.TypeOf(Organization{}).Name()
	OrganizationGroupKind        = schema.GroupKind{Group: Group, Kind: OrganizationKind}.String()
	OrganizationKindAPIVersion   = OrganizationKind + "." + SchemeGroupVersion.String()
	OrganizationGroupVersionKind = SchemeGroupVersion.WithKind(OrganizationKind)
)

func init() {
	SchemeBuilder.Register(&Organization{}, &OrganizationList{})
}
