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

package modespec

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
)

var modes = sets.New("normal", "peak", "offpeak", "maintenance", "standby")

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		max     int32
		want    map[string]int32
		wantErr string
	}{
		{name: "listed modes, unlisted get 0", raw: "normal=3,peak=6",
			want: map[string]int32{"normal": 3, "peak": 6, "offpeak": 0, "standby": 0}},
		{name: "whitespace around entries, keys and values", raw: " normal = 2 , *=1 ",
			want: map[string]int32{"normal": 2, "peak": 1, "maintenance": 1}},
		{name: "wildcard before a listed mode", raw: "*=0,maintenance=1",
			want: map[string]int32{"maintenance": 1, "normal": 0}},
		{name: "wildcard only", raw: "*=4", want: map[string]int32{"normal": 4, "standby": 4}},
		{name: "empty entries are ignored", raw: ",normal=1,,", want: map[string]int32{"normal": 1, "peak": 0}},
		{name: "largest int32", raw: "normal=2147483647", want: map[string]int32{"normal": math.MaxInt32}},
		{name: "VM counts of 0 and 1", raw: "normal=1,*=0", max: 1,
			want: map[string]int32{"normal": 1, "peak": 0}},

		{name: "empty", raw: "", wantErr: "no entries"},
		{name: "only separators", raw: " , ,", wantErr: "no entries"},
		{name: "no =", raw: "normal", wantErr: "expected mode=count"},
		{name: "no mode name", raw: "=3", wantErr: "missing mode name"},
		{name: "negative", raw: "normal=-1", wantErr: "integer of 0 or more"},
		{name: "not a number", raw: "normal=x", wantErr: "integer of 0 or more"},
		{name: "empty count", raw: "normal=", wantErr: "integer of 0 or more"},
		{name: "too large for int32", raw: "normal=2147483648", wantErr: "integer of 0 or more"},
		{name: "unknown mode", raw: "bogus=1", wantErr: "isn't in the ClusterModeDefinition"},
		{name: "mode names are case-sensitive", raw: "Normal=1", wantErr: "isn't in the ClusterModeDefinition"},
		{name: "duplicate mode", raw: "normal=1,normal=2", wantErr: `mode "normal" is given more than once`},
		{name: "duplicate wildcard", raw: "*=1,*=2", wantErr: `"*" is given more than once`},
		{name: "VM count above 1", raw: "normal=2", max: 1, wantErr: "integer from 0 to 1"},
		{name: "VM wildcard above 1", raw: "normal=1,*=2", max: 1, wantErr: "integer from 0 to 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			maxCount := c.max
			if maxCount == 0 {
				maxCount = math.MaxInt32
			}
			sp, err := Parse(c.raw, modes, maxCount)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("Parse(%q) error = %v, want one containing %q", c.raw, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.raw, err)
			}
			for mode, want := range c.want {
				if got := sp.ReplicasFor(mode); got != want {
					t.Errorf("Parse(%q).ReplicasFor(%q) = %d, want %d", c.raw, mode, got, want)
				}
			}
		})
	}
}

func TestParseUnknownModeIsDetectable(t *testing.T) {
	_, err := Parse("normal=1,holiday=2", modes, math.MaxInt32)
	if !errors.Is(err, ErrUnknownMode) {
		t.Fatalf("error %v doesn't wrap ErrUnknownMode", err)
	}
	if !strings.Contains(err.Error(), `"holiday=2"`) {
		t.Errorf("error %q doesn't name the entry", err)
	}
}

func TestParseDelay(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{raw: "", want: 0},
		{raw: "   ", want: 0},
		{raw: "0", want: 0},
		{raw: "0s", want: 0},
		{raw: "90s", want: 90 * time.Second},
		{raw: " 5m ", want: 5 * time.Minute},
		{raw: "1h30m", want: 90 * time.Minute},
		{raw: "-1s", wantErr: true},
		{raw: "5", wantErr: true},
		{raw: "soon", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseDelay(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseDelay(%q) = %v, want an error", c.raw, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseDelay(%q) = %v, %v; want %v, nil", c.raw, got, err, c.want)
		}
	}
}

func TestParseSettleTimeout(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{raw: "", want: 0},
		{raw: " ", want: 0},
		{raw: "20m", want: 20 * time.Minute},
		{raw: " 45s ", want: 45 * time.Second},
		{raw: "0", wantErr: true},
		{raw: "0s", wantErr: true},
		{raw: "-5m", wantErr: true},
		{raw: "ten minutes", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseSettleTimeout(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseSettleTimeout(%q) = %v, want an error", c.raw, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseSettleTimeout(%q) = %v, %v; want %v, nil", c.raw, got, err, c.want)
		}
	}
}
