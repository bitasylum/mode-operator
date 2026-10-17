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
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	modev1alpha1 "github.com/bitasylum/mode-operator/api/v1alpha1"
)

// These specs run the status reconciler alongside the workload reconcilers.
// With no Deployment or StatefulSet controller, markReady plays the part of
// the pods becoming ready.

func statusOf(name string) func() modev1alpha1.ClusterModeStatus {
	return func() modev1alpha1.ClusterModeStatus {
		cm := &modev1alpha1.ClusterMode{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, cm)).To(Succeed())
		return cm.Status
	}
}

// conditionOf returns a function that reads a condition as "Status/Reason".
func conditionOf(name, conditionType string) func() string {
	return func() string {
		c := meta.FindStatusCondition(statusOf(name)().Conditions, conditionType)
		if c == nil {
			return ""
		}
		return string(c.Status) + "/" + c.Reason
	}
}

func countsOf(name string) func() modev1alpha1.WorkloadCounts {
	return func() modev1alpha1.WorkloadCounts { return statusOf(name)().Workloads }
}

// markReady sets a workload's status as its controller would once every pod
// is ready.
func markReady(obj client.Object) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		live := fresh(obj)
		switch o := live.(type) {
		case *appsv1.Deployment:
			n := *o.Spec.Replicas
			o.Status = appsv1.DeploymentStatus{
				ObservedGeneration: o.Generation, Replicas: n, ReadyReplicas: n,
				UpdatedReplicas: n, AvailableReplicas: n,
			}
		case *appsv1.StatefulSet:
			n := *o.Spec.Replicas
			o.Status = appsv1.StatefulSetStatus{
				ObservedGeneration: o.Generation, Replicas: n, ReadyReplicas: n,
				CurrentReplicas: n, UpdatedReplicas: n, AvailableReplicas: n,
			}
		}
		g.Expect(k8sClient.Status().Update(ctx, live)).To(Succeed())
	}).Should(Succeed())
}

// tryMode writes a ClusterMode's mode as the mode manager does, returning the
// API server's answer instead of failing the spec.
func tryMode(name, definition, mode string) error {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(modev1alpha1.SchemeGroupVersion.String())
	u.SetKind("ClusterMode")
	u.SetName(name)
	if err := unstructured.SetNestedField(u.Object, definition, "spec", "definition"); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(u.Object, mode, "spec", "mode"); err != nil {
		return err
	}
	return k8sClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(modeManager), client.ForceOwnership)
}

// waitForStatusModes waits until mode-operator has copied the definition's
// modes into the ClusterMode's status.
func waitForStatusModes(name string, modes ...modev1alpha1.ModeName) {
	GinkgoHelper()
	Eventually(func() []modev1alpha1.ModeName { return statusOf(name)().Modes }).Should(ConsistOf(modes))
}

// metricSeries returns the series of a metric for one ClusterMode, as
// "label=value,...: value".
func metricSeries(c prometheus.Collector, clusterMode string) map[string]float64 {
	GinkgoHelper()
	ch := make(chan prometheus.Metric, 64)
	go func() { c.Collect(ch); close(ch) }()
	out := map[string]float64{}
	for m := range ch {
		var d dto.Metric
		Expect(m.Write(&d)).To(Succeed())
		var key string
		match := false
		for _, l := range d.GetLabel() {
			if l.GetName() == "clustermode" {
				match = l.GetValue() == clusterMode
				continue
			}
			key += l.GetName() + "=" + l.GetValue()
		}
		if !match {
			continue
		}
		switch {
		case d.Gauge != nil:
			out[key] = d.Gauge.GetValue()
		case d.Counter != nil:
			out[key] = d.Counter.GetValue()
		}
	}
	return out
}

