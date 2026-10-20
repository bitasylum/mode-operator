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
	"errors"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/resourceversion"
)

// Writer changes a ClusterMode's mode with server-side apply. It is safe for
// concurrent use; writes are made one at a time.
type Writer struct {
	reader       *Reader
	fieldManager string
	// definition and writerLabel are set for the mode manager's writer only.
	definition  string
	writerLabel string

	mu sync.Mutex
	// current is the mode this Writer last wrote or adopted.
	current    string
	hasCurrent bool
	// lastRV is the resourceVersion of the last change it wrote or adopted.
	lastRV   string
	onChange func(Change)
}

// Change is a change of the current mode, as a [Writer] saw it.
type Change struct {
	Mode string
	// By is the field manager that made the change: the Writer's own, or
	// someone else's.
	By string
	// Adopted is true when someone else made the change.
	Adopted     bool
	ClusterMode *ClusterMode
}

// NewModeManagerWriter returns the mode manager's Writer. Every write sets
// spec.mode, spec.definition to definition, and the
// mode.bitasylum.org/writer label to writerLabel, with field manager
// "mode-manager", and creates the ClusterMode if it doesn't exist. It needs
// the access mode-operator's clustermode-writer role gives.
func NewModeManagerWriter(r *Reader, definition, writerLabel string) *Writer {
	return &Writer{
		reader:       r,
		fieldManager: ModeManagerFieldManager,
		definition:   definition,
		writerLabel:  writerLabel,
	}
}

// NewWriter returns a Writer for anyone but the mode manager, such as an
// orchestration server. It writes spec.mode only, with the given field
// manager, and never creates the ClusterMode. The mode manager takes its
// change as the current mode. It needs get, list, watch and patch access to
// the ClusterMode.
func NewWriter(r *Reader, fieldManager string) *Writer {
	return &Writer{reader: r, fieldManager: fieldManager}
}

// Reader returns the Writer's Reader.
func (w *Writer) Reader() *Reader { return w.reader }

// Definition returns the ClusterModeDefinition the mode manager's Writer
// writes into spec.definition, or "" for other writers.
func (w *Writer) Definition() string { return w.definition }

// OnChange sets fn to be called with every change of the current mode: each
// of the Writer's own writes, and each change by someone else that
// [Writer.Observe] adopts. The calls are made one at a time while the Writer
// holds its lock, in the order the API server stored the changes, so fn must
// not call the Writer. The mode manager broadcasts from fn, which keeps what
// it broadcasts in step with the ClusterMode. Set it before using the
// Writer.
func (w *Writer) OnChange(fn func(Change)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onChange = fn
}

func (w *Writer) isModeManager() bool { return w.definition != "" }

// Check reports whether the API server would accept changing the mode to
// mode now, and returns the ClusterMode it checked. It returns an [*Error]
// with [ReasonUnknownMode], [ReasonTransitionNotAllowed] or
// [ReasonNotSettled] when it wouldn't, and, for the mode manager's Writer,
// [ReasonDefinitionMismatch] if the ClusterMode follows another definition.
// Changing to the current mode is accepted while it is one of the allowed
// modes. For the mode manager's Writer, a missing ClusterMode is checked
// against its definition, and the returned ClusterMode is nil.
func (w *Writer) Check(ctx context.Context, mode string) (*ClusterMode, error) {
	cm, err := w.reader.Get(ctx)
	if ReasonOf(err) == ReasonNotFound && w.isModeManager() {
		def, derr := w.reader.Definition(ctx, w.definition)
		if derr != nil {
			return nil, derr
		}
		if !def.Has(mode) {
			return nil, &Error{Reason: ReasonUnknownMode, Mode: mode}
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cm, w.check(ctx, cm, mode)
}

func (w *Writer) check(ctx context.Context, cm *ClusterMode, mode string) error {
	if w.isModeManager() && cm.Definition != w.definition {
		return &Error{
			Reason: ReasonDefinitionMismatch, Mode: mode,
			Err: fmt.Errorf("the ClusterMode follows %q, not %q", cm.Definition, w.definition),
		}
	}
	rules, err := w.reader.Rules(ctx, cm)
	if err != nil {
		return err
	}
	if !rules.Has(mode) {
		return &Error{Reason: ReasonUnknownMode, Mode: mode}
	}
	if mode == cm.Mode || !rules.Restricted() || mode == rules.DefaultMode {
		return nil
	}
	if !rules.Allowed(cm.Mode, mode) {
		return &Error{Reason: ReasonTransitionNotAllowed, From: cm.Mode, Mode: mode}
	}
	if !cm.Settled() {
		return &Error{Reason: ReasonNotSettled, From: cm.Mode, Mode: mode}
	}
	return nil
}

// Set checks the change with [Writer.Check], and makes it with
// [Writer.Apply] if the check passes.
func (w *Writer) Set(ctx context.Context, mode string) (*ClusterMode, error) {
	if _, err := w.Check(ctx, mode); err != nil {
		return nil, err
	}
	return w.Apply(ctx, mode)
}

// Apply writes mode without checking it first, and returns the ClusterMode
// the API server stored. The API server still checks the change; when it
// rejects it, Apply re-reads the ClusterMode and returns an [*Error] with the
// reason, or [ReasonRejected] if re-reading doesn't show one.
func (w *Writer) Apply(ctx context.Context, mode string) (*ClusterMode, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version,
		"kind":       "ClusterMode",
		"metadata":   map[string]any{"name": w.reader.name},
		"spec":       map[string]any{"mode": mode},
	}}
	if w.isModeManager() {
		if w.writerLabel != "" {
			obj.SetLabels(map[string]string{WriterLabel: w.writerLabel})
		}
		if err := unstructured.SetNestedField(obj.Object, w.definition, "spec", "definition"); err != nil {
			return nil, err
		}
	} else if _, err := w.reader.Get(ctx); err != nil {
		// Applying to a missing ClusterMode would create one, which only the
		// mode manager does.
		return nil, err
	}

	u, err := w.reader.client.Resource(ClusterModeResource).Apply(ctx, w.reader.name, obj,
		metav1.ApplyOptions{FieldManager: w.fieldManager, Force: true})
	if err != nil {
		return nil, w.classify(ctx, mode, err)
	}
	cm, err := clusterModeFrom(u)
	if err != nil {
		return nil, err
	}
	w.current, w.hasCurrent, w.lastRV = mode, true, cm.ResourceVersion
	if w.onChange != nil {
		w.onChange(Change{Mode: mode, By: w.fieldManager, ClusterMode: cm})
	}
	return cm, nil
}

