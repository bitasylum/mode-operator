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
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	modev1alpha1 "github.com/bitasylum/mode-operator/api/v1alpha1"
	"github.com/bitasylum/mode-operator/internal/workload"
)

// maxUnsettled is the most workloads status.unsettled lists.
const maxUnsettled = 20

// maxMessage is the CRD's MaxLength for status.unsettled[].message.
const maxMessage = 1024

// minSettled is how long a workload must stay at its count before falling
// short again starts a fresh settle timeout. Shorter spells, such as a
// crash-looping pod that passes readiness for a few seconds, carry on the
// timeout that was already running.
const minSettled = time.Minute

// workloadHistory is what the status reconciler remembers about a workload
// whose count is applied, between summaries.
type workloadHistory struct {
	// settledSince is when the workload was first seen at its count, while it
	// still is. It is zero while the workload is short.
	settledSince time.Time
	// shortSince is when the workload's current spell short of its count
	// began, if that is known.
	shortSince time.Time
}

// observedWorkload is a workload that follows a ClusterMode, with its kind.
type observedWorkload struct {
	kind workload.Kind
	obj  client.Object
}

func (w observedWorkload) key() string {
	return w.kind.Name() + "/" + w.obj.GetNamespace() + "/" + w.obj.GetName()
}

// summary is what summarize works out for one ClusterMode.
type summary struct {
	status modev1alpha1.ClusterModeStatus
	// requeue is how long until a Scaling workload would become Stalled, or
	// 0 if none would. No watch event fires when a timeout passes.
	requeue time.Duration
	// modeChanged is true when status.mode moves from one mode to another,
	// which the mode-change counter counts.
	modeChanged bool
	// history is what the next summary needs to remember, by workload.
	history map[string]workloadHistory
}

// report is one workload's place in the status.
type report struct {
	w                 observedWorkload
	state             modev1alpha1.WorkloadState
	invalidAnnotation bool
	desired           *int32
	applied, ready    int32
	since             *time.Time
	message           string
	// stallAt is when a Scaling workload becomes Stalled.
	stallAt time.Time
}

// summarize works out a ClusterMode's status from its definition (nil if it
// doesn't exist) and the workloads that follow it. history is the previous
// summary's, and defaultSettleTimeout is --settle-timeout.
//
// Every time it writes is truncated to whole seconds, as the API server
// stores them, so an unchanged status compares equal to the stored one.
func summarize(cm *modev1alpha1.ClusterMode, def *modev1alpha1.ClusterModeDefinition, workloads []observedWorkload,
	history map[string]workloadHistory, now time.Time, defaultSettleTimeout time.Duration,
) summary {
	nowTS := now.UTC().Truncate(time.Second)
	st := modev1alpha1.ClusterModeStatus{
		ObservedGeneration: cm.Generation,
		Modes:              cm.Status.Modes,
		DefaultMode:        cm.Status.DefaultMode,
		Transitions:        cm.Status.Transitions,
		Mode:               cm.Spec.Mode,
		ModeChangedAt:      cm.Status.ModeChangedAt,
		ModeSetBy:          modeSetBy(cm),
		Conditions:         slices.Clone(cm.Status.Conditions),
	}
	var s summary
	if cm.Status.Mode != cm.Spec.Mode || st.ModeChangedAt == nil {
		st.ModeChangedAt = &metav1.Time{Time: nowTS}
		s.modeChanged = cm.Status.Mode != "" && cm.Status.Mode != cm.Spec.Mode
	}
	mode := string(cm.Spec.Mode)

	if def == nil {
		// Keep the last copies of the modes and transitions, so writes are
		// still checked.
		setBlockedConditions(&st, nowTS, modev1alpha1.ReasonDefinitionNotFound,
			fmt.Sprintf("ClusterModeDefinition %q not found; workloads are left as they are", cm.Spec.Definition))
		s.status, s.history = st, history
		return s
	}
	spec := def.Spec.DeepCopy()
	st.Modes, st.DefaultMode, st.Transitions = spec.Modes, spec.DefaultMode, spec.Transitions
	modes := sets.New[string]()
	for _, m := range def.Spec.Modes {
		modes.Insert(string(m))
	}
	if !modes.Has(mode) {
		setBlockedConditions(&st, nowTS, modev1alpha1.ReasonInvalidMode,
			fmt.Sprintf("Mode %q isn't in ClusterModeDefinition %q; workloads are left as they are", mode, def.Name))
		s.status, s.history = st, history
		return s
	}

	s.history = map[string]workloadHistory{}
	reports := make([]report, 0, len(workloads))
	for _, w := range workloads {
		r := classify(w, mode, modes, st.ModeChangedAt.Time, history, s.history, now, defaultSettleTimeout)
		reports = append(reports, r)
		if r.state == modev1alpha1.WorkloadScaling {
			if d := r.stallAt.Sub(now); s.requeue == 0 || d < s.requeue {
				s.requeue = d
			}
		}
	}

	for _, r := range reports {
		countState(&st.Workloads, r.state)
		if r.invalidAnnotation {
			st.InvalidAnnotations++
		}
		if r.state != modev1alpha1.WorkloadInvalid {
			addClamped(&st.Replicas.Desired, *r.desired)
			addClamped(&st.Replicas.Applied, r.applied)
			addClamped(&st.Replicas.Ready, r.ready)
		}
	}
	st.Unsettled = unsettled(reports)
	setConditions(&st, nowTS, mode)
	s.status = st
	return s
}

