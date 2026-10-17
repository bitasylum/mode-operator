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

package controller

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	modev1alpha1 "github.com/bitasylum/mode-operator/api/v1alpha1"
	"github.com/bitasylum/mode-operator/internal/workload"
)

var (
	sumNow     = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	sumModes   = []modev1alpha1.ModeName{"normal", "peak", "offpeak", "maintenance", "standby"}
	sumDef     = &modev1alpha1.ClusterModeDefinition{ObjectMeta: metav1.ObjectMeta{Name: "standard"}, Spec: modev1alpha1.ClusterModeDefinitionSpec{Modes: sumModes, DefaultMode: "standby"}}
	sumTimeout = 10 * time.Minute
)

// at returns sumNow moved by d, as an RFC 3339 annotation value.
func at(d time.Duration) string { return sumNow.Add(d).Format(time.RFC3339) }

// cmIn returns ClusterMode "default" in mode, as mode-operator last saw it
// an hour ago.
func cmIn(mode string) *modev1alpha1.ClusterMode {
	return &modev1alpha1.ClusterMode{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Generation: 4},
		Spec:       modev1alpha1.ClusterModeSpec{Definition: "standard", Mode: modev1alpha1.ModeName(mode)},
		Status: modev1alpha1.ClusterModeStatus{
			Modes:         sumModes,
			Mode:          modev1alpha1.ModeName(mode),
			ModeChangedAt: &metav1.Time{Time: sumNow.Add(-time.Hour)},
		},
	}
}

// deploy returns a Deployment in namespace shop with spec.replicas set to
// spec, and status reporting statusReplicas, of which ready are ready.
func deploy(name string, spec, statusReplicas, ready int32, ann map[string]string) observedWorkload {
	return observedWorkload{kind: workload.Deployment{}, obj: &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Generation: 2, Annotations: ann},
		Spec:       appsv1.DeploymentSpec{Replicas: new(spec)},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: statusReplicas, ReadyReplicas: ready},
	}}
}

// settled returns a Deployment that has reached count n in mode.
func settled(name, mode string, n int32, replicas string) observedWorkload {
	return deploy(name, n, n, n, map[string]string{AnnotationReplicas: replicas, AnnotationAppliedMode: mode})
}

