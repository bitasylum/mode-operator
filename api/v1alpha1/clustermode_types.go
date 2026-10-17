/*
Copyright 2026 Bitasylum

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

// Condition types reported in ClusterMode status.
const (
	// ConditionApplied is True when every workload that isn't Invalid has the
	// replicas the mode asks for.
	ConditionApplied = "Applied"
	// ConditionReady is True when every workload that isn't Invalid is Settled.
	ConditionReady = "Ready"
	// ConditionDegraded is True when any workload has an annotation error or is
	// Stalled, or when the mode can't be applied at all because the definition
	// is missing or doesn't list it.
	ConditionDegraded = "Degraded"
)

// Condition reasons reported in ClusterMode status.
const (
	ReasonAllApplied                 = "AllApplied"
	ReasonAllSettled                 = "AllSettled"
	ReasonNoWorkloads                = "NoWorkloads"
	ReasonApplying                   = "Applying"
	ReasonScaleToZeroPending         = "ScaleToZeroPending"
	ReasonScaling                    = "Scaling"
	ReasonStalled                    = "Stalled"
	ReasonInvalidMode                = "InvalidMode"
	ReasonDefinitionNotFound         = "DefinitionNotFound"
	ReasonInvalidWorkloads           = "InvalidWorkloads"
	ReasonStalledWorkloads           = "StalledWorkloads"
	ReasonInvalidAndStalledWorkloads = "InvalidAndStalledWorkloads"
	ReasonAsExpected                 = "AsExpected"
)

// WorkloadState is the state of one workload that follows a ClusterMode.
// Every workload is in exactly one state.
type WorkloadState string

// Workload states, in the order they are checked.
const (
	// WorkloadInvalid means an annotation error is stopping mode-operator from
	// applying the mode. Replicas are left untouched.
	WorkloadInvalid WorkloadState = "Invalid"
	// WorkloadPending means the workload is waiting out its scale-to-zero delay.
	WorkloadPending WorkloadState = "Pending"
	// WorkloadApplying means mode-operator hasn't written the workload's count
	// for the current mode yet.
	WorkloadApplying WorkloadState = "Applying"
	// WorkloadStalled means the workload hasn't reached its count within its
	// settle timeout.
	WorkloadStalled WorkloadState = "Stalled"
	// WorkloadScaling means the count is applied but the workload hasn't
	// reached it yet.
	WorkloadScaling WorkloadState = "Scaling"
	// WorkloadSettled means the count is applied and the workload has reached it.
	WorkloadSettled WorkloadState = "Settled"
)

// ClusterModeSpec selects the ClusterModeDefinition to follow and holds the
// current mode. The mode manager writes it; maintainers may change mode by hand.
type ClusterModeSpec struct {
	// definition names the ClusterModeDefinition that lists the modes this
	// ClusterMode may be in. It can't be changed once set.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="definition can't be changed once set"
	Definition string `json:"definition"`

	// mode is the current mode. It must be one of the definition's modes. When
	// it changes, the API server checks it against status.modes, mode-operator's
	// copy of those modes.
	// +required
	Mode ModeName `json:"mode"`
}

// WorkloadCounts counts the workloads that follow a ClusterMode, by state.
type WorkloadCounts struct {
	// total is the sum of the other counts.
	// +optional
	Total int32 `json:"total"`
	// settled counts workloads whose count is applied and reached.
	// +optional
	Settled int32 `json:"settled"`
	// scaling counts workloads whose count is applied but not yet reached.
	// +optional
	Scaling int32 `json:"scaling"`
	// stalled counts workloads that haven't reached their count within their
	// settle timeout.
	// +optional
	Stalled int32 `json:"stalled"`
	// applying counts workloads that mode-operator hasn't written yet.
	// +optional
	Applying int32 `json:"applying"`
	// pending counts workloads waiting out their scale-to-zero delay.
	// +optional
	Pending int32 `json:"pending"`
	// invalid counts workloads that an annotation error stops mode-operator
	// from changing.
	// +optional
	Invalid int32 `json:"invalid"`
}

// ReplicaCounts sums replica counts over the workloads that aren't Invalid.
// A VirtualMachine counts as one replica when it is on.
type ReplicaCounts struct {
	// desired is what the current mode asks for.
	// +optional
	Desired int32 `json:"desired"`
	// applied is what mode-operator has set: spec.replicas, or 1 for a running
	// VirtualMachine. It is above desired while scale-to-zero delays run.
	// +optional
	Applied int32 `json:"applied"`
	// ready is what the workloads report as ready.
	// +optional
	Ready int32 `json:"ready"`
}

// UnsettledWorkload is one workload that isn't Settled.
type UnsettledWorkload struct {
	// kind is the workload's kind, for example Deployment or VirtualMachine.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`
	// namespace is the workload's namespace.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// name is the workload's name.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// state is the workload's state.
	// +required
	// +kubebuilder:validation:Enum=Stalled;Invalid;Scaling;Applying;Pending
	State WorkloadState `json:"state"`
	// desired is the count the current mode asks for. It is omitted when the
	// workload's replicas annotation is invalid.
	// +optional
	Desired *int32 `json:"desired,omitempty"`
	// applied is the workload's current count.
	// +optional
	Applied int32 `json:"applied"`
	// ready is the workload's ready count, as it reports it. A StatefulSet
	// counts its ready pods above its count too, so this can be above
	// desired while those pods are still to stop.
	// +optional
	Ready int32 `json:"ready"`
	// since is when the workload entered its state, where that is known.
	// +optional
	Since *metav1.Time `json:"since,omitempty"`
	// message explains the state, for example "4 of 6 pods ready" or an
	// annotation error.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
}

// ClusterModeStatus reports how far the current mode has been applied.
// mode-operator is its only writer.
type ClusterModeStatus struct {
	// observedGeneration is the metadata.generation this status was computed
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// modes is mode-operator's copy of the definition's modes. Changes to
	// spec.mode are checked against it. It keeps its last value if the
	// definition is deleted.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	Modes []ModeName `json:"modes,omitempty"`

	// defaultMode is mode-operator's copy of the definition's defaultMode.
	// Changes of spec.mode to it are always allowed.
	// +optional
	DefaultMode ModeName `json:"defaultMode,omitempty"`

	// transitions is mode-operator's copy of the definition's transitions.
	// When it is set, a change of spec.mode must follow it, and must wait
	// until the current mode is Ready. Like modes, it keeps its last value if
	// the definition is deleted.
	// +optional
	// +listType=map
	// +listMapKey=from
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Transitions []ModeTransition `json:"transitions,omitempty"`

	// mode is the mode this status describes.
	// +optional
	Mode ModeName `json:"mode,omitempty"`

	// modeChangedAt is when mode-operator first saw the current mode.
	// +optional
	ModeChangedAt *metav1.Time `json:"modeChangedAt,omitempty"`

	// modeSetBy is the field manager that owns spec.mode: mode-manager
	// normally, or the tool a maintainer used for a manual edit.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ModeSetBy string `json:"modeSetBy,omitempty"`

	// workloads counts the workloads that follow this ClusterMode, by state.
	// +optional
	Workloads WorkloadCounts `json:"workloads,omitzero"`

	// invalidAnnotations is the number of workloads with any annotation error,
	// including errors that aren't blocking anything yet. It isn't a state and
	// isn't part of workloads.total.
	// +optional
	InvalidAnnotations int32 `json:"invalidAnnotations,omitempty"`

	// replicas sums replica counts over the workloads that aren't Invalid.
	// +optional
	Replicas ReplicaCounts `json:"replicas,omitzero"`

	// unsettled lists up to 20 workloads that aren't Settled, most urgent
	// first. The counts in workloads always cover every workload.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Unsettled []UnsettledWorkload `json:"unsettled,omitempty"`

	// conditions are Applied, Ready and Degraded.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="name must be 63 characters or fewer, so that it can be used as a label value"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == oldSelf.spec.mode || !has(self.status) || !has(self.status.modes) || self.spec.mode in self.status.modes",message="spec.mode must be one of the modes in the ClusterModeDefinition (status.modes)",fieldPath=".spec.mode"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == oldSelf.spec.mode || !has(self.status) || !has(self.status.transitions) || (has(self.status.defaultMode) && self.spec.mode == self.status.defaultMode) || self.status.transitions.exists(t, t.from == oldSelf.spec.mode && self.spec.mode in t.to)",message="the ClusterModeDefinition's transitions don't allow this change of spec.mode (status.transitions)",fieldPath=".spec.mode"
// +kubebuilder:validation:XValidation:rule="self.spec.mode == oldSelf.spec.mode || !has(self.status) || !has(self.status.transitions) || (has(self.status.defaultMode) && self.spec.mode == self.status.defaultMode) || (has(self.status.mode) && self.status.mode == oldSelf.spec.mode && has(self.status.conditions) && self.status.conditions.exists(c, c.type == 'Ready' && c.status == 'True'))",message="spec.mode can't change until the current mode is Ready, because the ClusterModeDefinition has transitions",fieldPath=".spec.mode"
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Applied",type=string,JSONPath=`.status.conditions[?(@.type=="Applied")].reason`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Workloads",type=integer,JSONPath=`.status.workloads.total`
// +kubebuilder:printcolumn:name="Settled",type=integer,JSONPath=`.status.workloads.settled`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Desired-Replicas",type=integer,JSONPath=`.status.replicas.desired`,priority=1
// +kubebuilder:printcolumn:name="Applied-Replicas",type=integer,JSONPath=`.status.replicas.applied`,priority=1
// +kubebuilder:printcolumn:name="Ready-Replicas",type=integer,JSONPath=`.status.replicas.ready`,priority=1
// +kubebuilder:printcolumn:name="Set-By",type=string,JSONPath=`.status.modeSetBy`,priority=1
// +kubebuilder:printcolumn:name="Definition",type=string,JSONPath=`.spec.definition`,priority=1

// ClusterMode holds a cluster's current mode, and reports in status how far
// mode-operator has applied it to the workloads that follow it.
type ClusterMode struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec names the definition and holds the current mode.
	// +required
	Spec ClusterModeSpec `json:"spec"`

	// status reports how far the current mode has been applied.
	// +optional
	Status ClusterModeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterModeList contains a list of ClusterMode
type ClusterModeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ClusterMode `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ClusterMode{}, &ClusterModeList{})
		return nil
	})
}
