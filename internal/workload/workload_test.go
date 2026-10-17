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
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// replicaKind builds objects of a kind whose count is spec.replicas.
type replicaKind struct {
	kind Kind
	// build returns an object with the given spec.replicas, generation and
	// status.
	build func(replicas *int32, generation, observedGeneration int64, statusReplicas, ready int32) client.Object
}

var replicaKinds = []replicaKind{
	{
		kind: Deployment{},
		build: func(replicas *int32, generation, observed int64, statusReplicas, ready int32) client.Object {
			return &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", Generation: generation},
				Spec:       appsv1.DeploymentSpec{Replicas: replicas},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: observed, Replicas: statusReplicas, ReadyReplicas: ready,
				},
			}
		},
	},
	{
		kind: StatefulSet{},
		build: func(replicas *int32, generation, observed int64, statusReplicas, ready int32) client.Object {
			return &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", Generation: generation},
				Spec:       appsv1.StatefulSetSpec{Replicas: replicas},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: observed, Replicas: statusReplicas, ReadyReplicas: ready,
				},
			}
		},
	},
}

func TestCurrentAndHolds(t *testing.T) {
	for _, rk := range replicaKinds {
		t.Run(rk.kind.Name(), func(t *testing.T) {
			set := rk.build(ptr.To[int32](3), 1, 1, 0, 0)
			if got := rk.kind.Current(set); got != 3 {
				t.Errorf("Current = %d, want 3", got)
			}
			if !rk.kind.Holds(set, 3) || rk.kind.Holds(set, 2) {
				t.Errorf("Holds should be true for 3 only")
			}

			// The API server defaults spec.replicas to 1, so nil counts as 1,
			// but it isn't treated as holding 1: applying sets it explicitly.
			unset := rk.build(nil, 1, 1, 0, 0)
			if got := rk.kind.Current(unset); got != 1 {
				t.Errorf("Current with nil replicas = %d, want 1", got)
			}
			if rk.kind.Holds(unset, 1) {
				t.Errorf("Holds with nil replicas should be false")
			}
		})
	}
}

func TestApplyConfiguration(t *testing.T) {
	ann := map[string]string{"mode.bitasylum.org/applied-mode": "peak"}
	for _, rk := range replicaKinds {
		t.Run(rk.kind.Name(), func(t *testing.T) {
			// Fields other than the count on the live object must not appear
			// in the request, or mode-operator would take ownership of them.
			live := rk.build(ptr.To[int32](3), 7, 7, 3, 3)
			live.SetLabels(map[string]string{"app": "web"})

			got, err := json.Marshal(rk.kind.ApplyConfiguration(live, 6, ann))
			if err != nil {
				t.Fatal(err)
			}
			want := `{"kind":"` + rk.kind.Name() + `","apiVersion":"apps/v1",` +
				`"metadata":{"name":"web","namespace":"shop","annotations":{"mode.bitasylum.org/applied-mode":"peak"}},` +
				`"spec":{"replicas":6}}`
			if string(got) != want {
				t.Errorf("apply request\n got: %s\nwant: %s", got, want)
			}

			// With no annotations the request carries none, so server-side
			// apply removes any that mode-operator set before.
			got, err = json.Marshal(rk.kind.ApplyConfiguration(live, 0, nil))
			if err != nil {
				t.Fatal(err)
			}
			want = `{"kind":"` + rk.kind.Name() + `","apiVersion":"apps/v1",` +
				`"metadata":{"name":"web","namespace":"shop"},"spec":{"replicas":0}}`
			if string(got) != want {
				t.Errorf("apply request with no annotations\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestProgress(t *testing.T) {
	cases := []struct {
		name               string
		replicas           *int32
		generation         int64
		observedGeneration int64
		statusReplicas     int32
		ready              int32
		wantReached        bool
	}{
		{name: "all ready", replicas: ptr.To[int32](3), generation: 2, observedGeneration: 2,
			statusReplicas: 3, ready: 3, wantReached: true},
		{name: "some ready", replicas: ptr.To[int32](3), generation: 2, observedGeneration: 2,
			statusReplicas: 3, ready: 2},
		{name: "scaling down, extra pods still counted", replicas: ptr.To[int32](1), generation: 2, observedGeneration: 2,
			statusReplicas: 3, ready: 1},
		{name: "status from an older generation", replicas: ptr.To[int32](3), generation: 3, observedGeneration: 2,
			statusReplicas: 3, ready: 3},
		{name: "scaled to zero", replicas: ptr.To[int32](0), generation: 4, observedGeneration: 4,
			statusReplicas: 0, ready: 0, wantReached: true},
		{name: "nil replicas means 1", replicas: nil, generation: 1, observedGeneration: 1,
			statusReplicas: 1, ready: 1, wantReached: true},
	}
	for _, rk := range replicaKinds {
		for _, c := range cases {
			t.Run(rk.kind.Name()+"/"+c.name, func(t *testing.T) {
				obj := rk.build(c.replicas, c.generation, c.observedGeneration, c.statusReplicas, c.ready)
				p := rk.kind.Progress(obj)
				if p.Ready != c.ready || p.Reached != c.wantReached {
					t.Errorf("Progress = %+v; want ready %d, reached %v", p, c.ready, c.wantReached)
				}
			})
		}
	}
}

// A Deployment leaves terminating pods out of status.replicas and reports
// them in status.terminatingReplicas, where the cluster has that field.
func TestDeploymentWaitsForPodsShuttingDown(t *testing.T) {
	scaledDown := func(terminating *int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Generation: 3},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr.To[int32](0)},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 3, TerminatingReplicas: terminating},
		}
	}
	cases := []struct {
		name        string
		terminating *int32
		want        Progress
	}{
		{name: "pods still shutting down", terminating: ptr.To[int32](2), want: Progress{Stopping: 2}},
		{name: "every pod gone", terminating: ptr.To[int32](0), want: Progress{Reached: true}},
		{name: "a cluster that doesn't report terminating pods", terminating: nil, want: Progress{Reached: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Deployment{}).Progress(scaledDown(c.terminating)); got != c.want {
				t.Errorf("Progress = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A StatefulSet counts its pods in status.replicas until they have gone.
func TestStatefulSetWaitsForPodsShuttingDown(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Generation: 3},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To[int32](0)},
		Status:     appsv1.StatefulSetStatus{ObservedGeneration: 3, Replicas: 2, ReadyReplicas: 2},
	}
	if got, want := (StatefulSet{}).Progress(s), (Progress{Ready: 2, Stopping: 2}); got != want {
		t.Errorf("Progress = %+v, want %+v", got, want)
	}
}

func TestNewObjectAndList(t *testing.T) {
	var d, s Kind = Deployment{}, StatefulSet{}
	if _, ok := d.NewObject().(*appsv1.Deployment); !ok {
		t.Error("Deployment.NewObject isn't a *Deployment")
	}
	if _, ok := d.NewList().(*appsv1.DeploymentList); !ok {
		t.Error("Deployment.NewList isn't a *DeploymentList")
	}
	if _, ok := s.NewObject().(*appsv1.StatefulSet); !ok {
		t.Error("StatefulSet.NewObject isn't a *StatefulSet")
	}
	if _, ok := s.NewList().(*appsv1.StatefulSetList); !ok {
		t.Error("StatefulSet.NewList isn't a *StatefulSetList")
	}
}