// with returns ann plus the given key and value pairs.
func with(ann map[string]string, kv ...string) map[string]string {
	out := maps.Clone(ann)
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestSummarizeStates(t *testing.T) {
	normal3 := map[string]string{AnnotationReplicas: "normal=3,peak=6", AnnotationAppliedMode: "normal"}
	cases := []struct {
		name    string
		mode    string
		w       observedWorkload
		history map[string]workloadHistory
		state   modev1alpha1.WorkloadState
		desired *int32
		since   string // RFC 3339, or "" for none
		message string // substring
	}{
		{
			name: "settled", mode: "normal", w: deploy("web", 3, 3, 3, normal3),
			state: modev1alpha1.WorkloadSettled,
		},
		{
			name: "missing replicas annotation", mode: "normal", w: deploy("web", 3, 3, 3, nil),
			state: modev1alpha1.WorkloadInvalid, message: "but no mode.bitasylum.org/replicas annotation",
		},
		{
			name: "invalid replicas annotation", mode: "normal",
			w:     deploy("web", 3, 3, 3, map[string]string{AnnotationReplicas: "normal=x"}),
			state: modev1alpha1.WorkloadInvalid, message: `entry "normal=x"`,
		},
		{
			name: "invalid delay blocking a scale to zero", mode: "standby",
			w:     deploy("web", 3, 3, 3, with(normal3, AnnotationScaleToZeroDelay, "soon")),
			state: modev1alpha1.WorkloadInvalid, desired: ptr.To[int32](0), message: "holding 3 replicas until it is fixed",
		},
		{
			name: "invalid delay that blocks nothing", mode: "normal",
			w:     deploy("web", 3, 3, 3, with(normal3, AnnotationScaleToZeroDelay, "soon")),
			state: modev1alpha1.WorkloadSettled,
		},
		{
			name: "pending scale to zero", mode: "standby",
			w: deploy("web", 3, 3, 3, with(normal3, AnnotationScaleToZeroDelay, "5m",
				AnnotationZeroPendingSince, at(-time.Minute))),
			state: modev1alpha1.WorkloadPending, desired: ptr.To[int32](0), since: at(-time.Minute),
			message: "Scaling to 0 at " + at(4*time.Minute),
		},
		{
			name: "applying a new mode", mode: "peak", w: deploy("web", 3, 3, 3, normal3),
			state: modev1alpha1.WorkloadApplying, desired: ptr.To[int32](6), since: at(-time.Hour),
			message: `Replicas are 3; mode "peak" wants 6`,
		},
		{
			name: "applying after drift has no known start", mode: "normal", w: deploy("web", 1, 1, 1, normal3),
			state: modev1alpha1.WorkloadApplying, desired: ptr.To[int32](3),
		},
		{
			name: "scaling up", mode: "normal",
			w:     deploy("web", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(-time.Minute))),
			state: modev1alpha1.WorkloadScaling, desired: ptr.To[int32](3), since: at(-time.Minute),
			message: "1 of 3 replicas ready",
		},
		{
			name: "scaling down", mode: "offpeak",
			w: deploy("web", 0, 3, 3, map[string]string{
				AnnotationReplicas: "normal=3", AnnotationAppliedMode: "offpeak",
				AnnotationReplicasChangedAt: at(-time.Minute),
			}),
			state: modev1alpha1.WorkloadScaling, desired: ptr.To[int32](0),
			message: "3 replicas ready, waiting for the Deployment to settle at 0",
		},
		{
			name: "stalled once the settle timeout has passed", mode: "normal",
			w:     deploy("web", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(-sumTimeout))),
			state: modev1alpha1.WorkloadStalled, since: at(-sumTimeout),
			message: "Not settled within the 10m0s settle timeout: 1 of 3 replicas ready",
		},
		{
			name: "still scaling a second before the settle timeout", mode: "normal",
			w:     deploy("web", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(time.Second-sumTimeout))),
			state: modev1alpha1.WorkloadScaling,
		},
		{
			name: "with no replicas-changed-at, the timeout runs from modeChangedAt", mode: "normal",
			w:     deploy("web", 3, 3, 1, normal3),
			state: modev1alpha1.WorkloadStalled, since: at(-time.Hour),
		},
		{
			name: "a workload's own settle timeout", mode: "normal",
			w: deploy("web", 3, 3, 1, with(normal3, AnnotationSettleTimeout, "30m",
				AnnotationReplicasChangedAt, at(-20*time.Minute))),
			state: modev1alpha1.WorkloadScaling,
		},
		{
			name: "an invalid settle timeout falls back to the default", mode: "normal",
			w: deploy("web", 3, 3, 1, with(normal3, AnnotationSettleTimeout, "0",
				AnnotationReplicasChangedAt, at(-20*time.Minute))),
			state: modev1alpha1.WorkloadStalled,
		},
		{
			name: "a rollout long after the workload settled gets the whole timeout", mode: "normal",
			w:       deploy("web", 3, 4, 3, with(normal3, AnnotationReplicasChangedAt, at(-24*time.Hour))),
			history: map[string]workloadHistory{"Deployment/shop/web": {settledSince: sumNow.Add(-time.Hour)}},
			state:   modev1alpha1.WorkloadScaling, since: at(0),
		},
		{
			name: "the timeout runs from when the settled workload was first seen short", mode: "normal",
			w:       deploy("web", 3, 3, 2, with(normal3, AnnotationReplicasChangedAt, at(-24*time.Hour))),
			history: map[string]workloadHistory{"Deployment/shop/web": {shortSince: sumNow.Add(-11 * time.Minute)}},
			state:   modev1alpha1.WorkloadStalled, since: at(-11 * time.Minute),
		},
		{
			name: "a brief spell at its count carries on the timeout already running", mode: "normal",
			w: deploy("web", 3, 3, 2, with(normal3, AnnotationReplicasChangedAt, at(-24*time.Hour))),
			history: map[string]workloadHistory{"Deployment/shop/web": {
				settledSince: sumNow.Add(-10 * time.Second), shortSince: sumNow.Add(-11 * time.Minute),
			}},
			state: modev1alpha1.WorkloadStalled, since: at(-11 * time.Minute),
		},
		{
			name: "a brief spell at its count, with no earlier spell known, uses replicas-changed-at", mode: "normal",
			w:       deploy("web", 3, 3, 2, with(normal3, AnnotationReplicasChangedAt, at(-24*time.Hour))),
			history: map[string]workloadHistory{"Deployment/shop/web": {settledSince: sumNow.Add(-10 * time.Second)}},
			state:   modev1alpha1.WorkloadStalled, since: at(-24 * time.Hour),
		},
		{
			name: "with no history, as after a restart, a stuck workload stays Stalled", mode: "normal",
			w:     deploy("web", 3, 3, 2, with(normal3, AnnotationReplicasChangedAt, at(-24*time.Hour))),
			state: modev1alpha1.WorkloadStalled, since: at(-24 * time.Hour),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := summarize(cmIn(c.mode), sumDef, []observedWorkload{c.w}, c.history, sumNow, sumTimeout)
			if c.state == modev1alpha1.WorkloadSettled {
				if s.status.Workloads.Settled != 1 || len(s.status.Unsettled) != 0 {
					t.Fatalf("want Settled, got counts %+v, unsettled %+v", s.status.Workloads, s.status.Unsettled)
				}
				return
			}
			if len(s.status.Unsettled) != 1 {
				t.Fatalf("want one unsettled workload, got %+v", s.status.Unsettled)
			}
			u := s.status.Unsettled[0]
			if u.State != c.state {
				t.Errorf("state = %s, want %s (message %q)", u.State, c.state, u.Message)
			}
			if c.desired != nil && (u.Desired == nil || *u.Desired != *c.desired) {
				t.Errorf("desired = %v, want %d", u.Desired, *c.desired)
			}
			if c.state == modev1alpha1.WorkloadInvalid && c.desired == nil && u.Desired != nil {
				t.Errorf("desired = %d, want none for an invalid replicas annotation", *u.Desired)
			}
			if got := sinceString(u.Since); c.since != "" && got != c.since {
				t.Errorf("since = %s, want %s", got, c.since)
			}
			if c.state == modev1alpha1.WorkloadInvalid && u.Since != nil {
				t.Errorf("since = %s, want none for Invalid", sinceString(u.Since))
			}
			if !strings.Contains(u.Message, c.message) {
				t.Errorf("message = %q, want it to contain %q", u.Message, c.message)
			}
		})
	}
}

