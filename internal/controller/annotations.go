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

package controller

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/bitasylum/mode-operator/internal/modespec"
)

// errMissingReplicas is the replicas error of a labelled workload that has no
// replicas annotation.
var errMissingReplicas = fmt.Errorf("the workload has the %s label but no %s annotation", LabelClusterMode, AnnotationReplicas)

// userAnnotations is a workload's user annotations, parsed. Every annotation
// is checked, whatever the current mode, so a mistake shows up as soon as it
// is made.
type userAnnotations struct {
	replicas    modespec.Spec
	replicasErr error

	delay    time.Duration
	delayErr error

	// settleTimeout is 0 when the annotation is absent or invalid, meaning
	// the controller's default applies.
	settleTimeout    time.Duration
	settleTimeoutErr error
}

// parseUserAnnotations parses a workload's annotations against the modes of
// its ClusterMode's definition. maxCount is the largest count the kind allows.
func parseUserAnnotations(ann map[string]string, modes sets.Set[string], maxCount int32) userAnnotations {
	var ua userAnnotations
	if raw, ok := ann[AnnotationReplicas]; !ok {
		ua.replicasErr = errMissingReplicas
	} else if ua.replicas, ua.replicasErr = modespec.Parse(raw, modes, maxCount); ua.replicasErr != nil {
		if errors.Is(ua.replicasErr, modespec.ErrUnknownMode) {
			ua.replicasErr = fmt.Errorf("%w; its modes are %s", ua.replicasErr, strings.Join(sets.List(modes), ", "))
		}
		ua.replicasErr = fmt.Errorf("%s: %w", AnnotationReplicas, ua.replicasErr)
	}
	if ua.delay, ua.delayErr = modespec.ParseDelay(ann[AnnotationScaleToZeroDelay]); ua.delayErr != nil {
		ua.delayErr = fmt.Errorf("%s: %w", AnnotationScaleToZeroDelay, ua.delayErr)
	}
	if ua.settleTimeout, ua.settleTimeoutErr = modespec.ParseSettleTimeout(ann[AnnotationSettleTimeout]); ua.settleTimeoutErr != nil {
		ua.settleTimeoutErr = fmt.Errorf("%s: %w", AnnotationSettleTimeout, ua.settleTimeoutErr)
	}
	return ua
}
