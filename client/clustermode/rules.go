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

import "slices"

// Transition lists the modes a ClusterMode may move to from one mode.
type Transition struct {
	From string   `json:"from"`
	To   []string `json:"to"`
}

// Rules are the modes a ClusterMode may be in and the changes of mode allowed
// between them.
type Rules struct {
	// Modes are the allowed modes, in the definition's order.
	Modes []string
	// DefaultMode can be reached from any mode, at any time.
	DefaultMode string
	// Transitions, when not empty, list for each mode the modes it may move
	// to. A mode with no rule can only move to DefaultMode. When empty, any
	// mode may follow any other.
	Transitions []Transition
}

// Definition is a ClusterModeDefinition.
type Definition struct {
	Name string
	Rules
}

// Has reports whether mode is one of the allowed modes.
func (r Rules) Has(mode string) bool {
	return slices.Contains(r.Modes, mode)
}

// Restricted reports whether the rules have transitions. Leaving a mode for
// any mode but DefaultMode then also needs the mode to have settled.
func (r Rules) Restricted() bool {
	return len(r.Transitions) > 0
}

// Allowed reports whether the rules allow moving from one mode to another.
// It doesn't check whether the mode being left has settled.
func (r Rules) Allowed(from, to string) bool {
	if !r.Has(to) {
		return false
	}
	if from == to || !r.Restricted() || to == r.DefaultMode {
		return true
	}
	for _, t := range r.Transitions {
		if t.From == from {
			return slices.Contains(t.To, to)
		}
	}
	return false
}

// Next returns the modes that may follow from, in the definition's order.
func (r Rules) Next(from string) []string {
	var next []string
	for _, m := range r.Modes {
		if m != from && r.Allowed(from, m) {
			next = append(next, m)
		}
	}
	return next
}

// Plan returns the shortest series of allowed changes from one mode to
// another, ending with to, or nil if from is to. Of paths that are equally
// short, it takes the one whose modes come first in the definition's order.
// It returns an [*Error] with [ReasonUnknownMode] or [ReasonNoPath].
func (r Rules) Plan(from, to string) ([]string, error) {
	if !r.Has(to) {
		return nil, &Error{Reason: ReasonUnknownMode, Mode: to}
	}
	if from == to {
		return nil, nil
	}
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, m := range r.Modes {
			if _, seen := prev[m]; seen || !r.Allowed(cur, m) {
				continue
			}
			prev[m] = cur
			if m == to {
				var path []string
				for at := to; at != from; at = prev[at] {
					path = append(path, at)
				}
				slices.Reverse(path)
				return path, nil
			}
			queue = append(queue, m)
		}
	}
	return nil, &Error{Reason: ReasonNoPath, From: from, Mode: to}
}

func cloneTransitions(ts []Transition) []Transition {
	if ts == nil {
		return nil
	}
	out := make([]Transition, len(ts))
	for i, t := range ts {
		out[i] = Transition{From: t.From, To: slices.Clone(t.To)}
	}
	return out
}