func sinceString(t *metav1.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func TestSummarizeCountsAndSums(t *testing.T) {
	normal3 := map[string]string{AnnotationReplicas: "normal=3", AnnotationAppliedMode: "normal"}
	s := summarize(cmIn("normal"), sumDef, []observedWorkload{
		settled("a", "normal", 3, "normal=3"),
		deploy("b", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(-time.Minute))), // Scaling
		deploy("c", 3, 3, 1, normal3),                                                      // Stalled: since modeChangedAt, an hour ago
		deploy("d", 1, 1, 1, normal3),                                                      // Applying: drift
		deploy("e", 5, 5, 5, map[string]string{AnnotationReplicas: "normal=x"}),            // Invalid
		deploy("f", 2, 2, 2, map[string]string{AnnotationAppliedMode: "normal"}),           // Invalid: missing
		deploy("g", 3, 3, 3, with(normal3, AnnotationScaleToZeroDelay, "soon")),            // Settled, but an error
		deploy("h", 3, 3, 3, with(normal3, AnnotationSettleTimeout, "forever")),            // Settled, but an error
	}, nil, sumNow, sumTimeout)

	want := modev1alpha1.WorkloadCounts{Total: 8, Settled: 3, Scaling: 1, Stalled: 1, Applying: 1, Invalid: 2}
	if s.status.Workloads != want {
		t.Errorf("counts = %+v, want %+v", s.status.Workloads, want)
	}
	if s.status.InvalidAnnotations != 4 {
		t.Errorf("invalidAnnotations = %d, want 4 (e, f, g and h)", s.status.InvalidAnnotations)
	}
	// The Invalid workloads e and f are left out of the sums.
	wantReplicas := modev1alpha1.ReplicaCounts{Desired: 18, Applied: 16, Ready: 12}
	if s.status.Replicas != wantReplicas {
		t.Errorf("replicas = %+v, want %+v", s.status.Replicas, wantReplicas)
	}
}

func TestSummarizeUnsettledOrderAndCap(t *testing.T) {
	ann := func(kv ...string) map[string]string {
		return with(map[string]string{AnnotationReplicas: "normal=3", AnnotationAppliedMode: "normal"}, kv...)
	}
	ws := make([]observedWorkload, 0, 27)
	// 22 Scaling workloads, so the list is cut off.
	for i := range 22 {
		ws = append(ws, deploy(fmt.Sprintf("scaling-%02d", i), 3, 3, 1, ann(AnnotationReplicasChangedAt, at(-time.Minute))))
	}
	ws = append(ws,
		deploy("invalid", 3, 3, 3, map[string]string{AnnotationReplicas: "bogus"}),
		deploy("stalled-new", 3, 3, 1, ann(AnnotationReplicasChangedAt, at(-11*time.Minute))),
		deploy("stalled-old", 3, 3, 1, ann(AnnotationReplicasChangedAt, at(-30*time.Minute))),
		deploy("applying", 1, 1, 1, ann()),
	)
	// A StatefulSet sorts after a Deployment with the same since.
	ws = append(ws, observedWorkload{kind: workload.StatefulSet{}, obj: &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "a-stalled-sts", Namespace: "shop", Generation: 1,
			Annotations: ann(AnnotationReplicasChangedAt, at(-30*time.Minute)),
		},
		Spec:   appsv1.StatefulSetSpec{Replicas: ptr.To[int32](3)},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, Replicas: 3, ReadyReplicas: 0},
	}})

	s := summarize(cmIn("normal"), sumDef, ws, nil, sumNow, sumTimeout)
	if len(s.status.Unsettled) != maxUnsettled {
		t.Fatalf("unsettled has %d entries, want %d", len(s.status.Unsettled), maxUnsettled)
	}
	if s.status.Workloads.Total != 27 || s.status.Workloads.Scaling != 22 {
		t.Errorf("counts must cover every workload, got %+v", s.status.Workloads)
	}
	got := make([]string, 0, 6)
	for _, u := range s.status.Unsettled[:6] {
		got = append(got, u.Kind+"/"+u.Name)
	}
	want := []string{
		"Deployment/stalled-old", "StatefulSet/a-stalled-sts", "Deployment/stalled-new",
		"Deployment/invalid", "Deployment/scaling-00", "Deployment/scaling-01",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("order\n got: %v\nwant: %v", got, want)
	}
	for _, u := range s.status.Unsettled {
		if u.Name == "applying" {
			t.Errorf("Applying should sort after every Scaling workload and be cut off")
		}
	}
}