// classify turns an API server rejection into an [*Error], re-reading the
// ClusterMode to find out why rather than parsing the message.
func (w *Writer) classify(ctx context.Context, mode string, err error) error {
	// Another writer's apply to a ClusterMode deleted since it checked tries
	// to create one, which RBAC or the schema then rejects.
	if !w.isModeManager() && (apierrors.IsForbidden(err) || apierrors.IsInvalid(err)) {
		if _, gerr := w.reader.Get(ctx); ReasonOf(gerr) == ReasonNotFound {
			return &Error{Reason: ReasonNotFound, Mode: mode, Err: err}
		}
	}
	switch {
	case apierrors.IsForbidden(err):
		return &Error{Reason: ReasonForbidden, Mode: mode, Err: err}
	case apierrors.IsNotFound(err):
		return w.reader.apiError(ctx, err, ReasonNotFound)
	case apierrors.IsInvalid(err):
		cm, gerr := w.reader.Get(ctx)
		if r := ReasonOf(gerr); r == ReasonNotFound || r == ReasonNotInstalled {
			return &Error{Reason: r, Mode: mode, Err: err}
		}
		if gerr != nil {
			return &Error{Reason: ReasonRejected, Mode: mode, Err: err}
		}
		if e, ok := errors.AsType[*Error](w.check(ctx, cm, mode)); ok {
			return &Error{Reason: e.Reason, Mode: e.Mode, From: e.From, Err: err}
		}
		return &Error{Reason: ReasonRejected, Mode: mode, Err: err}
	}
	return err
}

// Observe tells the Writer about the ClusterMode as a watch reports it, and
// reports whether it holds a mode the Writer didn't write: a change by a
// maintainer or another writer. The mode manager takes such a change as its
// current mode, without writing it back. by is the field manager that made
// it.
//
// Events older than the last change the Writer wrote or adopted are ignored,
// so its own writes and the changes they replaced are never adopted. An
// adopted change is passed to the function set with [Writer.OnChange].
func (w *Writer) Observe(cm *ClusterMode) (adopted bool, by string) {
	if cm == nil {
		return false, ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.hasCurrent && cm.Mode == w.current {
		return false, ""
	}
	if w.lastRV != "" {
		if c, err := resourceversion.CompareResourceVersion(cm.ResourceVersion, w.lastRV); err == nil && c <= 0 {
			return false, ""
		}
	}
	w.current, w.hasCurrent, w.lastRV = cm.Mode, true, cm.ResourceVersion
	if w.onChange != nil {
		w.onChange(Change{Mode: cm.Mode, By: cm.ModeOwner, Adopted: true, ClusterMode: cm})
	}
	return true, cm.ModeOwner
}

// Current returns the mode the Writer last wrote or adopted, and false if it
// has done neither.
func (w *Writer) Current() (mode string, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current, w.hasCurrent
}
