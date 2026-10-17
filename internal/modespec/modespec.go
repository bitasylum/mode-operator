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

// Package modespec parses the annotations that say how a workload is scaled
// in each mode.
//
// The replicas annotation has the form "normal=3,peak=6,*=0":
//   - each entry is mode=count, where count is an integer of 0 or more
//   - "*" sets the count for every mode that isn't listed
//   - a mode that isn't listed, with no "*", gets 0
//
// Every mode named must be in the ClusterModeDefinition that the workload's
// ClusterMode uses. The caller passes those modes in.
package modespec

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
)

// Wildcard is the key that sets the count for modes that aren't listed.
const Wildcard = "*"

// ErrUnknownMode is wrapped by the error Parse returns when an entry names a
// mode that isn't in the definition.
var ErrUnknownMode = errors.New("mode isn't in the ClusterModeDefinition")

// Spec is a parsed replicas annotation.
type Spec struct {
	perMode  map[string]int32
	wildcard *int32
}

// Parse parses a replicas annotation. Every mode it names must be in modes,
// and every count must be from 0 to maxCount.
func Parse(raw string, modes sets.Set[string], maxCount int32) (Spec, error) {
	sp := Spec{perMode: map[string]int32{}}
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return Spec{}, fmt.Errorf("entry %q: expected mode=count", part)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" {
			return Spec{}, fmt.Errorf("entry %q: missing mode name", part)
		}
		n, err := strconv.ParseInt(value, 10, 32)
		if err != nil || n < 0 || n > int64(maxCount) {
			return Spec{}, fmt.Errorf("entry %q: %s", part, countRule(maxCount))
		}
		count := int32(n)
		if key == Wildcard {
			if sp.wildcard != nil {
				return Spec{}, fmt.Errorf("%q is given more than once", Wildcard)
			}
			sp.wildcard = &count
			continue
		}
		if !modes.Has(key) {
			return Spec{}, fmt.Errorf("entry %q: %w", part, ErrUnknownMode)
		}
		if _, dup := sp.perMode[key]; dup {
			return Spec{}, fmt.Errorf("mode %q is given more than once", key)
		}
		sp.perMode[key] = count
	}
	if len(sp.perMode) == 0 && sp.wildcard == nil {
		return Spec{}, errors.New("no entries")
	}
	return sp, nil
}

func countRule(maxCount int32) string {
	if maxCount == math.MaxInt32 {
		return "count must be an integer of 0 or more"
	}
	return fmt.Sprintf("count must be an integer from 0 to %d", maxCount)
}

// ReplicasFor returns the count for mode.
func (s Spec) ReplicasFor(mode string) int32 {
	if n, ok := s.perMode[mode]; ok {
		return n
	}
	if s.wildcard != nil {
		return *s.wildcard
	}
	return 0
}

// ParseDelay parses a scale-to-zero-delay annotation. Empty or "0" means no
// delay.
func ParseDelay(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%q isn't a duration, for example 90s or 5m", raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("%q is negative", raw)
	}
	return d, nil
}

// ParseSettleTimeout parses a settle-timeout annotation, which must be greater
// than 0. It returns 0 for an empty value, meaning the controller's default
// applies.
func ParseSettleTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%q isn't a duration, for example 10m", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be greater than 0", raw)
	}
	return d, nil
}
