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

// Package startup decides which mode the mode manager starts in, and writes
// that decision. It keeps the stored mode across restarts of the mode
// manager, and resets to the definition's default mode after a full cluster
// reboot, which it detects with the [fingerprint] of node boot IDs: if any
// node still has the boot ID stored when the mode was last written, the
// cluster didn't fully reboot.
//
// [Decide] is the decision alone, as a pure function. [Run] gathers its
// inputs, makes it, and carries it out in an order that is safe to repeat
// after a failure.
package startup

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/bitasylum/mode-operator/client/clustermode"
	"github.com/bitasylum/mode-operator/client/fingerprint"
)

// Case is which situation the decision found.
type Case string

// Cases, in the order they are checked.
const (
	// CaseNotInstalled: the CRDs aren't installed. The mode manager runs in
	// its built-in default mode and writes nothing until they are.
	CaseNotInstalled Case = "NotInstalled"
	// CaseDefinitionNotFound: the ClusterModeDefinition doesn't exist.
	CaseDefinitionNotFound Case = "DefinitionNotFound"
	// CaseClusterModeNotFound: the ClusterMode doesn't exist yet.
	CaseClusterModeNotFound Case = "ClusterModeNotFound"
	// CaseInvalidStoredMode: the stored mode isn't one of the definition's
	// modes, or isn't one the mode manager knows.
	CaseInvalidStoredMode Case = "InvalidStoredMode"
	// CaseNoFingerprint: no fingerprint is stored for the ClusterMode.
	CaseNoFingerprint Case = "NoFingerprint"
	// CaseOverlap: at least one node stayed up, so the stored mode is kept.
	CaseOverlap Case = "Overlap"
	// CaseFullReboot: every node has rebooted since the fingerprint was
	// stored.
	CaseFullReboot Case = "FullReboot"
)

// Reasons of the events recorded on the ClusterMode.
const (
	ReasonModeRestored       = "ModeRestored"
	ReasonModeReset          = "ModeReset"
	ReasonDefinitionNotFound = "DefinitionNotFound"
	ReasonInvalidStoredMode  = "InvalidStoredMode"
)

// Inputs are what the decision is made from. A nil pointer means the object
// doesn't exist.
type Inputs struct {
	// Installed is whether the API server serves ClusterModes.
	Installed   bool
	Definition  *clustermode.Definition
	ClusterMode *clustermode.ClusterMode
	Fingerprint *fingerprint.Record
	// Live are the nodes' boot IDs now.
	Live fingerprint.BootIDs
}

// Options configure the decision.
type Options struct {
	// ClusterMode is the ClusterMode's name. A fingerprint stored for
	// another ClusterMode counts as none.
	ClusterMode string
	// BuiltinDefault is the mode to run in without the CRDs or the
	// definition, and when the definition's default mode isn't known.
	BuiltinDefault string
	// KnownModes, if set, are the modes the mode manager can run in. A stored
	// mode outside them is reset. Nil means every mode of the definition.
	KnownModes []string
}

// Event is an event to record on the ClusterMode.
type Event struct {
	// Type is corev1.EventTypeNormal or corev1.EventTypeWarning.
	Type    string
	Reason  string
	Message string
}

// Decision is the mode to start in, and why.
type Decision struct {
	Case Case
	Mode string
	// Restored is true when the stored mode is kept.
	Restored bool
	// Persist is whether to write the ClusterMode and the fingerprint.
	// Decide sets it false only when the CRDs aren't installed.
	Persist bool
	// Event, if set, is recorded on the ClusterMode once it is written.
	Event *Event
	// Overlap lists the nodes that stayed up, for CaseOverlap.
	Overlap []string
}

// Decide makes the startup decision. The first case that applies wins.
func Decide(in Inputs, o Options) Decision {
	if !in.Installed {
		return Decision{Case: CaseNotInstalled, Mode: o.BuiltinDefault}
	}
	if in.Definition == nil {
		// mode-operator keeps the last copy of a deleted definition's rules,
		// and the API server checks writes against it, so the copy's default
		// mode is the one that can always be written.
		mode := o.BuiltinDefault
		if in.ClusterMode != nil && in.ClusterMode.Status.DefaultMode != "" {
			mode = in.ClusterMode.Status.DefaultMode
		}
		return Decision{
			Case: CaseDefinitionNotFound, Mode: mode, Persist: true,
			Event: warning(ReasonDefinitionNotFound,
				"The ClusterModeDefinition doesn't exist; started in the default mode %s", mode),
		}
	}
	def := in.Definition.DefaultMode
	if !o.knows(def) {
		def = o.BuiltinDefault
	}
	if in.ClusterMode == nil {
		return Decision{
			Case: CaseClusterModeNotFound, Mode: def, Persist: true,
			Event: normal(ReasonModeReset, "Created in the default mode %s", def),
		}
	}
	stored := in.ClusterMode.Mode
	if !in.Definition.Has(stored) || !o.knows(stored) {
		return Decision{
			Case: CaseInvalidStoredMode, Mode: def, Persist: true,
			Event: warning(ReasonInvalidStoredMode,
				"The stored mode %s isn't one this mode manager can run in; reset to the default mode %s", stored, def),
		}
	}
	rec := in.Fingerprint
	if rec == nil || len(rec.BootIDs) == 0 || (o.ClusterMode != "" && rec.ClusterMode != o.ClusterMode) {
		return Decision{
			Case: CaseNoFingerprint, Mode: def, Persist: true,
			Event: normal(ReasonModeReset,
				"No node fingerprint was stored; reset from %s to the default mode %s", stored, def),
		}
	}
	if up := fingerprint.Overlap(rec.BootIDs, in.Live); len(up) > 0 {
		return Decision{
			Case: CaseOverlap, Mode: stored, Restored: true, Persist: true, Overlap: up,
			Event: normal(ReasonModeRestored,
				"Kept the mode %s: %d of %d nodes stayed up", stored, len(up), len(rec.BootIDs)),
		}
	}
	return Decision{
		Case: CaseFullReboot, Mode: def, Persist: true,
		Event: normal(ReasonModeReset,
			"Every node has rebooted since the mode was stored; reset from %s to the default mode %s", stored, def),
	}
}

func (o Options) knows(mode string) bool {
	return o.KnownModes == nil || slices.Contains(o.KnownModes, mode)
}

func normal(reason, format string, args ...any) *Event {
	return &Event{Type: corev1.EventTypeNormal, Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func warning(reason, format string, args ...any) *Event {
	return &Event{Type: corev1.EventTypeWarning, Reason: reason, Message: fmt.Sprintf(format, args...)}
}