var _ = Describe("ClusterMode status", func() {
	var (
		ns   string
		def  string
		mode string
	)
	modes := []modev1alpha1.ModeName{normal, peak, "offpeak", standby}

	BeforeEach(func() {
		ns = newNamespace()
		def = uniqueName("standard")
		mode = uniqueName("cluster")
		createDefinition(def, modes...)
	})

	It("copies the definition's modes, after which the API server rejects other modes", func() {
		applyMode(mode, def, normal)
		waitForStatusModes(mode, modes...)
		Expect(conditionOf(mode, modev1alpha1.ConditionApplied)()).To(Equal("True/NoWorkloads"))

		cm := &modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: mode}}
		err := k8sClient.Patch(ctx, cm, client.RawPatch("application/merge-patch+json", []byte(`{"spec":{"mode":"holiday"}}`)),
			client.FieldOwner(maintainer))
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected the API server to reject the mode, got %v", err)
		Expect(err.Error()).To(ContainSubstring("spec.mode must be one of the modes in the ClusterModeDefinition"))
	})

	It("reports a mode set before status.modes existed as InvalidMode, and accepts a change to a valid one", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		applyMode(mode, def, "holiday")
		Eventually(conditionOf(mode, modev1alpha1.ConditionApplied)).Should(Equal("False/InvalidMode"))
		Expect(conditionOf(mode, modev1alpha1.ConditionDegraded)()).To(Equal("True/InvalidMode"))
		waitForStatusModes(mode, modes...)
		Consistently(replicasOf(dep)).Should(Equal(int32(1)))

		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		Eventually(conditionOf(mode, modev1alpha1.ConditionApplied)).Should(Equal("True/AllApplied"))
	})

	It("reports DefinitionNotFound, then counts the workloads once the definition exists", func() {
		later := uniqueName("later")
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		applyMode(mode, later, normal)
		Eventually(conditionOf(mode, modev1alpha1.ConditionReady)).Should(Equal("False/DefinitionNotFound"))
		Expect(conditionOf(mode, modev1alpha1.ConditionDegraded)()).To(Equal("True/DefinitionNotFound"))
		Expect(statusOf(mode)().Modes).To(BeEmpty())
		Expect(statusOf(mode)().Workloads).To(BeZero())

		createDefinition(later, normal, standby)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		Eventually(countsOf(mode)).Should(HaveField("Total", int32(1)))
		waitForStatusModes(mode, normal, standby)
	})

	It("shows workloads as Scaling until their pods are ready, then Settled", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,peak=6"})
		sts := newStatefulSet(ns, "db", followLabels(mode), map[string]string{AnnotationReplicas: "normal=2"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		Eventually(replicasOf(sts)).Should(Equal(int32(2)))
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 2, Scaling: 2}))
		Expect(conditionOf(mode, modev1alpha1.ConditionApplied)()).To(Equal("True/AllApplied"))
		Expect(conditionOf(mode, modev1alpha1.ConditionReady)()).To(Equal("False/Scaling"))
		Expect(statusOf(mode)().Unsettled).To(HaveLen(2))

		markReady(dep)
		markReady(sts)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 2, Settled: 2}))
		st := statusOf(mode)()
		Expect(st.Replicas).To(Equal(modev1alpha1.ReplicaCounts{Desired: 5, Applied: 5, Ready: 5}))
		Expect(st.Unsettled).To(BeEmpty())
		Expect(conditionOf(mode, modev1alpha1.ConditionReady)()).To(Equal("True/AllSettled"))
		Expect(conditionOf(mode, modev1alpha1.ConditionDegraded)()).To(Equal("False/AsExpected"))
		Expect(st.ObservedGeneration).To(BeNumerically(">", 0))

		By("leaving the status alone once nothing changes")
		resourceVersion := func() string {
			cm := &modev1alpha1.ClusterMode{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: mode}, cm)).To(Succeed())
			return cm.ResourceVersion
		}
		settledAt := resourceVersion()
		Consistently(resourceVersion, "5s").Should(Equal(settledAt))

		By("going back to Scaling after a mode change, with modeChangedAt moved on")
		changedAt := st.ModeChangedAt.Time
		applyMode(mode, def, peak)
		Eventually(func() string { return string(statusOf(mode)().Mode) }).Should(Equal(peak))
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 2, Scaling: 2}))
		Expect(statusOf(mode)().ModeChangedAt.Time).NotTo(BeTemporally("<", changedAt))
		markReady(dep)
		markReady(sts)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 2, Settled: 2}))
		Expect(statusOf(mode)().Replicas).To(Equal(modev1alpha1.ReplicaCounts{Desired: 6, Applied: 6, Ready: 6}))
	})

	It("reports a workload that misses its settle timeout as Stalled", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{
			AnnotationReplicas: "normal=3", AnnotationSettleTimeout: "2s",
		})
		applyMode(mode, def, normal)
		Eventually(countsOf(mode)).Should(HaveField("Stalled", int32(1)))
		Expect(conditionOf(mode, modev1alpha1.ConditionReady)()).To(Equal("False/Stalled"))
		Expect(conditionOf(mode, modev1alpha1.ConditionDegraded)()).To(Equal("True/StalledWorkloads"))
		u := statusOf(mode)().Unsettled
		Expect(u).To(HaveLen(1))
		Expect(u[0]).To(And(
			HaveField("Kind", "Deployment"), HaveField("Namespace", ns), HaveField("Name", "web"),
			HaveField("State", modev1alpha1.WorkloadStalled),
		))
		Expect(u[0].Message).To(ContainSubstring("settle timeout"))
		Expect(u[0].Since).NotTo(BeNil())

		markReady(dep)
		Eventually(conditionOf(mode, modev1alpha1.ConditionDegraded)).Should(Equal("False/AsExpected"))
	})

	It("counts an invalid replicas annotation as Invalid and sets Degraded", func() {
		newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=x"})
		applyMode(mode, def, normal)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 1, Invalid: 1}))
		st := statusOf(mode)()
		Expect(st.InvalidAnnotations).To(Equal(int32(1)))
		Expect(st.Unsettled).To(HaveLen(1))
		Expect(st.Unsettled[0].State).To(Equal(modev1alpha1.WorkloadInvalid))
		Expect(st.Unsettled[0].Desired).To(BeNil())
		Expect(st.Unsettled[0].Message).To(ContainSubstring(`"normal=x"`))
		Expect(conditionOf(mode, modev1alpha1.ConditionDegraded)()).To(Equal("True/InvalidWorkloads"))
	})

	It("reports an invalid delay at once, and counts it Invalid only while it blocks a scale to zero", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{
			AnnotationReplicas: "normal=3", AnnotationScaleToZeroDelay: "soon",
		})
		applyMode(mode, def, normal)
		Eventually(func() int32 { return statusOf(mode)().InvalidAnnotations }).Should(Equal(int32(1)))
		Expect(conditionOf(mode, modev1alpha1.ConditionDegraded)()).To(Equal("True/InvalidWorkloads"))
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		markReady(dep)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 1, Settled: 1}))

		applyMode(mode, def, standby)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 1, Invalid: 1}))
		Expect(statusOf(mode)().Unsettled[0].Message).To(ContainSubstring("holding 3 replicas until it is fixed"))
	})

	It("shows a pending scale to zero as Pending", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{
			AnnotationReplicas: "normal=3", AnnotationScaleToZeroDelay: "1m",
		})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		markReady(dep)
		Eventually(countsOf(mode)).Should(HaveField("Settled", int32(1)))

		applyMode(mode, def, standby)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 1, Pending: 1}))
		Expect(statusOf(mode)().Replicas).To(Equal(modev1alpha1.ReplicaCounts{Desired: 0, Applied: 3, Ready: 3}))
		Expect(conditionOf(mode, modev1alpha1.ConditionApplied)()).To(Equal("False/ScaleToZeroPending"))
		Expect(conditionOf(mode, modev1alpha1.ConditionDegraded)()).To(Equal("False/AsExpected"))
	})

	It("records which field manager set the mode", func() {
		applyMode(mode, def, normal)
		Eventually(func() string { return statusOf(mode)().ModeSetBy }).Should(Equal(modeManager))

		mergePatch(&modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: mode}}, maintainer, `{"spec":{"mode":"peak"}}`)
		Eventually(func() string { return statusOf(mode)().ModeSetBy }).Should(Equal(maintainer))

		applyMode(mode, def, normal)
		Eventually(func() string { return statusOf(mode)().ModeSetBy }).Should(Equal(modeManager))
	})

	It("keeps each ClusterMode's counts right as a workload moves between them", func() {
		other := uniqueName("other")
		applyMode(mode, def, normal)
		applyMode(other, def, peak)
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,peak=6"})
		Eventually(countsOf(mode)).Should(HaveField("Total", int32(1)))
		Eventually(conditionOf(other, modev1alpha1.ConditionApplied)).Should(Equal("True/NoWorkloads"))

		mergePatch(dep, "kubectl-label", fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, LabelClusterMode, other))
		Eventually(countsOf(other)).Should(HaveField("Total", int32(1)))
		Eventually(countsOf(mode)).Should(HaveField("Total", int32(0)))
	})

	It("drops a workload whose label is removed from the counts", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		applyMode(mode, def, normal)
		Eventually(countsOf(mode)).Should(HaveField("Total", int32(1)))

		mergePatch(dep, "kubectl-label", fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, LabelClusterMode))
		Eventually(countsOf(mode)).Should(HaveField("Total", int32(0)))
		Expect(replicasOf(dep)()).To(Equal(int32(3)))
	})

	It("marks a workload Invalid when the definition drops a mode its annotation names", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,offpeak=1"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		markReady(dep)
		Eventually(countsOf(mode)).Should(HaveField("Settled", int32(1)))

		setDefinitionModes(def, normal, peak, standby)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 1, Invalid: 1}))
		Expect(statusOf(mode)().Unsettled[0].Message).To(ContainSubstring("its modes are normal, peak, standby"))
		waitForStatusModes(mode, normal, peak, standby)
		Expect(replicasOf(dep)()).To(Equal(int32(3)))
	})

	It("leaves the mode manager owning spec and mode-operator owning status", func() {
		applyMode(mode, def, normal)
		waitForStatusModes(mode, modes...)

		cm := &modev1alpha1.ClusterMode{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: mode}, cm)).To(Succeed())
		owners := map[string][]string{}
		for _, mf := range cm.ManagedFields {
			var fields map[string]map[string]any
			Expect(json.Unmarshal(mf.FieldsV1.GetRawBytes(), &fields)).To(Succeed())
			for _, f := range []string{"f:mode", "f:definition"} {
				if _, ok := fields["f:spec"][f]; ok {
					owners["spec."+f[2:]] = append(owners["spec."+f[2:]], mf.Manager)
				}
			}
			if _, ok := fields["f:status"]; ok {
				owners["status"] = append(owners["status"], mf.Manager+"/"+mf.Subresource)
			}
		}
		Expect(owners).To(Equal(map[string][]string{
			"spec.mode":       {modeManager},
			"spec.definition": {modeManager},
			"status":          {FieldManager + "/status"},
		}))
	})

	It("keeps the metrics in step with the status, and deletes them with the ClusterMode", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,peak=6"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		markReady(dep)
		Eventually(func() map[string]float64 { return metricSeries(workloadsMetric, mode) }).Should(HaveKeyWithValue("state=settled", 1.0))
		Expect(metricSeries(currentModeMetric, mode)).To(Equal(map[string]float64{"mode=normal": 1}))
		Expect(metricSeries(replicasMetric, mode)).To(Equal(map[string]float64{"type=desired": 3, "type=applied": 3, "type=ready": 3}))
		Expect(metricSeries(invalidAnnotationsMetric, mode)).To(Equal(map[string]float64{"": 0}))
		Expect(metricSeries(modeChangesMetric, mode)).To(Equal(map[string]float64{"": 0}))
		Expect(metricSeries(conditionMetric, mode)).To(Equal(map[string]float64{
			"type=Applied": 1, "type=Ready": 1, "type=Degraded": 0,
		}))

		applyMode(mode, def, peak)
		Eventually(func() map[string]float64 { return metricSeries(currentModeMetric, mode) }).Should(Equal(map[string]float64{"mode=peak": 1}))
		Expect(metricSeries(modeChangesMetric, mode)).To(Equal(map[string]float64{"": 1}))

		Expect(k8sClient.Delete(ctx, &modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: mode}})).To(Succeed())
		for _, c := range []prometheus.Collector{
			currentModeMetric, modeChangesMetric, workloadsMetric, replicasMetric,
			invalidAnnotationsMetric, conditionMetric,
		} {
			Eventually(func() map[string]float64 { return metricSeries(c, mode) }).Should(BeEmpty())
		}
	})
})