// classify puts one workload in exactly one state. The states are checked in
// order, and the first that matches wins. It records what the next summary
// needs to know about the workload in newHistory.
func classify(w observedWorkload, mode string, modes sets.Set[string], modeChangedAt time.Time,
	history, newHistory map[string]workloadHistory, now time.Time, defaultSettleTimeout time.Duration,
) report {
	ann := w.obj.GetAnnotations()
	ua := parseUserAnnotations(ann, modes, w.kind.MaxCount())
	cur := w.kind.Current(w.obj)
	progress := w.kind.Progress(w.obj)
	r := report{
		w:                 w,
		invalidAnnotation: ua.replicasErr != nil || ua.delayErr != nil || ua.settleTimeoutErr != nil,
		applied:           cur,
		ready:             progress.Ready,
	}

	if ua.replicasErr != nil {
		r.state, r.message = modev1alpha1.WorkloadInvalid, ua.replicasErr.Error()
		return r
	}
	desired := ua.replicas.ReplicasFor(mode)
	r.desired = &desired
	if desired == 0 && cur > 0 && ua.delayErr != nil {
		r.state = modev1alpha1.WorkloadInvalid
		r.message = fmt.Sprintf("%s; holding %d replicas until it is fixed", ua.delayErr, cur)
		return r
	}

	if pending := ann[AnnotationZeroPendingSince]; desired == 0 && pending != "" {
		r.state = modev1alpha1.WorkloadPending
		r.message = "Waiting out the scale-to-zero delay"
		if since, err := time.Parse(time.RFC3339, pending); err == nil {
			r.since = &since
			r.message = fmt.Sprintf("Scaling to 0 at %s, when the scale-to-zero delay ends",
				since.Add(ua.delay).UTC().Format(time.RFC3339))
		}
		return r
	}

	if appliedMode := ann[AnnotationAppliedMode]; !w.kind.Holds(w.obj, desired) || appliedMode != mode {
		r.state = modev1alpha1.WorkloadApplying
		r.message = fmt.Sprintf("Replicas are %d; mode %q wants %d", cur, mode, desired)
		if appliedMode != mode {
			r.since = &modeChangedAt
		}
		return r
	}

	nowTS := now.UTC().Truncate(time.Second)
	h, known := history[w.key()]
	if progress.Reached {
		r.state = modev1alpha1.WorkloadSettled
		if h.settledSince.IsZero() {
			h.settledSince = nowTS
		}
		newHistory[w.key()] = h
		return r
	}

	// The count is applied but not reached. The settle timeout runs from the
	// last change to the count. If the workload has stayed at its count for
	// minSettled since then, it runs instead from when it was first seen short
	// again, so a rollout or a crash long after the last mode change gets the
	// whole timeout too. With no history, as after a restart, only the change
	// counts, so a stalled workload stays Stalled.
	start := modeChangedAt
	if t, err := time.Parse(time.RFC3339, ann[AnnotationReplicasChangedAt]); err == nil {
		start = t
	}
	if known {
		if !h.settledSince.IsZero() && nowTS.Sub(h.settledSince) >= minSettled {
			h.shortSince = nowTS
		}
		h.settledSince = time.Time{}
		newHistory[w.key()] = h
		if h.shortSince.After(start) {
			start = h.shortSince
		}
	}
	r.since = &start

	timeout := defaultSettleTimeout
	if ua.settleTimeout > 0 {
		timeout = ua.settleTimeout
	}
	where := progressMessage(w.kind.Name(), desired, progress)
	if deadline := start.Add(timeout); now.Before(deadline) {
		r.state, r.message, r.stallAt = modev1alpha1.WorkloadScaling, where, deadline
		return r
	}
	r.state = modev1alpha1.WorkloadStalled
	r.message = fmt.Sprintf("Not settled within the %s settle timeout: %s", timeout, where)
	return r
}

