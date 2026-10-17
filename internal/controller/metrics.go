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
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	modev1alpha1 "github.com/Audiobuzz/mode-operator/api/v1alpha1"
)

// labelClusterMode is the metric label that names the ClusterMode.
const labelClusterMode = "clustermode"

// The status reconciler sets every metric from the status it writes, so the
// two always agree. Reconcile errors come from controller-runtime's own
// controller_runtime_reconcile_errors_total.
var (
	currentModeMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mode_operator_current_mode",
		Help: "1 for the current mode of each ClusterMode.",
	}, []string{labelClusterMode, "mode"})

	modeChangesMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mode_operator_mode_changes_total",
		Help: "Changes of mode seen in each ClusterMode.",
	}, []string{labelClusterMode})

	workloadsMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mode_operator_workloads",
		Help: "Workloads that follow each ClusterMode, by state.",
	}, []string{labelClusterMode, "state"})

	replicasMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mode_operator_replicas",
		Help: "Replicas summed over the workloads that follow each ClusterMode and aren't Invalid: " +
			"desired by the mode, applied by mode-operator, and ready.",
	}, []string{labelClusterMode, "type"})

	invalidAnnotationsMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mode_operator_invalid_annotations",
		Help: "Workloads with any annotation error, for each ClusterMode.",
	}, []string{labelClusterMode})

	conditionMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mode_operator_condition",
		Help: "1 when the ClusterMode condition is True, 0 when it is False.",
	}, []string{labelClusterMode, "type"})
)

// lastMode is the mode each ClusterMode's current-mode series was last set
// for, so the old series can be deleted after the new one is set.
var (
	lastModeMu sync.Mutex
	lastMode   = map[string]string{}
)

func init() {
	metrics.Registry.MustRegister(currentModeMetric, modeChangesMetric, workloadsMetric, replicasMetric,
		invalidAnnotationsMetric, conditionMetric)
}

// recordStatus sets the gauges for one ClusterMode from its status.
func recordStatus(name string, st *modev1alpha1.ClusterModeStatus) {
	// Set the new series before deleting the old one, so a scrape never sees
	// the ClusterMode without a current mode.
	mode := string(st.Mode)
	currentModeMetric.WithLabelValues(name, mode).Set(1)
	lastModeMu.Lock()
	if old, ok := lastMode[name]; ok && old != mode {
		currentModeMetric.DeleteLabelValues(name, old)
	}
	lastMode[name] = mode
	lastModeMu.Unlock()

	// Create the counter at 0, so rate() sees the first change.
	modeChangesMetric.WithLabelValues(name)

	for state, n := range map[string]int32{
		"settled":  st.Workloads.Settled,
		"scaling":  st.Workloads.Scaling,
		"stalled":  st.Workloads.Stalled,
		"applying": st.Workloads.Applying,
		"pending":  st.Workloads.Pending,
		"invalid":  st.Workloads.Invalid,
	} {
		workloadsMetric.WithLabelValues(name, state).Set(float64(n))
	}
	replicasMetric.WithLabelValues(name, "desired").Set(float64(st.Replicas.Desired))
	replicasMetric.WithLabelValues(name, "applied").Set(float64(st.Replicas.Applied))
	replicasMetric.WithLabelValues(name, "ready").Set(float64(st.Replicas.Ready))
	invalidAnnotationsMetric.WithLabelValues(name).Set(float64(st.InvalidAnnotations))
	for _, c := range st.Conditions {
		v := 0.0
		if c.Status == metav1.ConditionTrue {
			v = 1
		}
		conditionMetric.WithLabelValues(name, c.Type).Set(v)
	}
}

// countModeChange counts one change of mode.
func countModeChange(name string) {
	modeChangesMetric.WithLabelValues(name).Inc()
}

// forgetMetrics deletes every series for a deleted ClusterMode.
func forgetMetrics(name string) {
	labels := prometheus.Labels{labelClusterMode: name}
	currentModeMetric.DeletePartialMatch(labels)
	modeChangesMetric.DeletePartialMatch(labels)
	workloadsMetric.DeletePartialMatch(labels)
	replicasMetric.DeletePartialMatch(labels)
	invalidAnnotationsMetric.DeletePartialMatch(labels)
	conditionMetric.DeletePartialMatch(labels)
	lastModeMu.Lock()
	delete(lastMode, name)
	lastModeMu.Unlock()
}
