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

package startup

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/bitasylum/mode-operator/client/clustermode"
	"github.com/bitasylum/mode-operator/client/fingerprint"
)

// FingerprintStore loads and saves the fingerprint. A [*fingerprint.Store]
// is one.
type FingerprintStore interface {
	Load(ctx context.Context) (*fingerprint.Record, error)
	Save(ctx context.Context, r fingerprint.Record) error
}

// Deps are what [Run] reads and writes with.
type Deps struct {
	// Writer is the mode manager's Writer, from
	// [clustermode.NewModeManagerWriter]. Its definition is the one the
	// decision reads.
	Writer       *clustermode.Writer
	Fingerprints FingerprintStore
	// Nodes returns the nodes' boot IDs now, for example by calling
	// [fingerprint.Take].
	Nodes func(ctx context.Context) (fingerprint.BootIDs, error)
	// Recorder, if set, records the decision's event on the ClusterMode.
	Recorder record.EventRecorder
	// Now, if set, replaces time.Now.
	Now func() time.Time
}

// Result is what [Run] decided and wrote.
type Result struct {
	Decision Decision
	// ClusterMode is the ClusterMode as written, or nil if nothing was.
	ClusterMode *clustermode.ClusterMode
	// Fingerprint is the record saved, or nil if none was.
	Fingerprint *fingerprint.Record
}

// Run makes the startup decision and carries it out, in this order:
//
//  1. Write the ClusterMode with the decided mode, creating it if missing.
//  2. Save the fingerprint with the nodes' boot IDs now.
//  3. Record the decision's event on the ClusterMode.
//
// The order makes a failure safe to retry: until the fingerprint is saved,
// the old one stays in place, so running again reaches the same decision.
// An error reading any input stops Run before it writes anything, so a read
// that fails is never taken for a missing object. Without the CRDs, Run
// writes nothing and returns the CaseNotInstalled decision; call [Persist]
// once they are installed. A ClusterMode that follows another definition
// than the Writer's is an error with [clustermode.ReasonDefinitionMismatch],
// since spec.definition can't be changed.
func Run(ctx context.Context, d Deps, o Options) (Result, error) {
	r := d.Writer.Reader()
	installed, err := r.Available(ctx)
	if err != nil {
		return Result{}, err
	}
	in := Inputs{Installed: installed}
	if installed {
		in.Definition, err = r.Definition(ctx, d.Writer.Definition())
		if clustermode.ReasonOf(err) == clustermode.ReasonDefinitionNotFound {
			in.Definition, err = nil, nil
		}
		if err != nil {
			return Result{}, fmt.Errorf("reading the ClusterModeDefinition: %w", err)
		}
		in.ClusterMode, err = r.Get(ctx)
		if clustermode.ReasonOf(err) == clustermode.ReasonNotFound {
			in.ClusterMode, err = nil, nil
		}
		if err != nil {
			return Result{}, fmt.Errorf("reading the ClusterMode: %w", err)
		}
		if cm := in.ClusterMode; cm != nil && cm.Definition != d.Writer.Definition() {
			return Result{}, &clustermode.Error{
				Reason: clustermode.ReasonDefinitionMismatch,
				Err:    fmt.Errorf("ClusterMode %s follows %q, not %q", cm.Name, cm.Definition, d.Writer.Definition()),
			}
		}
		if in.Fingerprint, err = d.Fingerprints.Load(ctx); err != nil {
			return Result{}, err
		}
	}
	if in.Live, err = d.Nodes(ctx); err != nil {
		return Result{}, err
	}
	if o.ClusterMode == "" {
		o.ClusterMode = r.Name()
	}
	dec := Decide(in, o)
	if !dec.Persist {
		return Result{Decision: dec}, nil
	}
	return write(ctx, d, dec, in.Live, d.now())
}

// Persist writes mode and a new fingerprint, for a mode manager that
// started before the CRDs were installed and has run in mode since. It
// records no event.
func Persist(ctx context.Context, d Deps, mode string, startedAt time.Time) (Result, error) {
	live, err := d.Nodes(ctx)
	if err != nil {
		return Result{}, err
	}
	return write(ctx, d, Decision{Case: CaseNotInstalled, Mode: mode, Persist: true}, live, startedAt)
}

func write(ctx context.Context, d Deps, dec Decision, live fingerprint.BootIDs, startedAt time.Time) (Result, error) {
	res := Result{Decision: dec}
	cm, err := d.Writer.Apply(ctx, dec.Mode)
	if err != nil {
		return res, fmt.Errorf("writing the ClusterMode: %w", err)
	}
	res.ClusterMode = cm
	rec := fingerprint.Record{
		ClusterMode:       cm.Name,
		BootIDs:           live,
		UpdatedAt:         d.now().UTC(),
		LastStartupAt:     startedAt.UTC(),
		RestoredOnStartup: dec.Restored,
	}
	if err := d.Fingerprints.Save(ctx, rec); err != nil {
		return res, fmt.Errorf("saving the fingerprint: %w", err)
	}
	res.Fingerprint = &rec
	if dec.Event != nil && d.Recorder != nil {
		d.Recorder.Event(Reference(cm), dec.Event.Type, dec.Event.Reason, dec.Event.Message)
	}
	return res, nil
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Reference returns a reference to cm for recording events on it.
func Reference(cm *clustermode.ClusterMode) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		APIVersion: clustermode.Group + "/" + clustermode.Version,
		Kind:       "ClusterMode",
		Name:       cm.Name,
		UID:        cm.UID,
	}
}

// NewRecorder returns an EventRecorder that records events as component,
// and a function that stops it. Events about a ClusterMode, which is
// cluster-scoped, are recorded in the default namespace, so the recorder
// needs create and patch access to Events there.
func NewRecorder(ctx context.Context, client kubernetes.Interface, component string) (record.EventRecorder, func()) {
	b := record.NewBroadcaster(record.WithContext(ctx))
	b.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: client.CoreV1().Events("")})
	return b.NewRecorder(scheme.Scheme, corev1.EventSource{Component: component}), b.Shutdown
}