func TestSummarizeConditions(t *testing.T) {
	normal3 := map[string]string{AnnotationReplicas: "normal=3", AnnotationAppliedMode: "normal"}
	scaling := deploy("scaling", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(-time.Minute)))
	stalled := deploy("stalled", 3, 3, 1, normal3)
	applying := deploy("applying", 1, 1, 1, normal3)
	invalid := deploy("invalid", 3, 3, 3, map[string]string{AnnotationReplicas: "normal=x"})
	pending := deploy("pending", 3, 3, 3, map[string]string{
		AnnotationReplicas:    "normal=3",
		AnnotationAppliedMode: "normal", AnnotationScaleToZeroDelay: "5m", AnnotationZeroPendingSince: at(-time.Minute),
	})

	type cond struct{ status, reason string }
	cases := []struct {
		name                     string
		cm                       *modev1alpha1.ClusterMode
		def                      *modev1alpha1.ClusterModeDefinition
		ws                       []observedWorkload
		applied, ready, degraded cond
		message                  string // substring of any condition's message
	}{
		{
			name: "no workloads", cm: cmIn("normal"), def: sumDef,
			applied: cond{"True", "NoWorkloads"}, ready: cond{"True", "NoWorkloads"}, degraded: cond{"False", "AsExpected"},
		},
		{
			name: "every workload settled", cm: cmIn("normal"), def: sumDef,
			ws:      []observedWorkload{settled("a", "normal", 3, "normal=3"), settled("b", "normal", 3, "normal=3")},
			applied: cond{"True", "AllApplied"}, ready: cond{"True", "AllSettled"}, degraded: cond{"False", "AsExpected"},
		},
		{
			name: "scaling", cm: cmIn("normal"), def: sumDef,
			ws:      []observedWorkload{settled("a", "normal", 3, "normal=3"), scaling, scaling},
			applied: cond{"True", "AllApplied"}, ready: cond{"False", "Scaling"}, degraded: cond{"False", "AsExpected"},
			message: "2 of 3 workloads are scaling",
		},
		{
			name: "stalled comes before applying", cm: cmIn("normal"), def: sumDef,
			ws:      []observedWorkload{stalled, applying},
			applied: cond{"False", "Applying"}, ready: cond{"False", "Stalled"}, degraded: cond{"True", "StalledWorkloads"},
			message: "1 of 2 workloads is stalled",
		},
		{
			name: "pending", cm: cmIn("standby"), def: sumDef,
			ws:      []observedWorkload{pending},
			applied: cond{"False", "ScaleToZeroPending"}, ready: cond{"False", "ScaleToZeroPending"},
			degraded: cond{"False", "AsExpected"},
		},
		{
			name: "invalid workloads are left out of Applied and Ready", cm: cmIn("normal"), def: sumDef,
			ws:      []observedWorkload{settled("a", "normal", 3, "normal=3"), invalid},
			applied: cond{"True", "AllApplied"}, ready: cond{"True", "AllSettled"}, degraded: cond{"True", "InvalidWorkloads"},
			message: "1 invalid is left as it is",
		},
		{
			name: "invalid and stalled", cm: cmIn("normal"), def: sumDef,
			ws:      []observedWorkload{invalid, stalled},
			applied: cond{"True", "AllApplied"}, ready: cond{"False", "Stalled"},
			degraded: cond{"True", "InvalidAndStalledWorkloads"},
			message:  "1 workload has annotation errors and 1 is stalled",
		},
		{
			name: "definition not found", cm: cmIn("normal"), def: nil,
			ws:      []observedWorkload{settled("a", "normal", 3, "normal=3")},
			applied: cond{"False", "DefinitionNotFound"}, ready: cond{"False", "DefinitionNotFound"},
			degraded: cond{"True", "DefinitionNotFound"},
			message:  `ClusterModeDefinition "standard" not found`,
		},
		{
			name: "mode not in the definition", cm: cmIn("holiday"), def: sumDef,
			ws:      []observedWorkload{settled("a", "normal", 3, "normal=3")},
			applied: cond{"False", "InvalidMode"}, ready: cond{"False", "InvalidMode"}, degraded: cond{"True", "InvalidMode"},
			message: `Mode "holiday" isn't in ClusterModeDefinition "standard"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := summarize(c.cm, c.def, c.ws, nil, sumNow, sumTimeout).status
			var messages []string
			for typ, want := range map[string]cond{
				modev1alpha1.ConditionApplied: c.applied, modev1alpha1.ConditionReady: c.ready,
				modev1alpha1.ConditionDegraded: c.degraded,
			} {
				got := meta.FindStatusCondition(st.Conditions, typ)
				if got == nil {
					t.Fatalf("no %s condition", typ)
				}
				if string(got.Status) != want.status || got.Reason != want.reason {
					t.Errorf("%s = %s/%s, want %s/%s (%q)", typ, got.Status, got.Reason, want.status, want.reason, got.Message)
				}
				if got.ObservedGeneration != c.cm.Generation {
					t.Errorf("%s observedGeneration = %d, want %d", typ, got.ObservedGeneration, c.cm.Generation)
				}
				messages = append(messages, got.Message)
			}
			if !strings.Contains(strings.Join(messages, "\n"), c.message) {
				t.Errorf("no condition message contains %q:\n%s", c.message, strings.Join(messages, "\n"))
			}
			if c.def == nil || c.applied.reason == modev1alpha1.ReasonInvalidMode {
				if st.Workloads != (modev1alpha1.WorkloadCounts{}) || st.Unsettled != nil || st.Replicas != (modev1alpha1.ReplicaCounts{}) {
					t.Errorf("counts should be empty when the mode can't be applied, got %+v", st)
				}
			}
		})
	}
}

func TestSummarizeKeepsLastTransitionTime(t *testing.T) {
	earlier := metav1.Time{Time: sumNow.Add(-time.Hour)}
	cm := cmIn("normal")
	cm.Status.Conditions = []metav1.Condition{
		{Type: modev1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "AllSettled", LastTransitionTime: earlier},
		{Type: modev1alpha1.ConditionDegraded, Status: metav1.ConditionFalse, Reason: "AsExpected", LastTransitionTime: earlier},
	}
	normal3 := map[string]string{AnnotationReplicas: "normal=3", AnnotationAppliedMode: "normal"}
	st := summarize(cm, sumDef, []observedWorkload{
		settled("a", "normal", 3, "normal=3"),
		deploy("b", 3, 3, 3, with(normal3, AnnotationScaleToZeroDelay, "soon")),
	}, nil, sumNow, sumTimeout).status

	// Ready stays True with a new message, so its time is kept. Degraded
	// turns True, so its time moves to now.
	if got := meta.FindStatusCondition(st.Conditions, modev1alpha1.ConditionReady); !got.LastTransitionTime.Equal(&earlier) {
		t.Errorf("Ready lastTransitionTime = %v, want %v", got.LastTransitionTime, earlier)
	}
	if got := meta.FindStatusCondition(st.Conditions, modev1alpha1.ConditionDegraded); !got.LastTransitionTime.Time.Equal(sumNow) {
		t.Errorf("Degraded lastTransitionTime = %v, want %v", got.LastTransitionTime, sumNow)
	}
	// Conditions that weren't there before are added after the old ones.
	types := make([]string, 0, len(st.Conditions))
	for _, c := range st.Conditions {
		types = append(types, c.Type)
	}
	if strings.Join(types, ",") != "Ready,Degraded,Applied" {
		t.Errorf("condition order = %v", types)
	}
}

func TestSummarizeModeChange(t *testing.T) {
	t.Run("first sight of a mode records when, but isn't a change", func(t *testing.T) {
		cm := cmIn("normal")
		cm.Status = modev1alpha1.ClusterModeStatus{}
		s := summarize(cm, sumDef, nil, nil, sumNow.Add(400*time.Millisecond), sumTimeout)
		if s.modeChanged || s.status.Mode != "normal" || !s.status.ModeChangedAt.Time.Equal(sumNow) {
			t.Errorf("got modeChanged %v, mode %q, modeChangedAt %v", s.modeChanged, s.status.Mode, s.status.ModeChangedAt)
		}
	})
	t.Run("the same mode keeps modeChangedAt", func(t *testing.T) {
		s := summarize(cmIn("normal"), sumDef, nil, nil, sumNow, sumTimeout)
		if s.modeChanged || !s.status.ModeChangedAt.Time.Equal(sumNow.Add(-time.Hour)) {
			t.Errorf("got modeChanged %v, modeChangedAt %v", s.modeChanged, s.status.ModeChangedAt)
		}
	})
	t.Run("a new mode is a change", func(t *testing.T) {
		cm := cmIn("normal")
		cm.Spec.Mode = "peak"
		s := summarize(cm, sumDef, nil, nil, sumNow, sumTimeout)
		if !s.modeChanged || s.status.Mode != "peak" || !s.status.ModeChangedAt.Time.Equal(sumNow) {
			t.Errorf("got modeChanged %v, mode %q, modeChangedAt %v", s.modeChanged, s.status.Mode, s.status.ModeChangedAt)
		}
	})
}

func TestSummarizeStatusModes(t *testing.T) {
	cm := cmIn("normal")
	cm.Status.Modes = []modev1alpha1.ModeName{"normal", "old"}
	if got := summarize(cm, sumDef, nil, nil, sumNow, sumTimeout).status.Modes; !equality.Semantic.DeepEqual(got, sumModes) {
		t.Errorf("status.modes = %v, want the definition's %v", got, sumModes)
	}
	if got := summarize(cm, nil, nil, nil, sumNow, sumTimeout).status.Modes; !equality.Semantic.DeepEqual(got, cm.Status.Modes) {
		t.Errorf("with the definition deleted, status.modes = %v, want the last copy %v", got, cm.Status.Modes)
	}
	cm.Status.Modes = nil
	if got := summarize(cm, nil, nil, nil, sumNow, sumTimeout).status.Modes; got != nil {
		t.Errorf("with no definition ever, status.modes = %v, want none", got)
	}
}

func TestSummarizeRequeuesAtTheEarliestStall(t *testing.T) {
	normal3 := map[string]string{AnnotationReplicas: "normal=3", AnnotationAppliedMode: "normal"}
	s := summarize(cmIn("normal"), sumDef, []observedWorkload{
		deploy("a", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(-7*time.Minute))),
		deploy("b", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(-9*time.Minute))),
		settled("c", "normal", 3, "normal=3"),
	}, nil, sumNow, sumTimeout)
	if s.requeue != time.Minute {
		t.Errorf("requeue = %v, want 1m, when b stalls", s.requeue)
	}
	if s := summarize(cmIn("normal"), sumDef, []observedWorkload{settled("c", "normal", 3, "normal=3")},
		nil, sumNow, sumTimeout); s.requeue != 0 {
		t.Errorf("requeue = %v with nothing scaling, want 0", s.requeue)
	}
}

func TestSummarizeHistory(t *testing.T) {
	normal3 := map[string]string{
		AnnotationReplicas: "normal=3", AnnotationAppliedMode: "normal",
		AnnotationReplicasChangedAt: at(-24 * time.Hour),
	}
	key := "Deployment/shop/web"
	step := func(h map[string]workloadHistory, ready int32, after time.Duration) summary {
		return summarize(cmIn("normal"), sumDef, []observedWorkload{deploy("web", 3, 3, ready, normal3)},
			h, sumNow.Add(after), sumTimeout)
	}
	stateOf := func(s summary) modev1alpha1.WorkloadState {
		if len(s.status.Unsettled) == 0 {
			return modev1alpha1.WorkloadSettled
		}
		return s.status.Unsettled[0].State
	}

	// No history yet: a workload short of its count is timed from
	// replicas-changed-at, a day ago, so it is Stalled.
	s := step(nil, 1, 0)
	if _, known := s.history[key]; known || stateOf(s) != modev1alpha1.WorkloadStalled {
		t.Fatalf("with no history: state %s, history %v", stateOf(s), s.history)
	}
	// It settles, and stays settled for two minutes.
	s = step(s.history, 3, time.Minute)
	if got := s.history[key].settledSince; !got.Equal(sumNow.Add(time.Minute)) {
		t.Fatalf("settledSince = %v", got)
	}
	// Then a rollout: a fresh timeout from now.
	s = step(s.history, 2, 3*time.Minute)
	if got := s.history[key].shortSince; !got.Equal(sumNow.Add(3*time.Minute)) || stateOf(s) != modev1alpha1.WorkloadScaling {
		t.Fatalf("after settling: state %s, shortSince %v", stateOf(s), got)
	}
	// It flaps: at its count for 10 seconds every few minutes. The timeout
	// already running carries on, so it is Stalled 10 minutes after the
	// spell began.
	for _, back := range []time.Duration{7 * time.Minute, 11 * time.Minute} {
		s = step(s.history, 3, back)
		s = step(s.history, 2, back+10*time.Second)
	}
	if stateOf(s) != modev1alpha1.WorkloadScaling {
		t.Fatalf("at 11m10s, before the timeout: state %s", stateOf(s))
	}
	s = step(s.history, 2, 13*time.Minute)
	if stateOf(s) != modev1alpha1.WorkloadStalled || !s.history[key].shortSince.Equal(sumNow.Add(3*time.Minute)) {
		t.Fatalf("while flapping: state %s, history %+v", stateOf(s), s.history[key])
	}
	// While the mode can't be applied at all, the history is kept.
	blocked := summarize(cmIn("normal"), nil, []observedWorkload{deploy("web", 3, 3, 2, normal3)},
		s.history, sumNow.Add(12*time.Minute), sumTimeout)
	if !maps.Equal(blocked.history, s.history) {
		t.Errorf("history while the definition is missing = %v, want %v", blocked.history, s.history)
	}
	// While a mode is being applied, the history is dropped: the timeout
	// then runs from the new replicas-changed-at.
	applying := summarize(cmIn("normal"), sumDef, []observedWorkload{deploy("web", 1, 1, 1, normal3)},
		s.history, sumNow.Add(15*time.Minute), sumTimeout)
	if len(applying.history) != 0 {
		t.Errorf("history = %v, want empty", applying.history)
	}
}

// Writing the status and summarizing again with nothing changed must give
// the same status, or the reconciler would write forever.
func TestSummarizeIsStable(t *testing.T) {
	normal3 := map[string]string{AnnotationReplicas: "normal=3", AnnotationAppliedMode: "normal"}
	cm := cmIn("peak")
	cm.Status = modev1alpha1.ClusterModeStatus{}
	ws := []observedWorkload{
		settled("a", "peak", 6, "normal=3,peak=6"),
		deploy("b", 3, 3, 1, with(normal3, AnnotationReplicasChangedAt, at(-time.Minute))),
		deploy("c", 3, 3, 3, map[string]string{AnnotationReplicas: "normal=x"}),
	}
	first := summarize(cm, sumDef, ws, nil, sumNow.Add(300*time.Millisecond), sumTimeout)
	// Store it as the API server would: through JSON.
	raw, err := json.Marshal(first.status)
	if err != nil {
		t.Fatal(err)
	}
	var stored modev1alpha1.ClusterModeStatus
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	cm.Status = stored
	second := summarize(cm, sumDef, ws, first.history, sumNow.Add(1700*time.Millisecond), sumTimeout)
	if !equality.Semantic.DeepEqual(stored, second.status) {
		t.Errorf("status changed with nothing else changing:\nstored: %+v\nsecond: %+v", stored, second.status)
	}
}

func TestSummarizeClampsReplicaSums(t *testing.T) {
	big := map[string]string{AnnotationReplicas: "normal=2147483647", AnnotationAppliedMode: "normal"}
	st := summarize(cmIn("normal"), sumDef, []observedWorkload{
		deploy("a", math.MaxInt32, math.MaxInt32, math.MaxInt32, big),
		deploy("b", math.MaxInt32, math.MaxInt32, math.MaxInt32, big),
	}, nil, sumNow, sumTimeout).status
	want := modev1alpha1.ReplicaCounts{Desired: math.MaxInt32, Applied: math.MaxInt32, Ready: math.MaxInt32}
	if st.Replicas != want {
		t.Errorf("replicas = %+v, want %+v", st.Replicas, want)
	}
}

func TestModeSetBy(t *testing.T) {
	entry := func(manager string, minutesAgo int, subresource, fields string) metav1.ManagedFieldsEntry {
		return metav1.ManagedFieldsEntry{
			Manager: manager, Subresource: subresource,
			Time:     &metav1.Time{Time: sumNow.Add(-time.Duration(minutesAgo) * time.Minute)},
			FieldsV1: metav1.NewFieldsV1(fields),
		}
	}
	cases := []struct {
		name    string
		entries []metav1.ManagedFieldsEntry
		want    string
	}{
		{name: "nobody", want: ""},
		{name: "the mode manager", want: "mode-manager", entries: []metav1.ManagedFieldsEntry{
			entry("mode-manager", 10, "", `{"f:spec":{"f:definition":{},"f:mode":{}}}`),
			entry("mode-operator", 1, "status", `{"f:status":{"f:mode":{}}}`),
		}},
		{name: "a maintainer's edit after it", want: "kubectl-patch", entries: []metav1.ManagedFieldsEntry{
			entry("mode-manager", 10, "", `{"f:spec":{"f:definition":{}}}`),
			entry("kubectl-patch", 5, "", `{"f:spec":{"f:mode":{}}}`),
		}},
		{name: "shared ownership goes to the latest writer", want: "mode-manager", entries: []metav1.ManagedFieldsEntry{
			entry("kubectl-patch", 5, "", `{"f:spec":{"f:mode":{}}}`),
			entry("mode-manager", 2, "", `{"f:spec":{"f:mode":{}}}`),
		}},
		{name: "labels alone don't count", want: "", entries: []metav1.ManagedFieldsEntry{
			entry("kubectl-label", 1, "", `{"f:metadata":{"f:labels":{"f:mode":{}}}}`),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cm := cmIn("normal")
			cm.ManagedFields = c.entries
			if got := modeSetBy(cm); got != c.want {
				t.Errorf("modeSetBy = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSummarizeCopiesTransitionsAndDefaultMode(t *testing.T) {
	def := sumDef.DeepCopy()
	def.Spec.Transitions = []modev1alpha1.ModeTransition{{From: "peak", To: []modev1alpha1.ModeName{"normal", "standby"}}}
	st := summarize(cmIn("normal"), def, nil, nil, sumNow, sumTimeout).status
	if st.DefaultMode != "standby" || !equality.Semantic.DeepEqual(st.Transitions, def.Spec.Transitions) {
		t.Fatalf("defaultMode %q, transitions %+v; want the definition's", st.DefaultMode, st.Transitions)
	}

	// With the definition deleted, the last copies are kept, so writes are
	// still checked against them.
	cm := cmIn("normal")
	cm.Status.DefaultMode, cm.Status.Transitions = st.DefaultMode, st.Transitions
	kept := summarize(cm, nil, nil, nil, sumNow, sumTimeout).status
	if kept.DefaultMode != "standby" || !equality.Semantic.DeepEqual(kept.Transitions, def.Spec.Transitions) {
		t.Errorf("with the definition deleted: defaultMode %q, transitions %+v", kept.DefaultMode, kept.Transitions)
	}

	// A definition that drops its transitions removes the copy.
	if got := summarize(cm, sumDef, nil, nil, sumNow, sumTimeout).status.Transitions; got != nil {
		t.Errorf("transitions = %+v after the definition dropped them, want none", got)
	}
}

func TestSummarizeWaitsForPodsShuttingDown(t *testing.T) {
	ann := map[string]string{
		AnnotationReplicas: "normal=3", AnnotationAppliedMode: "standby",
		AnnotationReplicasChangedAt: at(-time.Minute),
	}
	dep := observedWorkload{kind: workload.Deployment{}, obj: &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", Generation: 2, Annotations: ann},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr.To[int32](0)},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, TerminatingReplicas: ptr.To[int32](2)},
	}}
	sts := observedWorkload{kind: workload.StatefulSet{}, obj: &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", Generation: 2, Annotations: ann},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To[int32](0)},
		Status:     appsv1.StatefulSetStatus{ObservedGeneration: 2, Replicas: 1, ReadyReplicas: 1},
	}}
	st := summarize(cmIn("standby"), sumDef, []observedWorkload{dep, sts}, nil, sumNow, sumTimeout).status
	if st.Workloads.Scaling != 2 {
		t.Fatalf("counts = %+v, want both Scaling", st.Workloads)
	}
	messages := map[string]string{}
	for _, u := range st.Unsettled {
		messages[u.Kind] = u.Message
	}
	if got := messages["Deployment"]; got != "2 pods still to stop" {
		t.Errorf("Deployment message = %q", got)
	}
	if got := messages["StatefulSet"]; got != "1 pod still to stop" {
		t.Errorf("StatefulSet message = %q", got)
	}
	if meta.FindStatusCondition(st.Conditions, modev1alpha1.ConditionReady).Status != metav1.ConditionFalse {
		t.Errorf("Ready should stay False while pods are shutting down")
	}
}

func TestProgressMessage(t *testing.T) {
	cases := []struct {
		name    string
		desired int32
		p       workload.Progress
		want    string
	}{
		{name: "scaling up", desired: 3, p: workload.Progress{Ready: 1}, want: "1 of 3 replicas ready"},
		{
			name: "ready but not settled", desired: 3, p: workload.Progress{Ready: 3},
			want: "3 replicas ready, waiting for the Deployment to settle at 3",
		},
		{name: "scaled to zero", desired: 0, p: workload.Progress{Stopping: 1}, want: "1 pod still to stop"},
		// A StatefulSet going from 5 to 3 counts all 5 as ready until they stop.
		{
			name: "scaling down", desired: 3, p: workload.Progress{Ready: 5, Stopping: 2},
			want: "3 of 3 replicas ready; 2 pods still to stop",
		},
		{
			name: "a pod stuck terminating", desired: 3, p: workload.Progress{Ready: 2, Stopping: 1},
			want: "2 of 3 replicas ready; 1 pod still to stop",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := progressMessage("Deployment", c.desired, c.p); got != c.want {
				t.Errorf("progressMessage = %q, want %q", got, c.want)
			}
		})
	}
}
