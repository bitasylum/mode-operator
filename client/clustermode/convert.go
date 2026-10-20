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
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// The objects as the API serves them, holding only the fields the package
// reads.
type wireClusterMode struct {
	Metadata metav1.ObjectMeta `json:"metadata"`
	Spec     struct {
		Definition string `json:"definition"`
		Mode       string `json:"mode"`
	} `json:"spec"`
	Status Status `json:"status"`
}

type wireDefinition struct {
	Metadata metav1.ObjectMeta `json:"metadata"`
	Spec     struct {
		Modes       []string     `json:"modes"`
		DefaultMode string       `json:"defaultMode"`
		Transitions []Transition `json:"transitions"`
	} `json:"spec"`
}

func clusterModeFrom(u *unstructured.Unstructured) (*ClusterMode, error) {
	var w wireClusterMode
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &w); err != nil {
		return nil, fmt.Errorf("decoding ClusterMode %q: %w", u.GetName(), err)
	}
	return &ClusterMode{
		Name:            w.Metadata.Name,
		UID:             w.Metadata.UID,
		ResourceVersion: w.Metadata.ResourceVersion,
		Generation:      w.Metadata.Generation,
		Labels:          w.Metadata.Labels,
		Definition:      w.Spec.Definition,
		Mode:            w.Spec.Mode,
		ModeOwner:       modeOwner(w.Metadata.ManagedFields),
		Status:          w.Status,
	}, nil
}

func definitionFrom(u *unstructured.Unstructured) (*Definition, error) {
	var w wireDefinition
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &w); err != nil {
		return nil, fmt.Errorf("decoding ClusterModeDefinition %q: %w", u.GetName(), err)
	}
	return &Definition{
		Name: w.Metadata.Name,
		Rules: Rules{
			Modes:       w.Spec.Modes,
			DefaultMode: w.Spec.DefaultMode,
			Transitions: w.Spec.Transitions,
		},
	}, nil
}

// modeOwner returns the field manager that most recently set spec.mode,
// choosing the alphabetically first on a tie, as mode-operator does for
// status.modeSetBy.
func modeOwner(entries []metav1.ManagedFieldsEntry) string {
	var owner string
	var latest time.Time
	for _, mf := range entries {
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
	return owner
}
