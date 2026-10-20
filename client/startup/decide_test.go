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
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/bitasylum/mode-operator/client/clustermode"
	"github.com/bitasylum/mode-operator/client/fingerprint"
)

func TestDecide(t *testing.T) {
	def := &clustermode.Definition{Name: "standard", Rules: clustermode.Rules{
		Modes: []string{"normal", "peak", "standby"}, DefaultMode: "standby",
	}}
	inPeak := &clustermode.ClusterMode{Name: "default", Mode: "peak"}
	stored := &fingerprint.Record{ClusterMode: "default", BootIDs: fingerprint.BootIDs{"a": "1", "b": "2"}}
	opts := Options{ClusterMode: "default", BuiltinDefault: "standby"}

	cases := []struct {
		name     string
		in       Inputs
		opts     Options
		want     Case
		mode     string
		restored bool
		persist  bool
		event    string
		warning  bool
	}{
		{
			name: "the CRDs aren't installed", in: Inputs{},
			want: CaseNotInstalled, mode: "standby",
		},
		{
			name: "the definition is missing", in: Inputs{Installed: true, ClusterMode: inPeak},
			want: CaseDefinitionNotFound, mode: "standby", persist: true, event: ReasonDefinitionNotFound, warning: true,
		},
		// The API server checks the write against the copy mode-operator kept.
		{
			name: "the definition is missing, and the ClusterMode kept a copy of its rules",
			in: Inputs{Installed: true, ClusterMode: &clustermode.ClusterMode{
				Name: "default", Mode: "peak", Status: clustermode.Status{DefaultMode: "normal"},
			}},
			want: CaseDefinitionNotFound, mode: "normal", persist: true, event: ReasonDefinitionNotFound, warning: true,
		},
		{
			name: "the ClusterMode is missing", in: Inputs{Installed: true, Definition: def},
			want: CaseClusterModeNotFound, mode: "standby", persist: true, event: ReasonModeReset,
		},
		{
			name: "the stored mode left the definition",
			in:   Inputs{Installed: true, Definition: def, ClusterMode: &clustermode.ClusterMode{Mode: "holiday"}},
			want: CaseInvalidStoredMode, mode: "standby", persist: true, event: ReasonInvalidStoredMode, warning: true,
		},
		{
			name: "the mode manager doesn't know the stored mode",
			in:   Inputs{Installed: true, Definition: def, ClusterMode: inPeak, Fingerprint: stored},
			opts: Options{ClusterMode: "default", BuiltinDefault: "standby", KnownModes: []string{"normal", "standby"}},
			want: CaseInvalidStoredMode, mode: "standby", persist: true, event: ReasonInvalidStoredMode, warning: true,
		},
		{
			name: "no fingerprint", in: Inputs{Installed: true, Definition: def, ClusterMode: inPeak},
			want: CaseNoFingerprint, mode: "standby", persist: true, event: ReasonModeReset,
		},
		{
			name: "a fingerprint with no boot IDs",
			in: Inputs{
				Installed: true, Definition: def, ClusterMode: inPeak,
				Fingerprint: &fingerprint.Record{ClusterMode: "default"},
			},
			want: CaseNoFingerprint, mode: "standby", persist: true, event: ReasonModeReset,
		},
		{
			name: "another ClusterMode's fingerprint",
			in: Inputs{
				Installed: true, Definition: def, ClusterMode: inPeak,
				Fingerprint: &fingerprint.Record{ClusterMode: "other", BootIDs: stored.BootIDs}, Live: stored.BootIDs,
			},
			want: CaseNoFingerprint, mode: "standby", persist: true, event: ReasonModeReset,
		},
		{
			name: "a node stayed up",
			in: Inputs{
				Installed: true, Definition: def, ClusterMode: inPeak, Fingerprint: stored,
				Live: fingerprint.BootIDs{"a": "10", "b": "2"},
			},
			want: CaseOverlap, mode: "peak", restored: true, persist: true, event: ReasonModeRestored,
		},
		{
			name: "every node rebooted",
			in: Inputs{
				Installed: true, Definition: def, ClusterMode: inPeak, Fingerprint: stored,
				Live: fingerprint.BootIDs{"a": "10", "b": "20"},
			},
			want: CaseFullReboot, mode: "standby", persist: true, event: ReasonModeReset,
		},
		{
			name: "no live boot IDs never match",
			in: Inputs{
				Installed: true, Definition: def, ClusterMode: inPeak, Fingerprint: stored,
				Live: fingerprint.BootIDs{},
			},
			want: CaseFullReboot, mode: "standby", persist: true, event: ReasonModeReset,
		},
		{
			name: "the definition's default isn't known",
			in:   Inputs{Installed: true, Definition: def},
			opts: Options{ClusterMode: "default", BuiltinDefault: "normal", KnownModes: []string{"normal", "peak"}},
			want: CaseClusterModeNotFound, mode: "normal", persist: true, event: ReasonModeReset,
		},
	}
	for _, c := range cases {
		o := opts
		if c.opts.BuiltinDefault != "" {
			o = c.opts
		}
		got := Decide(c.in, o)
		if got.Case != c.want || got.Mode != c.mode || got.Restored != c.restored || got.Persist != c.persist {
			t.Errorf("%s: Decide = %+v; want case %s, mode %s, restored %v, persist %v",
				c.name, got, c.want, c.mode, c.restored, c.persist)
			continue
		}
		switch {
		case c.event == "" && got.Event != nil:
			t.Errorf("%s: unexpected event %+v", c.name, got.Event)
		case c.event != "" && (got.Event == nil || got.Event.Reason != c.event ||
			(got.Event.Type == corev1.EventTypeWarning) != c.warning):
			t.Errorf("%s: event %+v, want %s (warning %v)", c.name, got.Event, c.event, c.warning)
		}
	}

	up := Decide(Inputs{
		Installed: true, Definition: def, ClusterMode: inPeak, Fingerprint: stored,
		Live: fingerprint.BootIDs{"x": "2", "y": "1"},
	}, opts)
	if up.Case != CaseOverlap || !slices.Equal(up.Overlap, []string{"a", "b"}) {
		t.Errorf("boot IDs match by value, whatever the node is called now: %+v", up)
	}
}