// progressMessage says how far a workload short of its count has got. While
// pods are still to stop, the ready count shown stops at desired, because a
// StatefulSet counts its ready pods above the count too.
func progressMessage(kind string, desired int32, p workload.Progress) string {
	switch {
	case p.Stopping > 0 && desired == 0:
		return fmt.Sprintf("%d %s still to stop", p.Stopping, pods(p.Stopping))
	case p.Stopping > 0:
		return fmt.Sprintf("%d of %d replicas ready; %d %s still to stop",
			min(p.Ready, desired), desired, p.Stopping, pods(p.Stopping))
	case p.Ready < desired:
		return fmt.Sprintf("%d of %d replicas ready", p.Ready, desired)
	default:
		return fmt.Sprintf("%d replicas ready, waiting for the %s to settle at %d", p.Ready, kind, desired)
	}
}

func pods(n int32) string {
	if n == 1 {
		return "pod"
	}
	return "pods"
}

// addClamped adds n to *sum, stopping at the largest int32 rather than
// overflowing. Counts come from user annotations and can be that large.
func addClamped(sum *int32, n int32) {
	*sum = int32(min(int64(*sum)+int64(n), math.MaxInt32))
}

func countState(c *modev1alpha1.WorkloadCounts, state modev1alpha1.WorkloadState) {
	c.Total++
	switch state {
	case modev1alpha1.WorkloadSettled:
		c.Settled++
	case modev1alpha1.WorkloadScaling:
		c.Scaling++
	case modev1alpha1.WorkloadStalled:
		c.Stalled++
	case modev1alpha1.WorkloadApplying:
		c.Applying++
	case modev1alpha1.WorkloadPending:
		c.Pending++
	case modev1alpha1.WorkloadInvalid:
		c.Invalid++
	}
}

// unsettledOrder is the order of states in status.unsettled, most urgent
// first.
var unsettledOrder = map[modev1alpha1.WorkloadState]int{
	modev1alpha1.WorkloadStalled:  0,
	modev1alpha1.WorkloadInvalid:  1,
	modev1alpha1.WorkloadScaling:  2,
	modev1alpha1.WorkloadApplying: 3,
	modev1alpha1.WorkloadPending:  4,
}

