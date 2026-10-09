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

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane/provider-github/apis/common/driftdetection"
)

// OrganizationWebhookParameters are the configurable fields of an OrganizationWebhook.
type OrganizationWebhookParameters struct {
	// Org is the name of the GitHub organization that owns this webhook.
	// +crossplane:generate:reference:type=github.com/crossplane/provider-github/apis/namespaced/organizations/v1alpha1.Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization.
	// +optional
	OrgRef *xpv2.NamespacedReference `json:"orgRef,omitempty"`

	// OrgSelector selects a reference to an Organization.
	// +optional
	OrgSelector *xpv2.NamespacedSelector `json:"orgSelector,omitempty"`

	// The URL to which the payloads will be delivered. An existing
	// webhook with this URL is adopted.
	// +optional
	URL string `json:"url,omitempty"`

	// The media type used to serialize the payloads. Supported values include json and form.
	// +kubebuilder:validation:Enum=json;form
	// +optional
	ContentType string `json:"contentType,omitempty"`

	// Determines what events the hook is triggered for. See https://docs.github.com/en/webhooks/webhook-events-and-payloads
	// +kubebuilder:validation:MinItems=1
	// +optional
	Events []string `json:"events"`

	// Determines if notifications are sent when the webhook is triggered.
	// Default: true
	// +optional
	Active *bool `json:"active,omitempty"`

	// Determines whether the SSL certificate of the host for url will be verified when delivering payloads.
	// We strongly recommend not setting this to true as you are subject to man-in-the-middle and other attacks.
	// Default: false
	// +optional
	InsecureSSL *bool `json:"insecureSsl,omitempty"`

	// Reference to a secret key containing the webhook secret.
	// You can use the webhook secret to limit incoming requests to only those originating from GitHub.
	// For more information, see https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
	// Requires spec.writeConnectionSecretToRef, where the applied secret is recorded.
	// The Secret is always read from the namespace of this resource; a namespace set on the selector is ignored.
	// +optional
	SecretKeyRef *xpv2.SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// OrganizationWebhookObservation are the observable fields of an OrganizationWebhook.
type OrganizationWebhookObservation struct {
	// ID is the GitHub webhook ID.
	ID int64 `json:"id,omitempty"`

	// Org is the organization the webhook was found under.
	Org string `json:"org,omitempty"`

	// URL is the URL to which GitHub delivers payloads.
	URL string `json:"url,omitempty"`

	// ContentType is the media type GitHub uses to serialize payloads.
	ContentType string `json:"contentType,omitempty"`

	// Events lists what the hook is triggered for.
	// +optional
	Events []string `json:"events"`

	// Active is whether notifications are sent when the webhook is triggered.
	Active *bool `json:"active,omitempty"`

	// InsecureSSL is whether GitHub skips verifying the SSL certificate of
	// the host for url.
	InsecureSSL *bool `json:"insecureSsl,omitempty"`

	// SecretKeyRef is excluded from the observation: the webhook secret is
	// write-only material that GitHub never returns, and the reference to it
	// is a spec input.
}

// An OrganizationWebhookSpec defines the desired state of an OrganizationWebhook.
type OrganizationWebhookSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`
	ForProvider              OrganizationWebhookParameters `json:"forProvider"`

	// DriftDetection configures which forProvider fields are owned outside
	// Crossplane and how drift in those fields is detected and corrected.
	// Absent configuration means drift detection is enabled with no ignored
	// paths.
	// +optional
	DriftDetection *driftdetection.DriftDetection `json:"driftDetection,omitempty"`
}

// An OrganizationWebhookStatus represents the observed state of an OrganizationWebhook.
type OrganizationWebhookStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 OrganizationWebhookObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// An OrganizationWebhook is a GitHub organization webhook.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,github}
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.managementPolicies) || !('*' in self.spec.managementPolicies || 'Create' in self.spec.managementPolicies || 'Update' in self.spec.managementPolicies) || has(self.spec.forProvider.url)",message="url is required unless managementPolicies is Observe-only"
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.managementPolicies) || !('*' in self.spec.managementPolicies || 'Create' in self.spec.managementPolicies || 'Update' in self.spec.managementPolicies) || has(self.spec.forProvider.contentType)",message="contentType is required unless managementPolicies is Observe-only"
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.managementPolicies) || !('*' in self.spec.managementPolicies || 'Create' in self.spec.managementPolicies || 'Update' in self.spec.managementPolicies) || has(self.spec.forProvider.events)",message="events is required unless managementPolicies is Observe-only"
type OrganizationWebhook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OrganizationWebhookSpec   `json:"spec"`
	Status OrganizationWebhookStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OrganizationWebhookList contains a list of OrganizationWebhook
type OrganizationWebhookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OrganizationWebhook `json:"items"`
}

// OrganizationWebhook type metadata.
var (
	OrganizationWebhookKind             = reflect.TypeOf(OrganizationWebhook{}).Name()
	OrganizationWebhookGroupKind        = schema.GroupKind{Group: Group, Kind: OrganizationWebhookKind}.String()
	OrganizationWebhookKindAPIVersion   = OrganizationWebhookKind + "." + SchemeGroupVersion.String()
	OrganizationWebhookGroupVersionKind = SchemeGroupVersion.WithKind(OrganizationWebhookKind)
)

func init() {
	SchemeBuilder.Register(&OrganizationWebhook{}, &OrganizationWebhookList{})
}
