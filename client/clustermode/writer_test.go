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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func observed(mode, rv, owner string) *ClusterMode {
	return &ClusterMode{Mode: mode, ResourceVersion: rv, ModeOwner: owner}
}

func TestObserve(t *testing.T) {
	w := &Writer{}
	if adopted, _ := w.Observe(nil); adopted {
		t.Errorf("a missing ClusterMode is never adopted")
	}
	// Before the Writer has written anything, the mode it sees is adopted.
	if adopted, by := w.Observe(observed("normal", "5", "kubectl-patch")); !adopted || by != "kubectl-patch" {
		t.Errorf("first observation = %v, %q; want adopted by kubectl-patch", adopted, by)
	}
	if adopted, _ := w.Observe(observed("normal", "6", "kubectl-patch")); adopted {
		t.Errorf("an update that keeps the current mode, such as a status change, isn't adopted")
	}

	// The Writer's own write, as Apply records it.
	w.current, w.hasCurrent, w.lastRV = "peak", true, "10"
	cases := []struct {
		name    string
		cm      *ClusterMode
		adopted bool
		by      string
	}{
		{name: "its own write", cm: observed("peak", "10", "mode-manager")},
		{name: "an event from before its write", cm: observed("normal", "9", "kubectl-patch")},
		{
			name: "a newer change by another writer", cm: observed("standby", "12", "orchestrator"),
			adopted: true, by: "orchestrator",
		},
		{name: "the adopted mode again", cm: observed("standby", "13", "orchestrator")},
	}
	for _, c := range cases {
		adopted, by := w.Observe(c.cm)
		if adopted != c.adopted || by != c.by {
			t.Errorf("%s: Observe = %v, %q; want %v, %q", c.name, adopted, by, c.adopted, c.by)
		}
	}
	if mode, ok := w.Current(); !ok || mode != "standby" {
		t.Errorf("Current() = %q, %v; want the adopted standby", mode, ok)
	}

	// A resourceVersion that can't be compared counts as newer.
	w.current, w.lastRV = "peak", "10"
	if adopted, _ := w.Observe(observed("normal", "not-a-number", "kubectl-patch")); !adopted {
		t.Errorf("a change with an unparsable resourceVersion should be adopted")
	}
}

func TestReasonOf(t *testing.T) {
	wrapped := fmt.Errorf("moving: %w", &Error{Reason: ReasonNotSettled, From: "normal", Mode: "peak"})
	if got := ReasonOf(wrapped); got != ReasonNotSettled {
		t.Errorf("ReasonOf(wrapped) = %q, want NotSettled", got)
	}
	if got := ReasonOf(errors.New("other")); got != "" {
		t.Errorf("ReasonOf(other) = %q, want empty", got)
	}
	cause := errors.New("api")
	e := &Error{Reason: ReasonRejected, Err: cause}
	if !errors.Is(e, cause) {
		t.Errorf("an Error unwraps to its cause")
	}
	if e.Error() != "the API server rejected the change: api" {
		t.Errorf("Error() = %q", e.Error())
	}
}

func managedFields(manager, subresource string, at time.Time, fields string) metav1.ManagedFieldsEntry {
	return metav1.ManagedFieldsEntry{
		Manager: manager, Subresource: subresource, Time: &metav1.Time{Time: at},
		FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(fields),
	}
}

func TestModeOwner(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	mode := `{"f:spec":{"f:mode":{}}}`
	cases := []struct {
		name    string
		entries []metav1.ManagedFieldsEntry
		want    string
	}{
		{name: "the mode manager", want: "mode-manager", entries: []metav1.ManagedFieldsEntry{
			managedFields("mode-manager", "", t0, `{"f:spec":{"f:mode":{},"f:definition":{}}}`),
		}},
		{name: "a later maintainer edit", want: "kubectl-patch", entries: []metav1.ManagedFieldsEntry{
			managedFields("mode-manager", "", t0, mode),
			managedFields("kubectl-patch", "", t0.Add(time.Minute), mode),
		}},
		{name: "status writes don't count", want: "mode-manager", entries: []metav1.ManagedFieldsEntry{
			managedFields("mode-manager", "", t0, mode),
			managedFields("mode-operator", "status", t0.Add(time.Minute), `{"f:status":{"f:mode":{}}}`),
		}},
		{name: "labels alone don't count", want: "", entries: []metav1.ManagedFieldsEntry{
			managedFields("kubectl-label", "", t0, `{"f:metadata":{"f:labels":{"f:mode":{}}}}`),
		}},
		{name: "a tie goes to the first name", want: "a-tool", entries: []metav1.ManagedFieldsEntry{
			managedFields("b-tool", "", t0, mode),
			managedFields("a-tool", "", t0, mode),
		}},
	}
	for _, c := range cases {
		if got := modeOwner(c.entries); got != c.want {
			t.Errorf("%s: modeOwner = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestClusterModeFrom(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mode.bitasylum.org/v1alpha1",
		"kind":       "ClusterMode",
		"metadata": map[string]any{
			"name": "default", "uid": "u1", "resourceVersion": "42", "generation": int64(3),
			"labels": map[string]any{WriterLabel: "mode-manager.ops"},
		},
		"spec": map[string]any{"definition": "standard", "mode": "peak"},
		"status": map[string]any{
			"observedGeneration": int64(3),
			"modes":              []any{"normal", "peak", "standby"},
			"defaultMode":        "standby",
			"transitions":        []any{map[string]any{"from": "peak", "to": []any{"normal"}}},
			"mode":               "peak",
			"workloads":          map[string]any{"total": int64(2), "settled": int64(2)},
			"conditions": []any{map[string]any{
				"type": "Ready", "status": "True", "reason": "AllSettled", "message": "",
				"lastTransitionTime": "2026-10-17T09:00:00Z",
			}},
		},
	}}
	cm, err := clusterModeFrom(u)
	if err != nil {
		t.Fatal(err)
	}
	if cm.Name != "default" || cm.Mode != "peak" || cm.Definition != "standard" || cm.Generation != 3 ||
		cm.ResourceVersion != "42" || cm.Labels[WriterLabel] != "mode-manager.ops" {
		t.Errorf("metadata or spec not decoded: %+v", cm)
	}
	if !cm.Settled() || cm.Status.Workloads.Total != 2 || cm.Condition(ConditionReady) == nil {
		t.Errorf("status not decoded: %+v", cm.Status)
	}
	rules, ok := cm.Rules()
	if !ok || !rules.Allowed("peak", "normal") || rules.Allowed("peak", "nope") {
		t.Errorf("rules not decoded: %+v", rules)
	}

	// Settled needs the status to describe the latest generation and mode.
	stale := *cm
	stale.Generation = 4
	other := *cm
	other.Mode = "normal"
	if stale.Settled() || other.Settled() {
		t.Errorf("Settled must check observedGeneration and status.mode")
	}
}
