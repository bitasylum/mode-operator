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

package controller

// Prefix is the prefix of every label and annotation mode-operator reads or
// writes.
const Prefix = "mode.bitasylum.org/"

// Set by users on a workload's metadata.
const (
	// LabelClusterMode opts a workload in and names the ClusterMode it
	// follows. The cache only holds workloads that have it.
	LabelClusterMode = Prefix + "clustermode"

	// AnnotationReplicas holds the count for each mode, for example
	// "normal=3,peak=6,*=0".
	AnnotationReplicas = Prefix + "replicas"

	// AnnotationScaleToZeroDelay is an optional Go duration to wait before
	// scaling to zero. It never delays a scale-up, or a scale-down to a count
	// above zero.
	AnnotationScaleToZeroDelay = Prefix + "scale-to-zero-delay"

	// AnnotationSettleTimeout is an optional Go duration: how long the
	// workload may take to reach its count before it counts as Stalled.
	AnnotationSettleTimeout = Prefix + "settle-timeout"
)

// Written by mode-operator, with the same field manager as the count.
const (
	// AnnotationAppliedMode is the last mode fully applied.
	AnnotationAppliedMode = Prefix + "applied-mode"

	// AnnotationZeroPendingSince is the RFC 3339 start time of a pending
	// scale to zero. Being on the object, the countdown survives restarts
	// and leader changes.
	AnnotationZeroPendingSince = Prefix + "zero-pending-since"

	// AnnotationReplicasChangedAt is the RFC 3339 time mode-operator last
	// changed the count, used to detect stalled scaling.
	AnnotationReplicasChangedAt = Prefix + "replicas-changed-at"
)

// FieldManager is mode-operator's server-side apply field manager. ArgoCD's
// managedFieldsManagers setting must name it exactly.
const FieldManager = "mode-operator"

// Event reasons on workloads.
const (
	ReasonModeApplied          = "ModeApplied"
	ReasonScaleToZeroPending   = "ScaleToZeroPending"
	ReasonScaleToZeroCancelled = "ScaleToZeroCancelled"
	ReasonClusterModeNotFound  = "ClusterModeNotFound"
	ReasonMissingAnnotation    = "MissingAnnotation"
	ReasonInvalidAnnotation    = "InvalidAnnotation"
)
