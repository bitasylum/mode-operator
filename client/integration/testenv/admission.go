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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

var (
	policyResource = schema.GroupVersionResource{
		Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies",
	}
	bindingResource = schema.GroupVersionResource{
		Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings",
	}
	clusterModes = schema.GroupVersionResource{
		Group: "mode.bitasylum.org", Version: "v1alpha1", Resource: "clustermodes",
	}
)

// installAdmission creates the objects in config/admission and waits until
// the API server enforces the transitions policy, which it loads in the
// background.
func (e *Env) installAdmission(ctx context.Context) error {
	files, err := filepath.Glob(e.Path("config", "admission", "*.yaml"))
	if err != nil {
		return err
	}
	var message string
	for _, f := range files {
		if filepath.Base(f) == "kustomization.yaml" {
			continue
		}
		obj, err := readObject(f)
		if err != nil {
			return err
		}
		res := bindingResource
		if obj.GetKind() == "ValidatingAdmissionPolicy" {
			res = policyResource
			validations, _, _ := unstructured.NestedSlice(obj.Object, "spec", "validations")
			if len(validations) > 0 {
				message, _, _ = unstructured.NestedString(validations[0].(map[string]any), "message")
			}
		}
		if _, err := e.Dynamic.Resource(res).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating %s: %w", f, err)
		}
	}
	if message == "" {
		return fmt.Errorf("no validation message in config/admission")
	}
	return e.waitForPolicy(ctx, message)
}

// waitForPolicy waits until a change of mode that only the policy forbids is
// rejected with its message: one whose Ready status describes another
// generation.
func (e *Env) waitForPolicy(ctx context.Context, message string) error {
	const name = "admission-policy-probe"
	res := e.Dynamic.Resource(clusterModes)
	cm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "mode.bitasylum.org/v1alpha1",
		"kind":       "ClusterMode",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"definition": name, "mode": "a"},
	}}
	if _, err := res.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return err
	}
	defer func() { _ = res.Delete(ctx, name, metav1.DeleteOptions{}) }()

	// The ClusterMode is at generation 1.
	status := `{"status":{"observedGeneration":2,"modes":["a","b"],"defaultMode":"a",` +
		`"transitions":[{"from":"a","to":["b"]}],"mode":"a","conditions":[{"type":"Ready","status":"True",` +
		`"reason":"Probe","message":"probe","lastTransitionTime":"2026-01-01T00:00:00Z"}]}}`
	if _, err := res.Patch(ctx, name, types.MergePatchType, []byte(status), metav1.PatchOptions{}, "status"); err != nil {
		return err
	}
	// The message is folded YAML; compare its words.
	want := strings.Join(strings.Fields(message), " ")
	return wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true,
		func(ctx context.Context) (bool, error) {
			_, err := res.Patch(ctx, name, types.MergePatchType, []byte(`{"spec":{"mode":"b"}}`),
				metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
			switch {
			case err == nil:
				return false, nil
			case apierrors.IsInvalid(err) && strings.Contains(err.Error(), want):
				return true, nil
			default:
				return false, fmt.Errorf("the probe's change of mode was rejected for another reason: %w", err)
			}
		})
}

func readObject(path string) (*unstructured.Unstructured, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(data, &obj.Object); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return obj, nil
}
