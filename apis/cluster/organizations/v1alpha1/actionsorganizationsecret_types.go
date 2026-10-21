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

	"github.com/provider-github/provider-github/apis/common/driftdetection"
)

// ActionsOrganizationSecretParameters are the configurable fields of an ActionsOrganizationSecret.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.org) || has(self.org)",message="org cannot be removed once set — it is immutable after creation"
type ActionsOrganizationSecretParameters struct {
	// Org is the name of the GitHub organization that owns this secret. It is
	// immutable after creation.
	// +crossplane:generate:reference:type=github.com/provider-github/provider-github/apis/cluster/organizations/v1alpha1.Organization
	// +kubebuilder:validation:XValidation:rule="self == oldSelf || oldSelf == ''",message="org is immutable after creation"
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization.
	// +optional
	OrgRef *xpv2.Reference `json:"orgRef,omitempty"`

	// OrgSelector selects a reference to an Organization.
	// +optional
	OrgSelector *xpv2.Selector `json:"orgSelector,omitempty"`

	// ValueSecretRef references the Kubernetes Secret key holding the
	// plaintext value of the GitHub secret. The provider seals the value with
	// the organization public key before sending it; GitHub never returns it.
	// The Secret is always read from the namespace of this resource when the
	// resource is namespaced; a namespace set on the selector is ignored.
	// +optional
	ValueSecretRef *xpv2.SecretKeySelector `json:"valueSecretRef,omitempty"`

	// Visibility controls which repositories can use this secret.
	// +kubebuilder:validation:Enum=all;private;selected
	// +optional
	Visibility string `json:"visibility,omitempty"`

	// SelectedRepositories lists the repositories that can use the secret
	// when Visibility is "selected". When omitted, the provider keeps the
	// list GitHub already holds for the secret instead of clearing it.
	// +optional
	SelectedRepositories []SecretSelectedRepo `json:"selectedRepositories"`
}

// ActionsOrganizationSecretObservation are the observable fields of an
// ActionsOrganizationSecret. ValueSecretRef is not mirrored: the value is
// write-only material that GitHub never returns, and the reference to it is a
// spec input.
type ActionsOrganizationSecretObservation struct {
	// ID is the external name of the secret on GitHub.
	ID string `json:"id,omitempty"`

	// Org is the organization the secret was found under.
	Org string `json:"org,omitempty"`

	// Name is the secret name as GitHub reports it, which is the upper-cased
	// form of the external name.
	Name string `json:"name,omitempty"`

	// Visibility is the visibility GitHub reports for the secret.
	Visibility string `json:"visibility,omitempty"`

	// SelectedRepositories lists the repositories that can use the secret on
	// GitHub. GitHub is queried for the list only while the secret's
	// visibility is "selected", so the field is empty otherwise.
	// +optional
	SelectedRepositories []SecretSelectedRepoObservation `json:"selectedRepositories"`

	// CreatedAt is the time GitHub reports the secret was created, in RFC 3339 format.
	CreatedAt string `json:"createdAt,omitempty"`

	// UpdatedAt is the time GitHub reports the secret was last written, in RFC 3339 format.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ActionsOrganizationSecretWriteRecord holds the non-secret records the
// provider keeps about its own last successful write of the secret value.
// GitHub never returns the value, so these records are how a change to the
// value is detected. Nothing derived from the value is stored here.
type ActionsOrganizationSecretWriteRecord struct {
	// SecretUID is the UID of the Kubernetes Secret referenced by
	// forProvider.valueSecretRef at the last successful write.
	SecretUID string `json:"secretUID,omitempty"`

	// SecretResourceVersion is the resourceVersion of the Kubernetes Secret
	// referenced by forProvider.valueSecretRef at the last successful write.
	SecretResourceVersion string `json:"secretResourceVersion,omitempty"`

	// UpdatedAt is the updated_at timestamp GitHub reported when the provider
	// read the secret back right after its own last write, in RFC 3339
	// format. A later updated_at on GitHub means someone else wrote the secret.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// An ActionsOrganizationSecretSpec defines the desired state of an ActionsOrganizationSecret.
type ActionsOrganizationSecretSpec struct {
	xpv2.ClusterManagedResourceSpec `json:",inline"`
	ForProvider                     ActionsOrganizationSecretParameters `json:"forProvider"`

	// DriftDetection configures which forProvider fields are owned outside
	// Crossplane and how drift in those fields is detected and corrected.
	// Absent configuration means drift detection is enabled with no ignored
	// paths.
	// +optional
	DriftDetection *driftdetection.DriftDetection `json:"driftDetection,omitempty"`
}

// An ActionsOrganizationSecretStatus represents the observed state of an ActionsOrganizationSecret.
type ActionsOrganizationSecretStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 ActionsOrganizationSecretObservation `json:"atProvider,omitempty"`

	// LastWrite records the referenced Secret's identity and GitHub's
	// updated_at as of the provider's last successful write of the value.
	// +optional
	LastWrite *ActionsOrganizationSecretWriteRecord `json:"lastWrite,omitempty"`
}

// +kubebuilder:object:root=true

// An ActionsOrganizationSecret manages a GitHub Actions organization secret: its name, value, visibility and selected repositories.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,github}
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.managementPolicies) || !('*' in self.spec.managementPolicies || 'Create' in self.spec.managementPolicies || 'Update' in self.spec.managementPolicies) || has(self.spec.forProvider.valueSecretRef)",message="valueSecretRef is required unless managementPolicies is Observe-only"
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.managementPolicies) || !('*' in self.spec.managementPolicies || 'Create' in self.spec.managementPolicies || 'Update' in self.spec.managementPolicies) || has(self.spec.forProvider.visibility)",message="visibility is required unless managementPolicies is Observe-only"
type ActionsOrganizationSecret struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ActionsOrganizationSecretSpec   `json:"spec"`
	Status ActionsOrganizationSecretStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ActionsOrganizationSecretList contains a list of ActionsOrganizationSecret
type ActionsOrganizationSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ActionsOrganizationSecret `json:"items"`
}

// ActionsOrganizationSecret type metadata.
var (
	ActionsOrganizationSecretKind             = reflect.TypeOf(ActionsOrganizationSecret{}).Name()
	ActionsOrganizationSecretGroupKind        = schema.GroupKind{Group: Group, Kind: ActionsOrganizationSecretKind}.String()
	ActionsOrganizationSecretKindAPIVersion   = ActionsOrganizationSecretKind + "." + SchemeGroupVersion.String()
	ActionsOrganizationSecretGroupVersionKind = SchemeGroupVersion.WithKind(ActionsOrganizationSecretKind)
)

func init() {
	SchemeBuilder.Register(&ActionsOrganizationSecret{}, &ActionsOrganizationSecretList{})
}
