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

// Package clustermode reads and writes ClusterModes, the cluster-scoped
// objects that hold a cluster's current mode, and reads the
// ClusterModeDefinitions they follow.
//
// A [Reader] only reads. It gets a ClusterMode, watches it, and waits for a
// mode to settle, which means every workload has reached the mode's replica
// counts and no pod is still stopping. It needs read access to the
// ClusterMode only, for example through the clustermode-viewer role.
//
// A [Writer] changes the mode. [NewModeManagerWriter] makes the writer the
// mode manager uses: it creates the ClusterMode if it's missing, and owns
// spec.mode, spec.definition and the writer label. [NewWriter] makes a writer
// for anyone else, such as an orchestration server, which changes spec.mode
// only. [Writer.Set] checks a change before writing it, [Writer.Apply]
// leaves that to the API server, and [Writer.MoveTo] walks a path of several
// changes when the definition's transitions require one. [Writer.OnChange]
// reports every change of the current mode, the Writer's own and those it
// adopts from others, in order.
//
// Changes of mode are checked against [Rules]: the definition's modes,
// default mode and transitions. mode-operator copies these into each
// ClusterMode's status, and the API server checks every change against that
// copy, so the copy is what a Writer checks against and what [Rules.Next]
// and [Rules.Plan] advise from. Only until mode-operator has written the
// copy does the package read the ClusterModeDefinition instead.
//
// Errors that a caller can act on are an [*Error] with a [Reason]; use
// [ReasonOf] to get it.
package clustermode