var _ = Describe("ClusterMode transitions", func() {
	It("follows the definition's state machine, leaving a mode only once it is Ready", func() {
		ns := newNamespace()
		def := uniqueName("machine")
		mode := uniqueName("cluster")
		// normal and peak may move to each other, and standby, the default,
		// only to normal.
		created := &modev1alpha1.ClusterModeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: def},
			Spec: modev1alpha1.ClusterModeDefinitionSpec{
				Modes:       []modev1alpha1.ModeName{normal, peak, standby},
				DefaultMode: standby,
				Transitions: []modev1alpha1.ModeTransition{
					{From: normal, To: []modev1alpha1.ModeName{peak}},
					{From: peak, To: []modev1alpha1.ModeName{normal}},
					{From: standby, To: []modev1alpha1.ModeName{normal}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, created)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, created))).To(Succeed()) })

		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,peak=6"})
		applyMode(mode, def, normal)
		Eventually(func() []modev1alpha1.ModeTransition { return statusOf(mode)().Transitions }).
			Should(Equal(created.Spec.Transitions))
		Expect(statusOf(mode)().DefaultMode).To(Equal(modev1alpha1.ModeName(standby)))

		By("rejecting peak while normal is still scaling")
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		Eventually(conditionOf(mode, modev1alpha1.ConditionReady)).Should(Equal("False/Scaling"))
		err := tryMode(mode, def, peak)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("can't change until the current mode is Ready"))

		By("accepting peak once normal is Ready")
		markReady(dep)
		Eventually(conditionOf(mode, modev1alpha1.ConditionReady)).Should(Equal("True/AllSettled"))
		Expect(tryMode(mode, def, peak)).To(Succeed())
		Eventually(replicasOf(dep)).Should(Equal(int32(6)))

		By("accepting the default mode even though peak isn't Ready")
		Eventually(conditionOf(mode, modev1alpha1.ConditionReady)).Should(Equal("False/Scaling"))
		Expect(tryMode(mode, def, standby)).To(Succeed())
		Eventually(replicasOf(dep)).Should(Equal(int32(0)))

		By("rejecting peak from standby, whose rule lists only normal, even once it is Ready")
		markReady(dep)
		Eventually(conditionOf(mode, modev1alpha1.ConditionReady)).Should(Equal("True/AllSettled"))
		err = tryMode(mode, def, peak)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("transitions don't allow this change"))

		By("lifting the restrictions when the definition drops its transitions")
		Eventually(func(g Gomega) {
			live := &modev1alpha1.ClusterModeDefinition{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: def}, live)).To(Succeed())
			live.Spec.Transitions = nil
			g.Expect(k8sClient.Update(ctx, live)).To(Succeed())
		}).Should(Succeed())
		Eventually(func() []modev1alpha1.ModeTransition { return statusOf(mode)().Transitions }).Should(BeEmpty())
		Expect(tryMode(mode, def, peak)).To(Succeed())
	})
})

