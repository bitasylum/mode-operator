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

package clustermode

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// fakeReader is a Reader on the fake dynamic client, with discovery serving
// ClusterModes.
func fakeReader(objects ...runtime.Object) (*Reader, *dynamicfake.FakeDynamicClient) {
	lists := map[schema.GroupVersionResource]string{
		ClusterModeResource: "ClusterModeList",
		DefinitionResource:  "ClusterModeDefinitionList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objects...)
	disc := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{{
		GroupVersion: Group + "/" + Version,
		APIResources: []metav1.APIResource{{Name: "clustermodes"}, {Name: "clustermodedefinitions"}},
	}}}}
	return NewReader(client, disc, "default"), client
}

func clusterModeObject(mode string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version,
		"kind":       "ClusterMode",
		"metadata":   map[string]any{"name": "default"},
		"spec":       map[string]any{"definition": "standard", "mode": mode},
	}}
}

// The fake dynamic client has no watch-list support; a Watch on it must
// still report the ClusterMode, so consumers can test with it.
func TestWatchWithTheFakeClient(t *testing.T) {
	r, _ := fakeReader(clusterModeObject("peak"))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cm, err := r.WaitFor(ctx, func(cm *ClusterMode) (bool, error) { return cm != nil, nil })
	if err != nil || cm.Mode != "peak" {
		t.Fatalf("WaitFor = %+v, %v; want the existing ClusterMode", cm, err)
	}
}

func TestWatchReportsAMissingClusterModeOnce(t *testing.T) {
	r, client := fakeReader()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	events := make(chan Event, 4)
	go func() { _ = r.Watch(ctx, func(e Event) { events <- e }) }()
	if e := <-events; e.Type != Missing || !e.Initial {
		t.Fatalf("first event %+v, want an initial Missing", e)
	}
	if _, err := client.Resource(ClusterModeResource).Create(ctx, clusterModeObject("standby"),
		metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if e := <-events; e.Type != Added || e.ClusterMode.Mode != "standby" {
		t.Errorf("next event %+v, want Added", e)
	}
}