// unsettled lists up to maxUnsettled workloads that aren't Settled: by state,
// then oldest since first (unknown last), then by kind, namespace and name.
func unsettled(reports []report) []modev1alpha1.UnsettledWorkload {
	var list []report
	for _, r := range reports {
		if r.state != modev1alpha1.WorkloadSettled {
			list = append(list, r)
		}
	}
	slices.SortFunc(list, func(a, b report) int {
		if c := cmp.Compare(unsettledOrder[a.state], unsettledOrder[b.state]); c != 0 {
			return c
		}
		switch {
		case a.since != nil && b.since == nil:
			return -1
		case a.since == nil && b.since != nil:
			return 1
		case a.since != nil && b.since != nil:
			if c := a.since.Compare(*b.since); c != 0 {
				return c
			}
		}
		return cmp.Or(
			cmp.Compare(a.w.kind.Name(), b.w.kind.Name()),
			cmp.Compare(a.w.obj.GetNamespace(), b.w.obj.GetNamespace()),
			cmp.Compare(a.w.obj.GetName(), b.w.obj.GetName()),
		)
	})

	if len(list) == 0 {
		// nil, not empty, to match a stored status with no list.
		return nil
	}
	out := make([]modev1alpha1.UnsettledWorkload, 0, min(len(list), maxUnsettled))
	for _, r := range list[:min(len(list), maxUnsettled)] {
		u := modev1alpha1.UnsettledWorkload{
			Kind:      r.w.kind.Name(),
			Namespace: r.w.obj.GetNamespace(),
			Name:      r.w.obj.GetName(),
			State:     r.state,
			Desired:   r.desired,
			Applied:   r.applied,
			Ready:     r.ready,
			Message:   truncate(r.message, maxMessage),
		}
		if r.since != nil {
			u.Since = &metav1.Time{Time: r.since.UTC().Truncate(time.Second)}
		}
		out = append(out, u)
	}
	return out
}

// setConditions sets Applied, Ready and Degraded from the counts.
func setConditions(st *modev1alpha1.ClusterModeStatus, now time.Time, mode string) {
	c := st.Workloads
	of := func(n int32, what string) string {
		return fmt.Sprintf("%d of %d workloads %s %s", n, c.Total, isAre(n), what)
	}
	exceptInvalid := ""
	if c.Invalid > 0 {
		exceptInvalid = fmt.Sprintf("; %d invalid %s left as %s", c.Invalid, isAre(c.Invalid), itThey(c.Invalid))
	}

	applied := condition(modev1alpha1.ConditionApplied, metav1.ConditionTrue, modev1alpha1.ReasonAllApplied,
		fmt.Sprintf("Mode %q is applied to every workload%s", mode, exceptInvalid))
	ready := condition(modev1alpha1.ConditionReady, metav1.ConditionTrue, modev1alpha1.ReasonAllSettled,
		fmt.Sprintf("Every workload has settled in mode %q%s", mode, exceptInvalid))
	switch {
	case c.Total == 0:
		applied = condition(modev1alpha1.ConditionApplied, metav1.ConditionTrue, modev1alpha1.ReasonNoWorkloads,
			"No workloads follow this ClusterMode")
		ready = condition(modev1alpha1.ConditionReady, metav1.ConditionTrue, modev1alpha1.ReasonNoWorkloads,
			"No workloads follow this ClusterMode")
	case c.Applying > 0:
		applied = condition(modev1alpha1.ConditionApplied, metav1.ConditionFalse, modev1alpha1.ReasonApplying,
			of(c.Applying, fmt.Sprintf("waiting for mode %q to be applied", mode)))
	case c.Pending > 0:
		applied = condition(modev1alpha1.ConditionApplied, metav1.ConditionFalse, modev1alpha1.ReasonScaleToZeroPending,
			of(c.Pending, "waiting out their scale-to-zero delay"))
	}
	switch {
	case c.Total == 0:
	case c.Stalled > 0:
		ready = condition(modev1alpha1.ConditionReady, metav1.ConditionFalse, modev1alpha1.ReasonStalled,
			of(c.Stalled, "stalled"))
	case c.Applying > 0:
		ready = condition(modev1alpha1.ConditionReady, metav1.ConditionFalse, modev1alpha1.ReasonApplying,
			of(c.Applying, fmt.Sprintf("waiting for mode %q to be applied", mode)))
	case c.Scaling > 0:
		ready = condition(modev1alpha1.ConditionReady, metav1.ConditionFalse, modev1alpha1.ReasonScaling,
			of(c.Scaling, "scaling"))
	case c.Pending > 0:
		ready = condition(modev1alpha1.ConditionReady, metav1.ConditionFalse, modev1alpha1.ReasonScaleToZeroPending,
			of(c.Pending, "waiting out their scale-to-zero delay"))
	}

	var degraded metav1.Condition
	switch inv, stalled := st.InvalidAnnotations, c.Stalled; {
	case inv > 0 && stalled > 0:
		degraded = condition(modev1alpha1.ConditionDegraded, metav1.ConditionTrue,
			modev1alpha1.ReasonInvalidAndStalledWorkloads,
			fmt.Sprintf("%d %s annotation errors and %d %s stalled", inv, workloadsHave(inv), stalled, isAre(stalled)))
	case inv > 0:
		degraded = condition(modev1alpha1.ConditionDegraded, metav1.ConditionTrue, modev1alpha1.ReasonInvalidWorkloads,
			fmt.Sprintf("%d %s annotation errors", inv, workloadsHave(inv)))
	case stalled > 0:
		degraded = condition(modev1alpha1.ConditionDegraded, metav1.ConditionTrue, modev1alpha1.ReasonStalledWorkloads,
			fmt.Sprintf("%d %s stalled", stalled, workloadsAre(stalled)))
	default:
		degraded = condition(modev1alpha1.ConditionDegraded, metav1.ConditionFalse, modev1alpha1.ReasonAsExpected,
			"No annotation errors or stalled workloads")
	}

	for _, cond := range []metav1.Condition{applied, ready, degraded} {
		cond.ObservedGeneration = st.ObservedGeneration
		cond.LastTransitionTime = metav1.Time{Time: now}
		meta.SetStatusCondition(&st.Conditions, cond)
	}
}

