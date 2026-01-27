// Package v1alpha1 contains API Schema definitions for the migration v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=migration.openshift.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "migration.openshift.io", Version: "v1alpha1"}
)
