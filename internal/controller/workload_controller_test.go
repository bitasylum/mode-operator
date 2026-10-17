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
	"slices"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	modev1alpha1 "github.com/Audiobuzz/mode-operator/api/v1alpha1"
)

// These specs run the workload reconcilers against a real API server. There
// is no Deployment or StatefulSet controller, so no pods are ever created;
// the specs only check what mode-operator writes.

const (
	modeManager = "mode-manager"
	maintainer  = "kubectl-patch"
	normal      = "normal"
	peak        = "peak"
	standby     = "standby"
)

var nameSeq atomic.Int32

// uniqueName returns a name no other spec uses, for cluster-scoped objects.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, nameSeq.Add(1))
}

func newNamespace() string {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: uniqueName("workloads")}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return ns.Name
}

func createDefinition(name string, modes ...modev1alpha1.ModeName) {
	GinkgoHelper()
	def := &modev1alpha1.ClusterModeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       modev1alpha1.ClusterModeDefinitionSpec{Modes: modes, DefaultMode: modes[len(modes)-1]},
	}
	Expect(k8sClient.Create(ctx, def)).To(Succeed())
	DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, def))).To(Succeed()) })
}

// setDefinitionModes replaces a definition's modes, as an admin would.
func setDefinitionModes(name string, modes ...modev1alpha1.ModeName) {
	GinkgoHelper()
	def := &modev1alpha1.ClusterModeDefinition{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, def)).To(Succeed())
	def.Spec.Modes = modes
	Expect(k8sClient.Update(ctx, def)).To(Succeed())
	// Wait until the reconcilers can see the change.
	Eventually(func() []modev1alpha1.ModeName {
		seen := &modev1alpha1.ClusterModeDefinition{}
		Expect(cachedClient.Get(ctx, client.ObjectKey{Name: name}, seen)).To(Succeed())
		return seen.Spec.Modes
	}).Should(Equal(modes))
}

// applyMode writes a ClusterMode as the mode manager does: server-side apply
// with field manager mode-manager and Force.
func applyMode(name, definition, mode string) {
	GinkgoHelper()
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(modev1alpha1.SchemeGroupVersion.String())
	u.SetKind("ClusterMode")
	u.SetName(name)
	Expect(unstructured.SetNestedField(u.Object, definition, "spec", "definition")).To(Succeed())
	Expect(unstructured.SetNestedField(u.Object, mode, "spec", "mode")).To(Succeed())
	Expect(k8sClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(modeManager), client.ForceOwnership)).To(Succeed())
	DeferCleanup(func() {
		cm := &modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cm))).To(Succeed())
	})
}

// mergePatch applies a JSON merge patch to obj as fieldOwner.
func mergePatch(obj client.Object, fieldOwner, patch string) {
	GinkgoHelper()
	Expect(k8sClient.Patch(ctx, obj, client.RawPatch(types.MergePatchType, []byte(patch)),
		client.FieldOwner(fieldOwner))).To(Succeed())
}

func followLabels(clusterMode string) map[string]string {
	return map[string]string{LabelClusterMode: clusterMode}
}

// newDeployment creates a Deployment with 1 replica.
func newDeployment(ns, name string, labels, annotations map[string]string) *appsv1.Deployment {
	GinkgoHelper()
	sel := map[string]string{"app": name}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, Annotations: annotations},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sel},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.invalid/app"}}},
			},
		},
	}
	Expect(k8sClient.Create(ctx, d)).To(Succeed())
	return d
}

// newStatefulSet creates a StatefulSet with 1 replica.
func newStatefulSet(ns, name string, labels, annotations map[string]string) *appsv1.StatefulSet {
	GinkgoHelper()
	sel := map[string]string{"app": name}
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, Annotations: annotations},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    ptr.To[int32](1),
			ServiceName: name,
			Selector:    &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sel},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.invalid/app"}}},
			},
		},
	}
	Expect(k8sClient.Create(ctx, s)).To(Succeed())
	return s
}

// fresh reads obj again from the API server.
func fresh(obj client.Object) client.Object {
	GinkgoHelper()
	out := obj.DeepCopyObject().(client.Object)
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), out)).To(Succeed())
	return out
}

// replicasOf returns a function that reads obj's spec.replicas.
func replicasOf(obj client.Object) func() int32 {
	return func() int32 {
		switch o := fresh(obj).(type) {
		case *appsv1.Deployment:
			return *o.Spec.Replicas
		case *appsv1.StatefulSet:
			return *o.Spec.Replicas
		}
		Fail(fmt.Sprintf("unexpected kind %T", obj))
		return -1
	}
}

// annotationOf returns a function that reads one of obj's annotations.
func annotationOf(obj client.Object, key string) func() string {
	return func() string { return fresh(obj).GetAnnotations()[key] }
}