// setBlockedConditions reports that the mode can't be applied at all. That
// stops every workload, so Degraded is True as well.
func setBlockedConditions(st *modev1alpha1.ClusterModeStatus, now time.Time, reason, message string) {
	for _, cond := range []metav1.Condition{
		condition(modev1alpha1.ConditionApplied, metav1.ConditionFalse, reason, message),
		condition(modev1alpha1.ConditionReady, metav1.ConditionFalse, reason, message),
		condition(modev1alpha1.ConditionDegraded, metav1.ConditionTrue, reason, message),
	} {
		cond.ObservedGeneration = st.ObservedGeneration
		cond.LastTransitionTime = metav1.Time{Time: now}
		meta.SetStatusCondition(&st.Conditions, cond)
	}
}

func condition(t string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: t, Status: status, Reason: reason, Message: message}
}

func isAre(n int32) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func itThey(n int32) string {
	if n == 1 {
		return "it is"
	}
	return "they are"
}

func workloadsAre(n int32) string {
	if n == 1 {
		return "workload is"
	}
	return "workloads are"
}

func workloadsHave(n int32) string {
	if n == 1 {
		return "workload has"
	}
	return "workloads have"
}

// modeSetBy returns the field manager that owns spec.mode. If more than one
// does, it returns the one that wrote most recently.
func modeSetBy(cm *modev1alpha1.ClusterMode) string {
	var owner string
	var latest time.Time
	for _, mf := range cm.ManagedFields {
		if mf.Subresource != "" || mf.FieldsV1 == nil {
			continue
		}
		var fields struct {
			Spec map[string]json.RawMessage `json:"f:spec"`
		}
		if err := json.Unmarshal(mf.FieldsV1.GetRawBytes(), &fields); err != nil {
			continue
		}
		if _, ok := fields.Spec["f:mode"]; !ok {
			continue
		}
		var t time.Time
		if mf.Time != nil {
			t = mf.Time.Time
		}
		if owner == "" || t.After(latest) || (t.Equal(latest) && mf.Manager < owner) {
			owner, latest = mf.Manager, t
		}
	}
	return truncate(owner, 128)
}
