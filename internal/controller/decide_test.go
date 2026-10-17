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
	"maps"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	nowTS, earlier := ts(0), ts(-time.Hour)

	cases := []struct {
		name       string
		mode       string
		cur        int32
		desired    int32
		delay      time.Duration
		delayValid bool
		existing   map[string]string
		want       decision
	}{
		{
			name: "scale up applies at once", mode: "peak", cur: 3, desired: 6, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationReplicasChangedAt: earlier},
			want:     decision{target: 6, appliedMode: "peak", changedAt: nowTS, event: ReasonModeApplied},
		},
		{
			name: "scale down to a count above zero ignores the delay", mode: "offpeak", cur: 6, desired: 3,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "peak"},
			want:     decision{target: 3, appliedMode: "offpeak", changedAt: nowTS, event: ReasonModeApplied},
		},
		{
			name: "scale to zero with no delay applies at once", mode: "standby", cur: 3, desired: 0, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal"},
			want:     decision{target: 0, appliedMode: "standby", changedAt: nowTS, event: ReasonModeApplied},
		},
		{
			name: "scale to zero with a delay starts the countdown", mode: "standby", cur: 3, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationReplicasChangedAt: earlier},
			want: decision{
				target: 3, appliedMode: "normal", pendingSince: nowTS, changedAt: earlier,
				requeue: 5 * time.Minute, event: ReasonScaleToZeroPending,
			},
		},
		{
			name: "still pending keeps the original start", mode: "standby", cur: 3, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: ts(-2 * time.Minute)},
			want: decision{
				target: 3, appliedMode: "normal", pendingSince: ts(-2 * time.Minute),
				requeue: 3 * time.Minute,
			},
		},
		{
			name: "expired delay scales to zero and clears the pending start", mode: "standby", cur: 3, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: ts(-5 * time.Minute)},
			want:     decision{target: 0, appliedMode: "standby", changedAt: nowTS, event: ReasonModeApplied},
		},
		{
			name: "moving to a mode above zero cancels a pending scale to zero", mode: "normal", cur: 3, desired: 3,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{
				AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: ts(-time.Minute),
				AnnotationReplicasChangedAt: earlier,
			},
			want: decision{target: 3, appliedMode: "normal", changedAt: earlier, event: ReasonScaleToZeroCancelled},
		},
		{
			name: "cancelling to a different count", mode: "peak", cur: 3, desired: 6,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: ts(-time.Minute)},
			want:     decision{target: 6, appliedMode: "peak", changedAt: nowTS, event: ReasonScaleToZeroCancelled},
		},
		{
			// standby -> maintenance, both 0: the countdown that began for
			// standby carries on. This also covers moving to another
			// ClusterMode whose mode wants 0.
			name: "moving between two zero modes doesn't restart the countdown", mode: "maintenance", cur: 3, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: ts(-4 * time.Minute)},
			want: decision{
				target: 3, appliedMode: "normal", pendingSince: ts(-4 * time.Minute),
				requeue: time.Minute,
			},
		},
		{
			name: "a pending start in the future restarts from now", mode: "standby", cur: 3, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: ts(time.Hour)},
			want: decision{
				target: 3, appliedMode: "normal", pendingSince: nowTS,
				requeue: 5 * time.Minute,
			},
		},
		{
			name: "a malformed pending start restarts from now", mode: "standby", cur: 3, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: "yesterday"},
			want: decision{
				target: 3, appliedMode: "normal", pendingSince: nowTS,
				requeue: 5 * time.Minute,
			},
		},
		{
			name: "already at zero needs no delay", mode: "standby", cur: 0, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "maintenance", AnnotationReplicasChangedAt: earlier},
			want:     decision{target: 0, appliedMode: "standby", changedAt: earlier},
		},
		{
			name: "a stale pending start at zero is cleared quietly", mode: "standby", cur: 0, desired: 0,
			delay: 5 * time.Minute, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "standby", AnnotationZeroPendingSince: earlier},
			want:     decision{target: 0, appliedMode: "standby"},
		},
		{
			name: "replicas-changed-at is kept when only the mode changes", mode: "peak", cur: 3, desired: 3, delayValid: true,
			existing: map[string]string{AnnotationAppliedMode: "normal", AnnotationReplicasChangedAt: earlier},
			want:     decision{target: 3, appliedMode: "peak", changedAt: earlier},
		},
		{
			name: "a first apply with no annotations", mode: "normal", cur: 0, desired: 3, delayValid: true,
			existing: nil,
			want:     decision{target: 3, appliedMode: "normal", changedAt: nowTS, event: ReasonModeApplied},
		},
		{
			name: "an invalid delay holds a scale to zero without a countdown", mode: "standby", cur: 3, desired: 0,
			delayValid: false,
			existing:   map[string]string{AnnotationAppliedMode: "normal", AnnotationReplicasChangedAt: earlier},
			want:       decision{target: 3, appliedMode: "normal", changedAt: earlier},
		},
		{
			name: "an invalid delay keeps a countdown that was already running", mode: "standby", cur: 3, desired: 0,
			delayValid: false,
			existing:   map[string]string{AnnotationAppliedMode: "normal", AnnotationZeroPendingSince: ts(-time.Minute)},
			want:       decision{target: 3, appliedMode: "normal", pendingSince: ts(-time.Minute)},
		},
		{
			name: "an invalid delay doesn't block a scale-up", mode: "peak", cur: 3, desired: 6, delayValid: false,
			existing: map[string]string{AnnotationAppliedMode: "normal"},
			want:     decision{target: 6, appliedMode: "peak", changedAt: nowTS, event: ReasonModeApplied},
		},
		{
			name: "an invalid delay doesn't block a scale-down above zero", mode: "offpeak", cur: 6, desired: 1, delayValid: false,
			existing: map[string]string{AnnotationAppliedMode: "peak"},
			want:     decision{target: 1, appliedMode: "offpeak", changedAt: nowTS, event: ReasonModeApplied},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decide(c.mode, c.cur, c.desired, c.delay, c.delayValid, c.existing, now)
			if got != c.want {
				t.Errorf("decide()\n got: %+v\nwant: %+v", got, c.want)
			}
		})
	}
}

func TestDecideTruncatesTheStartToWholeSeconds(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 600_000_000, time.UTC)
	got := decide("standby", 3, 0, 10*time.Second, true, nil, now)
	if got.pendingSince != "2026-10-17T12:00:00Z" {
		t.Errorf("pendingSince = %q", got.pendingSince)
	}
	// The stored start is what later reconciles count from, so the first
	// requeue counts from it too.
	if want := 10*time.Second - 600*time.Millisecond; got.requeue != want {
		t.Errorf("requeue = %v, want %v", got.requeue, want)
	}
}

func TestDecisionAnnotationsLeaveOutEmptyValues(t *testing.T) {
	d := decision{appliedMode: "peak", changedAt: "2026-10-17T12:00:00Z"}
	want := map[string]string{
		AnnotationAppliedMode:       "peak",
		AnnotationReplicasChangedAt: "2026-10-17T12:00:00Z",
	}
	if got := d.annotations(); !maps.Equal(got, want) {
		t.Errorf("annotations() = %v, want %v", got, want)
	}
	if got := (decision{}).annotations(); len(got) != 0 {
		t.Errorf("annotations() of an empty decision = %v, want none", got)
	}
}
