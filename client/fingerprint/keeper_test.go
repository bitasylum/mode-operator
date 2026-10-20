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
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	clocktesting "k8s.io/utils/clock/testing"
)

// recorder is a Saver that records what it saves, and can fail.
type recorder struct {
	mu    sync.Mutex
	saved []Record
	fail  int
}

func (r *recorder) Save(_ context.Context, rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		return errors.New("saving failed")
	}
	r.saved = append(r.saved, rec)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.saved)
}

func (r *recorder) last() Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saved[len(r.saved)-1]
}

// keeperTest runs a Keeper on a fake cluster with a fake clock.
type keeperTest struct {
	t      *testing.T
	client *fake.Clientset
	clock  *clocktesting.FakeClock
	saver  *recorder
	keeper *Keeper
}

const testDebounce = 2 * time.Second

func startKeeper(t *testing.T, base Record, nodes ...string) *keeperTest {
	t.Helper()
	kt := &keeperTest{
		t:      t,
		client: fake.NewClientset(),
		clock:  clocktesting.NewFakeClock(time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)),
		saver:  &recorder{},
	}
	for i := 0; i+1 < len(nodes); i += 2 {
		kt.addNode(nodes[i], nodes[i+1])
	}
	kt.keeper = NewKeeper(kt.client, KeeperOptions{
		Saver: kt.saver, Base: base, Debounce: testDebounce, Clock: kt.clock,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- kt.keeper.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v", err)
		}
	})
	return kt
}

func (kt *keeperTest) addNode(name, bootID string) {
	kt.t.Helper()
	nodes := kt.client.CoreV1().Nodes()
	if _, err := nodes.Create(kt.t.Context(), node(name, bootID), metav1.CreateOptions{}); err != nil {
		kt.t.Fatal(err)
	}
}

func (kt *keeperTest) setBootID(name, bootID string) {
	kt.t.Helper()
	nodes := kt.client.CoreV1().Nodes()
	if _, err := nodes.Update(kt.t.Context(), node(name, bootID), metav1.UpdateOptions{}); err != nil {
		kt.t.Fatal(err)
	}
}

// wait polls in real time until cond holds.
func (kt *keeperTest) wait(what string, cond func() bool) {
	kt.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			kt.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stepUntil moves the clock on by d whenever the Keeper has a timer running,
// until cond holds. The informer delivers events in real time, so a test
// can't know how many timers it will take.
func (kt *keeperTest) stepUntil(what string, d time.Duration, cond func() bool) {
	kt.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			kt.t.Fatalf("timed out waiting for %s", what)
		}
		if kt.clock.HasWaiters() {
			kt.clock.Step(d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKeeperSavesOnlyWhenTheBootIDsChange(t *testing.T) {
	base := Record{ClusterMode: "default", BootIDs: BootIDs{"a": "1", "b": "2"}, RestoredOnStartup: true}
	kt := startKeeper(t, base, "a", "1", "b", "2")
	kt.wait("the first check", func() bool { return kt.keeper.Current().Equal(base.BootIDs) })
	if kt.saver.count() != 0 {
		t.Fatalf("the boot IDs match the base record, so nothing should be saved")
	}

	// A burst of changes is saved once, after the debounce. The clock doesn't
	// move until both events have had time to arrive.
	kt.setBootID("a", "10")
	kt.addNode("c", "3")
	time.Sleep(300 * time.Millisecond)
	kt.stepUntil("a save", testDebounce, func() bool { return kt.saver.count() > 0 })
	got := kt.saver.last()
	if want := (BootIDs{"a": "10", "b": "2", "c": "3"}); kt.saver.count() != 1 || !got.BootIDs.Equal(want) {
		t.Errorf("saved %d times, last %v; want once, %v", kt.saver.count(), got.BootIDs, want)
	}
	if got.ClusterMode != "default" || !got.RestoredOnStartup || !got.UpdatedAt.Equal(kt.clock.Now()) {
		t.Errorf("the rest of the base record must be saved too, with UpdatedAt: %+v", got)
	}

	// A change that doesn't touch a boot ID starts no save.
	n, err := kt.client.CoreV1().Nodes().Get(t.Context(), "b", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n.Labels = map[string]string{"zone": "x"}
	if _, err := kt.client.CoreV1().Nodes().Update(t.Context(), n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if kt.clock.HasWaiters() {
		t.Errorf("a label change shouldn't schedule a save")
	}

	// A node that leaves is saved too.
	if err := kt.client.CoreV1().Nodes().Delete(t.Context(), "c", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	kt.stepUntil("a second save", testDebounce, func() bool { return kt.saver.count() == 2 })
	if want := (BootIDs{"a": "10", "b": "2"}); !kt.saver.last().BootIDs.Equal(want) {
		t.Errorf("saved %v, want %v", kt.saver.last().BootIDs, want)
	}
}

func TestKeeperRetriesAFailedSave(t *testing.T) {
	kt := startKeeper(t, Record{ClusterMode: "default", BootIDs: BootIDs{"a": "1"}}, "a", "1")
	kt.wait("the first check", func() bool { return len(kt.keeper.Current()) == 1 })
	kt.saver.mu.Lock()
	kt.saver.fail = 2
	kt.saver.mu.Unlock()

	kt.setBootID("a", "2")
	// Each failure schedules a retry; stepping past the longest one reaches
	// them all.
	kt.stepUntil("the save to succeed", testDebounce, func() bool { return kt.saver.count() == 1 })
	kt.saver.mu.Lock()
	failed := kt.saver.fail
	kt.saver.mu.Unlock()
	if failed != 0 {
		t.Errorf("both failing saves should have been tried first")
	}
	if !kt.saver.last().BootIDs.Equal(BootIDs{"a": "2"}) {
		t.Errorf("saved %v", kt.saver.last().BootIDs)
	}
}

func TestKeeperWithoutASaverOnlyTracks(t *testing.T) {
	client := fake.NewClientset(node("a", "1"))
	changes := make(chan BootIDs, 4)
	clock := clocktesting.NewFakeClock(time.Now())
	k := NewKeeper(client, KeeperOptions{Clock: clock, OnChange: func(ids BootIDs) { changes <- ids }})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = k.Run(ctx) }()
	select {
	case ids := <-changes:
		if !ids.Equal(BootIDs{"a": "1"}) {
			t.Errorf("OnChange got %v", ids)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnChange wasn't called for the first set of boot IDs")
	}
}
