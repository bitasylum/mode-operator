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

import (
	"time"
)

// decision is what decide works out for one workload.
type decision struct {
	// target is the count to set.
	target int32
	// appliedMode, pendingSince and changedAt are the values of the
	// controller's annotations. Empty means the annotation is left off, which
	// makes server-side apply remove it.
	appliedMode  string
	pendingSince string
	changedAt    string
	// requeue is when to look again, for a pending scale to zero. 0 means
	// don't.
	requeue time.Duration
	// event is the reason of the Normal event to emit if the request is
	// applied, or "" for none.
	event string
}

// annotations returns the controller annotations to apply.
func (d decision) annotations() map[string]string {
	ann := map[string]string{}
	for k, v := range map[string]string{
		AnnotationAppliedMode:       d.appliedMode,
		AnnotationZeroPendingSince:  d.pendingSince,
		AnnotationReplicasChangedAt: d.changedAt,
	} {
		if v != "" {
			ann[k] = v
		}
	}
	return ann
}

// decide works out the count and controller annotations for a workload whose
// current count is cur, in a mode that wants desired. delay is the workload's
// scale-to-zero delay, and delayValid is false when that annotation is
// invalid. existing is the workload's annotations.
//
// The delay only ever holds a scale to zero. While it runs, the workload keeps
// its count and the mode it last applied.
func decide(mode string, cur, desired int32, delay time.Duration, delayValid bool,
	existing map[string]string, now time.Time,
) decision {
	d := decision{target: desired, appliedMode: mode}

	if desired == 0 && cur > 0 {
		switch {
		case !delayValid:
			// Hold without a countdown. Fixing the annotation retriggers, and
			// a countdown that was already running resumes from its start.
			d.target = cur
			d.appliedMode = existing[AnnotationAppliedMode]
			d.pendingSince = existing[AnnotationZeroPendingSince]
		case delay > 0:
			since, err := time.Parse(time.RFC3339, existing[AnnotationZeroPendingSince])
			if err != nil || since.After(now) {
				since = now.UTC().Truncate(time.Second)
			}
			if remaining := delay - now.Sub(since); remaining > 0 {
				d.target = cur
				d.appliedMode = existing[AnnotationAppliedMode]
				d.pendingSince = since.UTC().Format(time.RFC3339)
				d.requeue = remaining
			}
		}
	}

	if d.target != cur {
		d.changedAt = now.UTC().Format(time.RFC3339)
	} else {
		d.changedAt = existing[AnnotationReplicasChangedAt]
	}

	wasPending := existing[AnnotationZeroPendingSince] != ""
	switch {
	case d.pendingSince != "" && !wasPending:
		d.event = ReasonScaleToZeroPending
	case d.pendingSince == "" && wasPending && d.target != 0:
		d.event = ReasonScaleToZeroCancelled
	case d.target != cur:
		d.event = ReasonModeApplied
	}
	return d
}
