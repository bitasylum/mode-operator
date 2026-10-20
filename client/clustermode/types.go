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

package clustermode

import (
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// Group is the API group of ClusterModes and ClusterModeDefinitions.
	Group = "mode.bitasylum.org"
	// Version is the API version the package reads and writes.
	Version = "v1alpha1"

	// ModeManagerFieldManager is the field manager the mode manager writes
	// with.
	ModeManagerFieldManager = "mode-manager"
	// WriterLabel is the label that names the mode manager writing a
	// ClusterMode.
	WriterLabel = "mode.bitasylum.org/writer"

	// ConditionApplied is True when every workload has the mode's replicas.
	ConditionApplied = "Applied"
	// ConditionReady is True when every workload has settled in the mode.
	ConditionReady = "Ready"
	// ConditionDegraded is True when a workload is stuck or misconfigured, or
	// the mode can't be applied at all.
	ConditionDegraded = "Degraded"
)

var (
	// ClusterModeResource is the ClusterMode resource.
	ClusterModeResource = schema.GroupVersionResource{Group: Group, Version: Version, Resource: "clustermodes"}
	// DefinitionResource is the ClusterModeDefinition resource.
	DefinitionResource = schema.GroupVersionResource{
		Group: Group, Version: Version, Resource: "clustermodedefinitions",
	}
)

// ClusterMode is a ClusterMode: a cluster's current mode, and the status
// mode-operator reports for it.
type ClusterMode struct {
	Name            string
	UID             types.UID
	ResourceVersion string
	Generation      int64
	Labels          map[string]string
	// Definition names the ClusterModeDefinition the ClusterMode follows
	// (spec.definition).
	Definition string
	// Mode is the current mode (spec.mode).
	Mode string
	// ModeOwner is the field manager that set Mode most recently, from the
	// object's managed fields: "mode-manager" normally, or the tool or
	// program that changed it otherwise.
	ModeOwner string
	// Status is written by mode-operator only. It is empty until
	// mode-operator has reconciled the ClusterMode.
	Status Status
}

// Status is a ClusterMode's status.
type Status struct {
	// ObservedGeneration is the generation the status was computed from.
	ObservedGeneration int64 `json:"observedGeneration"`
	// Modes, DefaultMode and Transitions are mode-operator's copy of the
	// definition's rules, which the API server checks changes of mode
	// against.
	Modes       []string     `json:"modes"`
	DefaultMode string       `json:"defaultMode"`
	Transitions []Transition `json:"transitions"`
	// Mode is the mode the status describes.
	Mode string `json:"mode"`
	// ModeChangedAt is when mode-operator first saw the current mode.
	ModeChangedAt *metav1.Time `json:"modeChangedAt"`
	// ModeSetBy is the field manager that set the mode, as mode-operator
	// reports it.
	ModeSetBy          string              `json:"modeSetBy"`
	Workloads          WorkloadCounts      `json:"workloads"`
	InvalidAnnotations int32               `json:"invalidAnnotations"`
	Replicas           ReplicaCounts       `json:"replicas"`
	Unsettled          []UnsettledWorkload `json:"unsettled"`
	// Conditions are Applied, Ready and Degraded.
	Conditions []metav1.Condition `json:"conditions"`
}

// WorkloadCounts counts the workloads that follow a ClusterMode, by state.
type WorkloadCounts struct {
	Total    int32 `json:"total"`
	Settled  int32 `json:"settled"`
	Scaling  int32 `json:"scaling"`
	Stalled  int32 `json:"stalled"`
	Applying int32 `json:"applying"`
	Pending  int32 `json:"pending"`
	Invalid  int32 `json:"invalid"`
}

// ReplicaCounts sums replica counts over the workloads that aren't Invalid.
type ReplicaCounts struct {
	Desired int32 `json:"desired"`
	Applied int32 `json:"applied"`
	Ready   int32 `json:"ready"`
}

// UnsettledWorkload is one workload that hasn't settled, with the reason.
type UnsettledWorkload struct {
	Kind      string       `json:"kind"`
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	State     string       `json:"state"`
	Desired   *int32       `json:"desired"`
	Applied   int32        `json:"applied"`
	Ready     int32        `json:"ready"`
	Since     *metav1.Time `json:"since"`
	Message   string       `json:"message"`
}

// Condition returns the condition of the given type, or nil.
func (c *ClusterMode) Condition(conditionType string) *metav1.Condition {
	return meta.FindStatusCondition(c.Status.Conditions, conditionType)
}

// Settled reports whether the ClusterMode has settled in its current mode:
// mode-operator computed the status from the latest spec, for the current
// mode, and its Ready condition is True. A mode can only be left once it has
// settled when the definition has transitions.
func (c *ClusterMode) Settled() bool {
	return c.Status.ObservedGeneration == c.Generation &&
		c.Status.Mode == c.Mode &&
		meta.IsStatusConditionTrue(c.Status.Conditions, ConditionReady)
}

// Rules returns the rules mode-operator copied into the status from the
// definition, which are what the API server checks changes of mode against.
// ok is false until mode-operator has written them.
func (c *ClusterMode) Rules() (rules Rules, ok bool) {
	if len(c.Status.Modes) == 0 {
		return Rules{}, false
	}
	return Rules{
		Modes:       slices.Clone(c.Status.Modes),
		DefaultMode: c.Status.DefaultMode,
		Transitions: cloneTransitions(c.Status.Transitions),
	}, true
}
