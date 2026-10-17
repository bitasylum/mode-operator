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

package workload

import (
	"math"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Deployment is the apps/v1 Deployment kind. Its count is spec.replicas.
type Deployment struct{}

var _ Kind = Deployment{}

func (Deployment) Name() string               { return "Deployment" }
func (Deployment) NewObject() client.Object   { return &appsv1.Deployment{} }
func (Deployment) NewList() client.ObjectList { return &appsv1.DeploymentList{} }
func (Deployment) MaxCount() int32            { return math.MaxInt32 }
func (Deployment) Current(obj client.Object) int32 {
	return replicasOrDefault(obj.(*appsv1.Deployment).Spec.Replicas)
}

func (Deployment) Holds(obj client.Object, target int32) bool {
	r := obj.(*appsv1.Deployment).Spec.Replicas
	return r != nil && *r == target
}

func (Deployment) ApplyConfiguration(obj client.Object, target int32, annotations map[string]string) runtime.ApplyConfiguration {
	return appsv1ac.Deployment(obj.GetName(), obj.GetNamespace()).
		WithAnnotations(annotations).
		WithSpec(appsv1ac.DeploymentSpec().WithReplicas(target))
}

// Progress reports the Deployment as having reached its count once its status
// is up to date, every replica is ready, and no pod is still shutting down.
// status.replicas leaves terminating pods out; Kubernetes reports them in
// status.terminatingReplicas, where the cluster has that field.
func (Deployment) Progress(obj client.Object) Progress {
	d := obj.(*appsv1.Deployment)
	want := replicasOrDefault(d.Spec.Replicas)
	stopping := ptr.Deref(d.Status.TerminatingReplicas, 0)
	return Progress{
		Ready:    d.Status.ReadyReplicas,
		Stopping: stopping,
		Reached: d.Status.ObservedGeneration >= d.Generation &&
			d.Status.Replicas == want && d.Status.ReadyReplicas == want && stopping == 0,
	}
}
