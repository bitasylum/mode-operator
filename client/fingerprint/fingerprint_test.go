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

package fingerprint

import (
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func node(name, bootID string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: bootID}},
	}
}

func TestFromNodesLeavesOutEmptyBootIDs(t *testing.T) {
	got := FromNodes([]corev1.Node{*node("a", "1"), *node("b", ""), *node("c", "3")})
	if want := (BootIDs{"a": "1", "c": "3"}); !got.Equal(want) {
		t.Errorf("FromNodes = %v, want %v", got, want)
	}
}

func TestTake(t *testing.T) {
	client := fake.NewClientset(node("a", "1"), node("b", "2"), node("c", ""))
	got, err := Take(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	if want := (BootIDs{"a": "1", "b": "2"}); !got.Equal(want) {
		t.Errorf("Take = %v, want %v", got, want)
	}
}

func TestOverlap(t *testing.T) {
	stored := BootIDs{"a": "1", "b": "2", "c": "3"}
	cases := []struct {
		name string
		live BootIDs
		want []string
	}{
		{name: "nothing rebooted", live: BootIDs{"a": "1", "b": "2", "c": "3"}, want: []string{"a", "b", "c"}},
		{name: "a rolling reboot is half way", live: BootIDs{"a": "9", "b": "2", "c": "3"}, want: []string{"b", "c"}},
		{name: "every node rebooted", live: BootIDs{"a": "7", "b": "8", "c": "9"}},
		{name: "no live boot IDs", live: BootIDs{}},
	}
	for _, c := range cases {
		if got := Overlap(stored, c.live); !slices.Equal(got, c.want) {
			t.Errorf("%s: Overlap = %v, want %v", c.name, got, c.want)
		}
	}
	if got := Overlap(BootIDs{}, BootIDs{"a": "1"}); got != nil {
		t.Errorf("an empty fingerprint overlaps nothing, got %v", got)
	}
}

func TestCompare(t *testing.T) {
	before := BootIDs{"a": "1", "b": "2", "c": "3"}
	after := BootIDs{"a": "1", "b": "20", "d": "4"}
	want := Diff{Unchanged: []string{"a"}, Rebooted: []string{"b"}, Gone: []string{"c"}, Added: []string{"d"}}
	if got := Compare(before, after); !reflect.DeepEqual(got, want) {
		t.Errorf("Compare = %+v, want %+v", got, want)
	}
}

func TestRecordData(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 12, 44, 0, time.UTC)
	r := Record{
		ClusterMode:       "default",
		BootIDs:           BootIDs{"node-b": "91ad", "node-a": "3f0c"},
		UpdatedAt:         at,
		LastStartupAt:     at.Add(-time.Hour),
		RestoredOnStartup: true,
	}
	data := r.Data()
	want := map[string]string{
		"clusterMode":       "default",
		"bootIDs":           "node-a=3f0c\nnode-b=91ad\n",
		"updatedAt":         "2026-10-17T09:12:44Z",
		"lastStartupAt":     "2026-10-17T08:12:44Z",
		"restoredOnStartup": "true",
	}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("Data = %v, want %v", data, want)
	}
	if got := ParseRecord(data); !reflect.DeepEqual(got, r) {
		t.Errorf("ParseRecord(Data) = %+v, want %+v", got, r)
	}

	messy := ParseRecord(map[string]string{
		"bootIDs":           "node-a=1\n\nnot a line\nnode-b=\n=orphan\n  node-c=3  \n",
		"updatedAt":         "yesterday",
		"restoredOnStartup": "yes",
	})
	if want := (BootIDs{"node-a": "1", "node-c": "3"}); !messy.BootIDs.Equal(want) {
		t.Errorf("malformed lines: BootIDs = %v, want %v", messy.BootIDs, want)
	}
	if !messy.UpdatedAt.IsZero() || messy.RestoredOnStartup {
		t.Errorf("unparsable fields should be zero: %+v", messy)
	}
}

func TestStore(t *testing.T) {
	client := fake.NewClientset()
	s := NewStore(client, "mode-manager", DefaultName("default"))
	if got, err := s.Load(t.Context()); err != nil || got != nil {
		t.Fatalf("Load before Save = %v, %v; want nil, nil", got, err)
	}
	r := Record{ClusterMode: "default", BootIDs: BootIDs{"a": "1"}, UpdatedAt: time.Unix(0, 0).UTC()}
	if err := s.Save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("mode-manager").Get(t.Context(), "mode-manager-fingerprint-default",
		metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.ManagedFields[0].Manager != DefaultFieldManager {
		t.Errorf("written by %q, want %q", cm.ManagedFields[0].Manager, DefaultFieldManager)
	}
	got, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(*got, r) {
		t.Errorf("Load = %+v, %v; want %+v", got, err, r)
	}
}
