// Package v1alpha1 contains API Schema definitions for the spillway v1alpha1 API group.
//
// +kubebuilder:object:generate=true
// +groupName=spillway.kroy.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "spillway.kroy.io", Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &SpillwayProfile{}, &SpillwayProfileList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
