/*
Copyright 2026 Jarrad S.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ModeName is the name of a mode. Mode names are lowercase DNS labels, which
// keeps them clear of the characters the replicas annotation uses: "*", "="
// and ",".
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
type ModeName string

// ClusterModeDefinitionSpec lists the allowed modes and the default mode.
// +kubebuilder:validation:XValidation:rule="self.defaultMode in self.modes",message="defaultMode must be one of modes"
type ClusterModeDefinitionSpec struct {
	// modes lists the modes that ClusterModes using this definition may be in.
	// Names are unique. The limit of 32 keeps CEL rules that search the list
	// within the API server's cost budget.
	// +required
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Modes []ModeName `json:"modes"`

	// defaultMode is the mode the mode manager starts in when there is no mode
	// to restore: when it first creates a ClusterMode, after a full cluster
	// reboot, and when the stored mode is no longer allowed. It must be one of
	// modes.
	// +required
	DefaultMode ModeName `json:"defaultMode"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Default",type=string,JSONPath=`.spec.defaultMode`
// +kubebuilder:printcolumn:name="Modes",type=string,JSONPath=`.spec.modes`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterModeDefinition lists the modes that the ClusterModes using it may be
// in, and the mode to start in when there is no mode to restore. Admins write
// it, normally through Git; the mode manager and mode-operator only read it.
// It has no status.
type ClusterModeDefinition struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec lists the allowed modes and the default mode.
	// +required
	Spec ClusterModeDefinitionSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ClusterModeDefinitionList contains a list of ClusterModeDefinition
type ClusterModeDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ClusterModeDefinition `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ClusterModeDefinition{}, &ClusterModeDefinitionList{})
		return nil
	})
}
