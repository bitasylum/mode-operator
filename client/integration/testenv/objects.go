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

package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var definitions = schema.GroupVersionResource{
	Group: "mode.bitasylum.org", Version: "v1alpha1", Resource: "clustermodedefinitions",
}

// Transition is a rule of a definition's transitions.
type Transition struct {
	From string   `json:"from"`
	To   []string `json:"to"`
}

// Definition creates a ClusterModeDefinition and returns its name.
func (e *Env) Definition(
	ctx context.Context, modes []string, defaultMode string, transitions ...Transition,
) (string, error) {
	name := e.Name("definition")
	spec := map[string]any{"modes": toAny(modes), "defaultMode": defaultMode}
	if len(transitions) > 0 {
		ts := make([]any, 0, len(transitions))
		for _, t := range transitions {
			ts = append(ts, map[string]any{"from": t.From, "to": toAny(t.To)})
		}
		spec["transitions"] = ts
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mode.bitasylum.org/v1alpha1",
		"kind":       "ClusterModeDefinition",
		"metadata":   map[string]any{"name": name},
		"spec":       spec,
	}}
	if _, err := e.Dynamic.Resource(definitions).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("creating ClusterModeDefinition: %w", err)
	}
	return name, nil
}

// SetTransitions replaces a definition's transitions; nil removes them.
func (e *Env) SetTransitions(ctx context.Context, definition string, transitions []Transition) error {
	var value any
	if transitions != nil {
		value = transitions
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"transitions": value}})
	if err != nil {
		return err
	}
	_, err = e.Dynamic.Resource(definitions).Patch(ctx, definition, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// ClusterModeStatus is the part of a ClusterMode's status the fake operator
// writes.
type ClusterModeStatus struct {
	// Ready is the Ready condition's status.
	Ready bool
	// Stale makes observedGeneration lag the generation by one.
	Stale bool
	// Mode, when set, is written as status.mode instead of spec.mode.
	Mode string
	// WithoutRules leaves out the copies of the definition's rules.
	WithoutRules bool
}

// Operate writes a ClusterMode's status as mode-operator would: the copies
// of its definition's rules, the mode, observedGeneration and the Ready
// condition.
func (e *Env) Operate(ctx context.Context, name string, s ClusterModeStatus) error {
	cm, err := e.Dynamic.Resource(clusterModes).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	mode, _, _ := unstructured.NestedString(cm.Object, "spec", "mode")
	if s.Mode != "" {
		mode = s.Mode
	}
	generation := cm.GetGeneration()
	if s.Stale {
		generation--
	}
	readyStatus, reason := metav1.ConditionFalse, "Scaling"
	if s.Ready {
		readyStatus, reason = metav1.ConditionTrue, "AllSettled"
	}
	status := map[string]any{
		"observedGeneration": generation,
		"mode":               mode,
		"conditions": []any{map[string]any{
			"type": "Ready", "status": string(readyStatus), "reason": reason, "message": "set by the test",
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		}},
	}
	if !s.WithoutRules {
		defName, _, _ := unstructured.NestedString(cm.Object, "spec", "definition")
		def, err := e.Dynamic.Resource(definitions).Get(ctx, defName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		spec, _, _ := unstructured.NestedMap(def.Object, "spec")
		status["modes"] = spec["modes"]
		status["defaultMode"] = spec["defaultMode"]
		status["transitions"] = spec["transitions"]
	}
	patch, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		return err
	}
	_, err = e.Dynamic.Resource(clusterModes).Patch(ctx, name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: "mode-operator"}, "status")
	return err
}

// PatchMode changes a ClusterMode's mode with a merge patch, as a
// maintainer's kubectl patch does, with the given field manager.
func (e *Env) PatchMode(ctx context.Context, name, mode, fieldManager string) error {
	patch := fmt.Sprintf(`{"spec":{"mode":%q}}`, mode)
	_, err := e.Dynamic.Resource(clusterModes).Patch(ctx, name, types.MergePatchType, []byte(patch),
		metav1.PatchOptions{FieldManager: fieldManager})
	return err
}

// Namespace creates a namespace and returns its name.
func (e *Env) Namespace(ctx context.Context) (string, error) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.Name("ns")}}
	if _, err := e.Kube.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	return ns.Name, nil
}

// DeleteClusterMode deletes a ClusterMode, ignoring that it may not exist.
func (e *Env) DeleteClusterMode(ctx context.Context, name string) {
	_ = e.Dynamic.Resource(clusterModes).Delete(ctx, name, metav1.DeleteOptions{})
}

// Node creates a Node and sets its boot ID; an empty bootID leaves it unset.
func (e *Env) Node(ctx context.Context, bootID string) (string, error) {
	name := e.Name("node")
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := e.Kube.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	if bootID == "" {
		return name, nil
	}
	return name, e.SetBootID(ctx, name, bootID)
}

// SetBootID sets a Node's status.nodeInfo.bootID, as the kubelet does when
// the node boots.
func (e *Env) SetBootID(ctx context.Context, node, bootID string) error {
	patch := fmt.Sprintf(`{"status":{"nodeInfo":{"bootID":%q}}}`, bootID)
	_, err := e.Kube.CoreV1().Nodes().Patch(ctx, node, types.MergePatchType, []byte(patch),
		metav1.PatchOptions{}, "status")
	return err
}

// DeleteNode deletes a Node.
func (e *Env) DeleteNode(ctx context.Context, node string) error {
	return e.Kube.CoreV1().Nodes().Delete(ctx, node, metav1.DeleteOptions{})
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
