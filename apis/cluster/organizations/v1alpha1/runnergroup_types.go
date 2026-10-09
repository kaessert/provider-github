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
)

// RunnerGroupSelectedRepo references a repository that has access to a
// runner group whose visibility is "selected".
type RunnerGroupSelectedRepo struct {
	// Name of the repository.
	// +crossplane:generate:reference:type=Repository
	Repo string `json:"repo,omitempty"`

	// RepoRef is a reference to a Repository.
	// +optional
	RepoRef *xpv2.Reference `json:"repoRef,omitempty"`

	// RepoSelector selects a reference to a Repository.
	// +optional
	RepoSelector *xpv2.Selector `json:"repoSelector,omitempty"`
}

// WorkflowRef is a workflow allowed to use a runner group, as
// owner/repo/.github/workflows/file.yml@ref.
// +kubebuilder:validation:Pattern=`^[^/@]+/[^/@]+/\.github/workflows/[^/@]+@(refs/heads/.+|refs/tags/.+|[0-9a-f]{40})$`
// +kubebuilder:validation:MaxLength=512
type WorkflowRef string

// RunnerGroupParameters are the configurable fields of a RunnerGroup.
type RunnerGroupParameters struct {
	// Org is the name of the GitHub organization that owns this runner group.
	// +crossplane:generate:reference:type=Organization
	Org string `json:"org,omitempty"`

	// OrgRef is a reference to an Organization.
	// +optional
	OrgRef *xpv2.Reference `json:"orgRef,omitempty"`

	// OrgSelector selects a reference to an Organization.
	// +optional
	OrgSelector *xpv2.Selector `json:"orgSelector,omitempty"`

	// Visibility controls which repositories can use this runner group.
	// +kubebuilder:validation:Enum=all;selected;private
	Visibility string `json:"visibility"`

	// SelectedRepositories lists repositories that can use the runner
	// group. Only used when Visibility is "selected".
	// +optional
	SelectedRepositories []RunnerGroupSelectedRepo `json:"selectedRepositories,omitempty"`

	// AllowsPublicRepositories lets public repositories use the runner group.
	// Default: false
	// +optional
	AllowsPublicRepositories bool `json:"allowsPublicRepositories,omitempty"`

	// SelectedWorkflows lists the workflows allowed to use the runner
	// group, as owner/repo/.github/workflows/file.yml@ref, where ref is
	// refs/heads/<branch>, refs/tags/<tag> or a full 40-character commit
	// SHA. Setting it restricts the runner group to these workflows.
	// +optional
	SelectedWorkflows []WorkflowRef `json:"selectedWorkflows,omitempty"`
}

// RunnerGroupObservation are the observable fields of a RunnerGroup.
type RunnerGroupObservation struct {
	// ID is the GitHub runner group ID.
	ID int64 `json:"id,omitempty"`
}

// A RunnerGroupSpec defines the desired state of a RunnerGroup.
type RunnerGroupSpec struct {
	xpv2.ClusterManagedResourceSpec `json:",inline"`
	ForProvider                     RunnerGroupParameters `json:"forProvider"`
}

// A RunnerGroupStatus represents the observed state of a RunnerGroup.
type RunnerGroupStatus struct {
	xpv2.ManagedResourceStatus `json:",inline"`
	AtProvider                 RunnerGroupObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true

// A RunnerGroup is a GitHub Actions organization runner group.
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,github}
type RunnerGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunnerGroupSpec   `json:"spec"`
	Status RunnerGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RunnerGroupList contains a list of RunnerGroup
type RunnerGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RunnerGroup `json:"items"`
}

// RunnerGroup type metadata.
var (
	RunnerGroupKind             = reflect.TypeOf(RunnerGroup{}).Name()
	RunnerGroupGroupKind        = schema.GroupKind{Group: Group, Kind: RunnerGroupKind}.String()
	RunnerGroupKindAPIVersion   = RunnerGroupKind + "." + SchemeGroupVersion.String()
	RunnerGroupGroupVersionKind = SchemeGroupVersion.WithKind(RunnerGroupKind)
)

func init() {
	SchemeBuilder.Register(&RunnerGroup{}, &RunnerGroupList{})
}
