package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SpillwayProfile declaratively replicates a set of Secrets and ConfigMaps
// from the profile's own namespace into any namespace matching the target rules.
// This lets platform teams define replication policy independently of the source
// object — no annotations needed on the source.
type SpillwayProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SpillwayProfileSpec   `json:"spec,omitempty"`
	Status SpillwayProfileStatus `json:"status,omitempty"`
}

type SpillwayProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpillwayProfile `json:"items"`
}

type SpillwayProfileSpec struct {
	// TargetNamespaces lists explicit namespace names (and glob patterns) to
	// replicate into. Union with TargetSelector when both are set.
	TargetNamespaces []string `json:"targetNamespaces,omitempty"`

	// TargetSelector selects target namespaces by label. Union with
	// TargetNamespaces when both are set.
	TargetSelector *metav1.LabelSelector `json:"targetSelector,omitempty"`

	// ExcludeNamespaces lists namespace names or glob patterns to exclude.
	// Exclusions always win over includes.
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// Sources lists the Secrets and ConfigMaps in the profile's own namespace
	// to replicate. Each source is replicated independently.
	Sources []ProfileSource `json:"sources"`
}

// ProfileSource identifies one Secret or ConfigMap to replicate.
type ProfileSource struct {
	// Kind is "Secret" or "ConfigMap".
	Kind string `json:"kind"`

	// Name is the name of the source object in the profile's namespace.
	Name string `json:"name"`

	// IncludeKeys limits which data keys are copied into replicas (whitelist).
	// Mutually exclusive with ExcludeKeys; IncludeKeys takes precedence.
	IncludeKeys []string `json:"includeKeys,omitempty"`

	// ExcludeKeys removes specific data keys from replicas (blacklist).
	ExcludeKeys []string `json:"excludeKeys,omitempty"`
}

type SpillwayProfileStatus struct {
	// ReplicatedNamespaces lists the namespaces currently receiving replicas
	// from this profile.
	ReplicatedNamespaces []string `json:"replicatedNamespaces,omitempty"`

	// Conditions reflect the profile's reconciliation health.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DeepCopyObject implements runtime.Object.
func (in *SpillwayProfile) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *SpillwayProfile) DeepCopy() *SpillwayProfile {
	if in == nil {
		return nil
	}
	out := new(SpillwayProfile)
	in.DeepCopyInto(out)
	return out
}

func (in *SpillwayProfile) DeepCopyInto(out *SpillwayProfile) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *SpillwayProfileSpec) DeepCopyInto(out *SpillwayProfileSpec) {
	*out = *in
	if in.TargetNamespaces != nil {
		out.TargetNamespaces = make([]string, len(in.TargetNamespaces))
		copy(out.TargetNamespaces, in.TargetNamespaces)
	}
	if in.TargetSelector != nil {
		in, out := &in.TargetSelector, &out.TargetSelector
		*out = new(metav1.LabelSelector)
		(*in).DeepCopyInto(*out)
	}
	if in.ExcludeNamespaces != nil {
		out.ExcludeNamespaces = make([]string, len(in.ExcludeNamespaces))
		copy(out.ExcludeNamespaces, in.ExcludeNamespaces)
	}
	if in.Sources != nil {
		out.Sources = make([]ProfileSource, len(in.Sources))
		for i := range in.Sources {
			in.Sources[i].DeepCopyInto(&out.Sources[i])
		}
	}
}

func (in *ProfileSource) DeepCopyInto(out *ProfileSource) {
	*out = *in
	if in.IncludeKeys != nil {
		out.IncludeKeys = make([]string, len(in.IncludeKeys))
		copy(out.IncludeKeys, in.IncludeKeys)
	}
	if in.ExcludeKeys != nil {
		out.ExcludeKeys = make([]string, len(in.ExcludeKeys))
		copy(out.ExcludeKeys, in.ExcludeKeys)
	}
}

func (in *SpillwayProfileStatus) DeepCopyInto(out *SpillwayProfileStatus) {
	*out = *in
	if in.ReplicatedNamespaces != nil {
		out.ReplicatedNamespaces = make([]string, len(in.ReplicatedNamespaces))
		copy(out.ReplicatedNamespaces, in.ReplicatedNamespaces)
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		for i := range in.Conditions {
			in.Conditions[i].DeepCopyInto(&out.Conditions[i])
		}
	}
}

// DeepCopyObject implements runtime.Object.
func (in *SpillwayProfileList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *SpillwayProfileList) DeepCopy() *SpillwayProfileList {
	if in == nil {
		return nil
	}
	out := new(SpillwayProfileList)
	in.DeepCopyInto(out)
	return out
}

func (in *SpillwayProfileList) DeepCopyInto(out *SpillwayProfileList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]SpillwayProfile, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}
