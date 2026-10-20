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
	"slices"
	"testing"
)

// exampleRules are the README's example: standby is the default, and
// maintenance has no rule.
func exampleRules() Rules {
	return Rules{
		Modes:       []string{"normal", "peak", "offpeak", "maintenance", "standby"},
		DefaultMode: "standby",
		Transitions: []Transition{
			{From: "normal", To: []string{"peak", "offpeak"}},
			{From: "peak", To: []string{"normal"}},
			{From: "offpeak", To: []string{"normal"}},
			{From: "standby", To: []string{"normal", "maintenance"}},
		},
	}
}

func TestAllowed(t *testing.T) {
	r := exampleRules()
	cases := []struct {
		from, to string
		want     bool
	}{
		{"normal", "peak", true},
		{"peak", "offpeak", false},
		{"peak", "standby", true},
		{"maintenance", "normal", false},
		{"maintenance", "standby", true},
		{"normal", "normal", true},
		{"normal", "holiday", false},
		// A mode removed from the definition can still reach the default.
		{"removed", "standby", true},
		{"removed", "normal", false},
	}
	for _, c := range cases {
		if got := r.Allowed(c.from, c.to); got != c.want {
			t.Errorf("Allowed(%s, %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}

	unrestricted := Rules{Modes: r.Modes, DefaultMode: "standby"}
	if !unrestricted.Allowed("peak", "maintenance") || unrestricted.Allowed("peak", "holiday") {
		t.Errorf("without transitions, any mode of the definition may follow any other")
	}
	if unrestricted.Restricted() || !r.Restricted() {
		t.Errorf("Restricted should follow whether there are transitions")
	}
}

func TestNext(t *testing.T) {
	r := exampleRules()
	cases := map[string][]string{
		"standby":     {"normal", "maintenance"},
		"peak":        {"normal", "standby"},
		"maintenance": {"standby"},
	}
	for from, want := range cases {
		if got := r.Next(from); !slices.Equal(got, want) {
			t.Errorf("Next(%s) = %v, want %v", from, got, want)
		}
	}
}

func TestPlan(t *testing.T) {
	r := exampleRules()
	cases := []struct {
		from, to string
		want     []string
		reason   Reason
	}{
		{from: "peak", to: "maintenance", want: []string{"standby", "maintenance"}},
		{from: "normal", to: "offpeak", want: []string{"offpeak"}},
		{from: "maintenance", to: "peak", want: []string{"standby", "normal", "peak"}},
		{from: "offpeak", to: "peak", want: []string{"normal", "peak"}},
		{from: "standby", to: "standby"},
		{from: "standby", to: "holiday", reason: ReasonUnknownMode},
	}
	for _, c := range cases {
		got, err := r.Plan(c.from, c.to)
		if ReasonOf(err) != c.reason || !slices.Equal(got, c.want) {
			t.Errorf("Plan(%s, %s) = %v, %v; want %v, reason %q", c.from, c.to, got, err, c.want, c.reason)
		}
	}

	// standby has no rule, so nothing can leave it.
	stuck := Rules{
		Modes:       []string{"normal", "standby"},
		DefaultMode: "standby",
		Transitions: []Transition{{From: "normal", To: []string{"standby"}}},
	}
	if _, err := stuck.Plan("standby", "normal"); ReasonOf(err) != ReasonNoPath {
		t.Errorf("Plan with no way out = %v, want NoPath", err)
	}

	// Of two equally short paths, the one through the earlier mode wins.
	tie := Rules{
		Modes:       []string{"a", "b", "c", "d"},
		DefaultMode: "d",
		Transitions: []Transition{
			{From: "d", To: []string{"c", "b"}},
			{From: "b", To: []string{"a"}},
			{From: "c", To: []string{"a"}},
		},
	}
	if got, _ := tie.Plan("d", "a"); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("Plan(d, a) = %v, want [b a]", got)
	}
}

func TestClusterModeRules(t *testing.T) {
	cm := &ClusterMode{}
	if _, ok := cm.Rules(); ok {
		t.Errorf("a ClusterMode without a status has no rules")
	}
	r := exampleRules()
	cm.Status = Status{Modes: r.Modes, DefaultMode: r.DefaultMode, Transitions: r.Transitions}
	got, ok := cm.Rules()
	if !ok || !slices.Equal(got.Modes, r.Modes) || got.DefaultMode != "standby" || len(got.Transitions) != 4 {
		t.Errorf("Rules() = %+v, %v; want the status copy", got, ok)
	}
	got.Transitions[0].To[0] = "changed"
	if cm.Status.Transitions[0].To[0] != "peak" {
		t.Errorf("Rules() must return a copy")
	}
}