var _ = Describe("Pods shutting down", func() {
	It("keeps a scaled-down Deployment Scaling until its pods have gone", func() {
		ns := newNamespace()
		def := uniqueName("standard")
		mode := uniqueName("cluster")
		createDefinition(def, normal, standby)
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		markReady(dep)
		Eventually(countsOf(mode)).Should(HaveField("Settled", int32(1)))

		applyMode(mode, def, standby)
		Eventually(replicasOf(dep)).Should(Equal(int32(0)))
		setStopping := func(n int32) {
			GinkgoHelper()
			Eventually(func(g Gomega) {
				live := fresh(dep).(*appsv1.Deployment)
				live.Status = appsv1.DeploymentStatus{ObservedGeneration: live.Generation, TerminatingReplicas: new(n)}
				g.Expect(k8sClient.Status().Update(ctx, live)).To(Succeed())
			}).Should(Succeed())
		}
		setStopping(2)
		Eventually(func() string {
			if u := statusOf(mode)().Unsettled; len(u) == 1 {
				return u[0].Message
			}
			return ""
		}).Should(Equal("2 pods still to stop"))
		Expect(conditionOf(mode, modev1alpha1.ConditionReady)()).To(Equal("False/Scaling"))

		setStopping(0)
		Eventually(countsOf(mode)).Should(Equal(modev1alpha1.WorkloadCounts{Total: 1, Settled: 1}))
	})
})
