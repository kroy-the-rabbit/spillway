package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SpillwayProfile declaratively replicates a set of Secrets and ConfigMaps
// from the profile's own namespace into any namespace matching the target rules.
// This lets platform teams define replication policy independently of the source
// object — no annotations needed on the source.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=swp
type SpillwayProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SpillwayProfileSpec   `json:"spec,omitempty"`
	Status SpillwayProfileStatus `json:"status,omitempty"`
}

// SpillwayProfileList contains a list of SpillwayProfile.
//
// +kubebuilder:object:root=true
type SpillwayProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpillwayProfile `json:"items"`
}

// SpillwayProfileSpec defines the desired replication policy.
type SpillwayProfileSpec struct {
	// TargetNamespaces lists explicit namespace names (and glob patterns) to
	// replicate into. Union with TargetSelector when both are set.
	// +listType=set
	// +optional
	TargetNamespaces []string `json:"targetNamespaces,omitempty"`

	// TargetSelector selects target namespaces by label. Union with
	// TargetNamespaces when both are set.
	// +kubebuilder:validation:XValidation:rule="!has(self.matchExpressions) || self.matchExpressions.all(e, e.operator in ['In', 'NotIn', 'Exists', 'DoesNotExist'])",message="matchExpressions operator must be one of In, NotIn, Exists, DoesNotExist"
	// +optional
	TargetSelector *metav1.LabelSelector `json:"targetSelector,omitempty"`

	// ExcludeNamespaces lists namespace names or glob patterns to exclude.
	// Exclusions always win over includes.
	// +listType=set
	// +optional
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// Sources lists the Secrets and ConfigMaps in the profile's own namespace
	// to replicate. Each source is replicated independently.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=name
	Sources []ProfileSource `json:"sources"`
}

// ProfileSource identifies one Secret or ConfigMap to replicate.
// +kubebuilder:validation:XValidation:rule="!(has(self.includeKeys) && size(self.includeKeys) > 0 && has(self.excludeKeys) && size(self.excludeKeys) > 0)",message="includeKeys and excludeKeys are mutually exclusive"
type ProfileSource struct {
	// Kind is "Secret" or "ConfigMap".
	// +kubebuilder:validation:Enum=Secret;ConfigMap
	Kind string `json:"kind"`

	// Name is the name of the source object in the profile's namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// IncludeKeys limits which data keys are copied into replicas (whitelist).
	// Mutually exclusive with ExcludeKeys; IncludeKeys takes precedence.
	// +listType=set
	// +optional
	IncludeKeys []string `json:"includeKeys,omitempty"`

	// ExcludeKeys removes specific data keys from replicas (blacklist).
	// +listType=set
	// +optional
	ExcludeKeys []string `json:"excludeKeys,omitempty"`
}

// SpillwayProfileStatus reflects the observed state of a SpillwayProfile.
type SpillwayProfileStatus struct {
	// ReplicatedNamespaces lists the namespaces currently receiving replicas
	// from this profile.
	// +listType=set
	// +optional
	ReplicatedNamespaces []string `json:"replicatedNamespaces,omitempty"`

	// Conditions reflect the profile's reconciliation health.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
