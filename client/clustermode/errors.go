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
	"errors"
	"fmt"
)

// Reason says why an operation failed, in a way a caller can act on.
type Reason string

// Reasons an operation can fail.
const (
	// ReasonNotInstalled: the ClusterMode CRDs aren't installed.
	ReasonNotInstalled Reason = "NotInstalled"
	// ReasonNotFound: the ClusterMode doesn't exist.
	ReasonNotFound Reason = "NotFound"
	// ReasonDefinitionNotFound: the ClusterModeDefinition doesn't exist.
	ReasonDefinitionNotFound Reason = "DefinitionNotFound"
	// ReasonRulesUnavailable: mode-operator hasn't copied the definition's
	// rules into the ClusterMode's status yet, and the definition can't be
	// read.
	ReasonRulesUnavailable Reason = "RulesUnavailable"
	// ReasonUnknownMode: the mode isn't one of the definition's modes.
	ReasonUnknownMode Reason = "UnknownMode"
	// ReasonTransitionNotAllowed: the definition's transitions don't allow
	// the change.
	ReasonTransitionNotAllowed Reason = "TransitionNotAllowed"
	// ReasonNotSettled: the mode being left hasn't settled yet, and the
	// definition's transitions require it to.
	ReasonNotSettled Reason = "NotSettled"
	// ReasonNoPath: no series of allowed changes leads to the mode.
	ReasonNoPath Reason = "NoPath"
	// ReasonInterrupted: someone else changed the mode while waiting.
	ReasonInterrupted Reason = "Interrupted"
	// ReasonForbidden: RBAC doesn't allow the request.
	ReasonForbidden Reason = "Forbidden"
	// ReasonRejected: the API server rejected the change, and re-reading
	// the ClusterMode doesn't show why.
	ReasonRejected Reason = "Rejected"
	// ReasonDefinitionMismatch: the ClusterMode follows another definition
	// than the mode manager's Writer was given, and spec.definition can't be
	// changed once set.
	ReasonDefinitionMismatch Reason = "DefinitionMismatch"
)

// Error is an error with a [Reason].
type Error struct {
	Reason Reason
	// Mode is the mode the operation was about. For ReasonInterrupted, it is
	// the mode someone else changed to.
	Mode string
	// From is the mode being left, where there is one.
	From string
	// By is the field manager that changed the mode, for ReasonInterrupted.
	By string
	// Err is the underlying error, such as the API server's response.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	var msg string
	switch e.Reason {
	case ReasonNotInstalled:
		msg = "the ClusterMode CRDs aren't installed"
	case ReasonNotFound:
		msg = "the ClusterMode doesn't exist"
	case ReasonDefinitionNotFound:
		msg = "the ClusterModeDefinition doesn't exist"
	case ReasonRulesUnavailable:
		msg = "the ClusterMode's status has no copy of its definition's rules yet, and the definition can't be read"
	case ReasonUnknownMode:
		msg = fmt.Sprintf("%q isn't one of the definition's modes", e.Mode)
	case ReasonTransitionNotAllowed:
		msg = fmt.Sprintf("the definition's transitions don't allow changing from %s to %s", e.From, e.Mode)
	case ReasonNotSettled:
		msg = fmt.Sprintf("%s can't be left for %s until it has settled", e.From, e.Mode)
	case ReasonNoPath:
		msg = fmt.Sprintf("no allowed series of changes leads from %s to %s", e.From, e.Mode)
	case ReasonInterrupted:
		msg = fmt.Sprintf("the mode was changed to %s while waiting for %s", e.Mode, e.From)
		if e.By != "" {
			msg += ", by " + e.By
		}
	case ReasonForbidden:
		msg = "RBAC doesn't allow the request"
	case ReasonRejected:
		msg = "the API server rejected the change"
	case ReasonDefinitionMismatch:
		msg = "spec.definition can't be changed once set"
	default:
		msg = string(e.Reason)
	}
	if e.Err != nil {
		return msg + ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// ReasonOf returns the [Reason] of err, or "" if err isn't an [*Error].
func ReasonOf(err error) Reason {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Reason
	}
	return ""
}
