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

package workload

import (
	"math"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StatefulSet is the apps/v1 StatefulSet kind. Its count is spec.replicas.
type StatefulSet struct{}

var _ Kind = StatefulSet{}

func (StatefulSet) Name() string               { return "StatefulSet" }
func (StatefulSet) NewObject() client.Object   { return &appsv1.StatefulSet{} }
func (StatefulSet) NewList() client.ObjectList { return &appsv1.StatefulSetList{} }
func (StatefulSet) MaxCount() int32            { return math.MaxInt32 }
func (StatefulSet) Current(obj client.Object) int32 {
	return replicasOrDefault(obj.(*appsv1.StatefulSet).Spec.Replicas)
}

func (StatefulSet) Holds(obj client.Object, target int32) bool {
	r := obj.(*appsv1.StatefulSet).Spec.Replicas
	return r != nil && *r == target
}

func (StatefulSet) ApplyConfiguration(obj client.Object, target int32, annotations map[string]string) runtime.ApplyConfiguration {
	return appsv1ac.StatefulSet(obj.GetName(), obj.GetNamespace()).
		WithAnnotations(annotations).
		WithSpec(appsv1ac.StatefulSetSpec().WithReplicas(target))
}

// Progress reports the StatefulSet as having reached its count once its
// status is up to date and every replica is ready. status.replicas counts
// pods until they have gone, so a scale-down isn't reached while pods above
// the count are still to stop. readyReplicas includes those pods while they
// are ready.
func (StatefulSet) Progress(obj client.Object) Progress {
	s := obj.(*appsv1.StatefulSet)
	want := replicasOrDefault(s.Spec.Replicas)
	return Progress{
		Ready:    s.Status.ReadyReplicas,
		Stopping: max(0, s.Status.Replicas-want),
		Reached: s.Status.ObservedGeneration >= s.Generation &&
			s.Status.Replicas == want && s.Status.ReadyReplicas == want,
	}
}
