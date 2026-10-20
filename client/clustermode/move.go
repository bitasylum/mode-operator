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
	"context"
	"time"
)

// Phase is how far a step of [Writer.MoveTo] has got.
type Phase string

// Phases of a step.
const (
	// PhaseWaiting: waiting for the mode being left to settle.
	PhaseWaiting Phase = "Waiting"
	// PhaseWritten: the step's mode has been written.
	PhaseWritten Phase = "Written"
)

// Step is one change of mode in [Writer.MoveTo].
type Step struct {
	// N is the step's number, from 1, of Of steps.
	N, Of    int
	From, To string
	Phase    Phase
	// ClusterMode is the ClusterMode as it was when the step reached Phase.
	ClusterMode *ClusterMode
}

// MoveOptions configure [Writer.MoveTo].
type MoveOptions struct {
	// OnStep is called as each step reaches each phase, for reporting
	// progress. An error stops the move. The mode manager broadcasts each
	// mode from [Writer.OnChange] instead, which is called before a step is
	// reported Written, and in order with changes made by others.
	OnStep func(Step) error
	// StepTimeout limits how long each wait for a mode to settle takes. Zero
	// means no limit beyond ctx.
	StepTimeout time.Duration
	// WaitFinal makes MoveTo also wait for the target mode to settle.
	WaitFinal bool
}

// MoveTo changes the mode to target by the shortest series of changes the
// rules allow, one change at a time. Before leaving a mode for any mode but
// the default, it waits for the mode to settle when the rules have
// transitions. It returns the last ClusterMode it wrote or waited for, and an
// [*Error] with [ReasonInterrupted] if someone else changes the mode while it
// waits. It doesn't plan again after an interruption.
//
// For the mode manager's Writer, a missing ClusterMode is created in target.
func (w *Writer) MoveTo(ctx context.Context, target string, o MoveOptions) (*ClusterMode, error) {
	cm, err := w.reader.Get(ctx)
	if ReasonOf(err) == ReasonNotFound && w.isModeManager() {
		// Creating it in target is the whole move.
		written, err := w.Set(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := notify(o, Step{N: 1, Of: 1, To: target}, PhaseWritten, written); err != nil {
			return written, err
		}
		if !o.WaitFinal {
			return written, nil
		}
		return w.waitSettled(ctx, target, o.StepTimeout)
	}
	if err != nil {
		return nil, err
	}
	rules, err := w.reader.Rules(ctx, cm)
	if err != nil {
		return cm, err
	}
	plan, err := rules.Plan(cm.Mode, target)
	if err != nil {
		return cm, err
	}
	for i, to := range plan {
		step := Step{N: i + 1, Of: len(plan), From: cm.Mode, To: to}
		if rules.Restricted() && to != rules.DefaultMode {
			if err := notify(o, step, PhaseWaiting, cm); err != nil {
				return cm, err
			}
			settled, err := w.waitSettled(ctx, cm.Mode, o.StepTimeout)
			if err != nil {
				return cm, err
			}
			cm = settled
		}
		written, err := w.Set(ctx, to)
		if err != nil {
			return cm, err
		}
		cm = written
		if err := notify(o, step, PhaseWritten, cm); err != nil {
			return cm, err
		}
	}
	if o.WaitFinal {
		settled, err := w.waitSettled(ctx, target, o.StepTimeout)
		if err != nil {
			return cm, err
		}
		cm = settled
	}
	return cm, nil
}

func (w *Writer) waitSettled(ctx context.Context, mode string, timeout time.Duration) (*ClusterMode, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return w.reader.WaitSettled(ctx, mode)
}

func notify(o MoveOptions, step Step, phase Phase, cm *ClusterMode) error {
	if o.OnStep == nil {
		return nil
	}
	step.Phase, step.ClusterMode = phase, cm
	return o.OnStep(step)
}