// eventReasons returns a function that lists the reasons of the events about
// obj.
func eventReasons(obj client.Object, kind string) func() []string {
	return func() []string {
		var list eventsv1.EventList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(obj.GetNamespace()))).To(Succeed())
		var reasons []string
		for _, e := range list.Items {
			if e.Regarding.Kind == kind && e.Regarding.Name == obj.GetName() {
				reasons = append(reasons, e.Reason)
			}
		}
		return reasons
	}
}

// replicasOwners returns the field managers that own obj's spec.replicas.
func replicasOwners(obj client.Object) []string {
	GinkgoHelper()
	var owners []string
	for _, mf := range fresh(obj).GetManagedFields() {
		if mf.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		Expect(json.Unmarshal(mf.FieldsV1.GetRawBytes(), &fields)).To(Succeed())
		if spec, ok := fields["f:spec"].(map[string]any); ok {
			if _, ok := spec["f:replicas"]; ok {
				owners = append(owners, mf.Manager+"/"+string(mf.Operation))
			}
		}
	}
	slices.Sort(owners)
	return owners
}

var _ = Describe("Workload reconciler", func() {
	var (
		ns   string
		def  string
		mode string
	)

	BeforeEach(func() {
		ns = newNamespace()
		def = uniqueName("standard")
		mode = uniqueName("cluster")
		createDefinition(def, normal, peak, "offpeak", standby)
	})

	It("scales labelled Deployments and StatefulSets, and leaves unlabelled ones alone", func() {
		replicas := map[string]string{AnnotationReplicas: "normal=3,peak=6"}
		dep := newDeployment(ns, "web", followLabels(mode), replicas)
		sts := newStatefulSet(ns, "db", followLabels(mode), map[string]string{AnnotationReplicas: "normal=2,peak=4"})
		unlabelled := newDeployment(ns, "other", nil, replicas)

		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		Eventually(replicasOf(sts)).Should(Equal(int32(2)))
		Expect(annotationOf(dep, AnnotationAppliedMode)()).To(Equal(normal))
		Expect(annotationOf(dep, AnnotationReplicasChangedAt)()).NotTo(BeEmpty())

		applyMode(mode, def, peak)
		Eventually(replicasOf(dep)).Should(Equal(int32(6)))
		Eventually(replicasOf(sts)).Should(Equal(int32(4)))
		Eventually(annotationOf(sts, AnnotationAppliedMode)).Should(Equal(peak))
		Eventually(eventReasons(dep, "Deployment")).Should(ContainElement(ReasonModeApplied))

		Consistently(replicasOf(unlabelled)).Should(Equal(int32(1)))
		Expect(fresh(unlabelled).GetAnnotations()).NotTo(HaveKey(AnnotationAppliedMode))
	})

	It("gives mode-operator sole ownership of spec.replicas", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		sts := newStatefulSet(ns, "db", followLabels(mode), map[string]string{AnnotationReplicas: "normal=2"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		Eventually(replicasOf(sts)).Should(Equal(int32(2)))
		Expect(replicasOwners(dep)).To(Equal([]string{FieldManager + "/Apply"}))
		Expect(replicasOwners(sts)).To(Equal([]string{FieldManager + "/Apply"}))
	})

	It("reverts a manual change to spec.replicas", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))

		mergePatch(dep, "kubectl-scale", `{"spec":{"replicas":7}}`)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
	})

	It("follows the ClusterMode its label names, and moves when the label changes", func() {
		other := uniqueName("other")
		applyMode(mode, def, normal)
		applyMode(other, def, peak)
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,peak=6"})
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))

		mergePatch(dep, "kubectl-label", fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, LabelClusterMode, other))
		Eventually(replicasOf(dep)).Should(Equal(int32(6)))
	})

	It("follows a maintainer's edit, then the mode manager taking the mode back", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,peak=6"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))

		cm := &modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: mode}}
		mergePatch(cm, maintainer, `{"spec":{"mode":"peak"}}`)
		Eventually(replicasOf(dep)).Should(Equal(int32(6)))

		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
	})

	It("leaves a workload alone while its ClusterMode is missing, and applies the mode once it exists", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		Eventually(eventReasons(dep, "Deployment")).Should(ContainElement(ReasonClusterModeNotFound))
		Consistently(replicasOf(dep)).Should(Equal(int32(1)))

		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
	})

	It("changes nothing while the ClusterModeDefinition is missing, and applies the mode once it's created", func() {
		missing := uniqueName("later")
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		applyMode(mode, missing, normal)
		Consistently(replicasOf(dep)).Should(Equal(int32(1)))

		createDefinition(missing, normal, standby)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
	})

	It("checks annotations again when the definition's modes change", func() {
		narrow := uniqueName("narrow")
		createDefinition(narrow, normal, standby)
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,offpeak=1"})
		applyMode(mode, narrow, normal)
		By("rejecting an annotation that names a mode the definition doesn't have")
		Eventually(eventReasons(dep, "Deployment")).Should(ContainElement(ReasonInvalidAnnotation))
		Consistently(replicasOf(dep)).Should(Equal(int32(1)))

		By("applying the mode once the definition has every mode the annotation names")
		setDefinitionModes(narrow, normal, "offpeak", standby)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		applyMode(mode, narrow, "offpeak")
		Eventually(replicasOf(dep)).Should(Equal(int32(1)))

		By("leaving replicas alone after the definition drops a mode the annotation still names")
		setDefinitionModes(narrow, normal, standby)
		applyMode(mode, narrow, normal)
		Consistently(replicasOf(dep)).Should(Equal(int32(1)))
	})

	It("holds a scale to zero for the delay, then scales to zero", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{
			AnnotationReplicas: "normal=3", AnnotationScaleToZeroDelay: "2s",
		})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))

		applyMode(mode, def, standby)
		Eventually(annotationOf(dep, AnnotationZeroPendingSince)).ShouldNot(BeEmpty())
		Expect(replicasOf(dep)()).To(Equal(int32(3)))
		Expect(annotationOf(dep, AnnotationAppliedMode)()).To(Equal(normal))
		Eventually(eventReasons(dep, "Deployment")).Should(ContainElement(ReasonScaleToZeroPending))

		Eventually(replicasOf(dep)).Should(Equal(int32(0)))
		Expect(annotationOf(dep, AnnotationZeroPendingSince)()).To(BeEmpty())
		Expect(annotationOf(dep, AnnotationAppliedMode)()).To(Equal(standby))
	})

	It("cancels a pending scale to zero when the mode changes back", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{
			AnnotationReplicas: "normal=3", AnnotationScaleToZeroDelay: "1m",
		})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))

		applyMode(mode, def, standby)
		Eventually(annotationOf(dep, AnnotationZeroPendingSince)).ShouldNot(BeEmpty())

		applyMode(mode, def, normal)
		Eventually(annotationOf(dep, AnnotationZeroPendingSince)).Should(BeEmpty())
		Expect(replicasOf(dep)()).To(Equal(int32(3)))
		Eventually(eventReasons(dep, "Deployment")).Should(ContainElement(ReasonScaleToZeroCancelled))
	})

	It("leaves replicas alone when the replicas annotation is invalid or missing", func() {
		invalid := newDeployment(ns, "invalid", followLabels(mode), map[string]string{AnnotationReplicas: "normal=x"})
		missing := newStatefulSet(ns, "missing", followLabels(mode), nil)
		applyMode(mode, def, normal)

		Eventually(eventReasons(invalid, "Deployment")).Should(ContainElement(ReasonInvalidAnnotation))
		Eventually(eventReasons(missing, "StatefulSet")).Should(ContainElement(ReasonMissingAnnotation))
		Consistently(replicasOf(invalid)).Should(Equal(int32(1)))
		Consistently(replicasOf(missing)).Should(Equal(int32(1)))
	})

	It("with an invalid delay, still scales between modes above zero but holds a scale to zero", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{
			AnnotationReplicas: "normal=3,peak=6", AnnotationScaleToZeroDelay: "soon",
		})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))
		Eventually(eventReasons(dep, "Deployment")).Should(ContainElement(ReasonInvalidAnnotation))

		applyMode(mode, def, peak)
		Eventually(replicasOf(dep)).Should(Equal(int32(6)))

		By("holding at the current replicas, with no countdown, in a mode that wants zero")
		applyMode(mode, def, standby)
		Consistently(replicasOf(dep)).Should(Equal(int32(6)))
		Expect(annotationOf(dep, AnnotationZeroPendingSince)()).To(BeEmpty())

		By("running the delay once the annotation is fixed")
		mergePatch(dep, "kubectl-annotate", fmt.Sprintf(`{"metadata":{"annotations":{%q:"1s"}}}`, AnnotationScaleToZeroDelay))
		Eventually(replicasOf(dep)).Should(Equal(int32(0)))
	})

	It("leaves workloads alone when their ClusterMode is deleted", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))

		Expect(k8sClient.Delete(ctx, &modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: mode}})).To(Succeed())
		Consistently(replicasOf(dep)).Should(Equal(int32(3)))

		By("warning, and still changing nothing, when the workload is next edited")
		mergePatch(dep, "kubectl-annotate", fmt.Sprintf(`{"metadata":{"annotations":{%q:"normal=5"}}}`, AnnotationReplicas))
		Eventually(eventReasons(dep, "Deployment")).Should(ContainElement(ReasonClusterModeNotFound))
		Consistently(replicasOf(dep)).Should(Equal(int32(3)))
	})

	It("stops managing a workload whose label is removed", func() {
		dep := newDeployment(ns, "web", followLabels(mode), map[string]string{AnnotationReplicas: "normal=3,peak=6"})
		applyMode(mode, def, normal)
		Eventually(replicasOf(dep)).Should(Equal(int32(3)))

		mergePatch(dep, "kubectl-label", fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, LabelClusterMode))
		applyMode(mode, def, peak)
		Consistently(replicasOf(dep)).Should(Equal(int32(3)))
	})
})
