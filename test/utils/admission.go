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

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	modev1alpha1 "github.com/Audiobuzz/mode-operator/api/v1alpha1"
)

// InstallAdmissionPolicies creates the objects in dir, normally
// config/admission, and waits until the API server enforces the transitions
// policy. The API server loads policies in the background, so a test that ran
// straight away could pass without them.
func InstallAdmissionPolicies(ctx context.Context, c client.Client, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if filepath.Base(f) == "kustomization.yaml" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(data, &obj.Object); err != nil {
			return fmt.Errorf("reading %s: %w", f, err)
		}
		if err := c.Create(ctx, obj); err != nil {
			return fmt.Errorf("creating %s: %w", f, err)
		}
	}
	return waitForTransitionsPolicy(ctx, c)
}

// transitionsPolicyMessage is part of the transitions policy's message, so the
// probe isn't satisfied by a denial for another reason, such as an expression
// that fails to evaluate.
const transitionsPolicyMessage = "as mode-operator reports for the latest spec"

// waitForTransitionsPolicy waits until the API server rejects a change of mode
// that only the transitions policy forbids: one whose Ready status was
// computed from another generation. Run it before any controller writes
// ClusterMode status.
func waitForTransitionsPolicy(ctx context.Context, c client.Client) error {
	const name = "admission-policy-probe"
	cm := &modev1alpha1.ClusterMode{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       modev1alpha1.ClusterModeSpec{Definition: name, Mode: "a"},
	}
	if err := c.Create(ctx, cm); err != nil {
		return err
	}
	defer func() { _ = c.Delete(ctx, cm) }()

	// The ClusterMode is at generation 1.
	status, err := json.Marshal(map[string]any{"status": map[string]any{
		"observedGeneration": 2,
		"modes":              []string{"a", "b"},
		"transitions":        []map[string]any{{"from": "a", "to": []string{"b"}}},
		"mode":               "a",
		"conditions": []map[string]any{{
			"type": "Ready", "status": "True", "reason": "Probe", "message": "probe",
			"lastTransitionTime": "2026-01-01T00:00:00Z",
		}},
	}})
	if err != nil {
		return err
	}
	if err := c.Status().Patch(ctx, cm, client.RawPatch(types.MergePatchType, status)); err != nil {
		return err
	}

	change := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"mode":"b"}}`))
	return wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true,
		func(ctx context.Context) (bool, error) {
			err := c.Patch(ctx, cm.DeepCopy(), change, client.DryRunAll)
			switch {
			case err == nil:
				return false, nil
			case apierrors.IsInvalid(err) && strings.Contains(err.Error(), transitionsPolicyMessage):
				return true, nil
			default:
				return false, fmt.Errorf("the probe's change of mode was rejected for another reason: %w", err)
			}
		})
}
