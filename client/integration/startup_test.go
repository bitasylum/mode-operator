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

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"

	"github.com/bitasylum/mode-operator/client/clustermode"
	"github.com/bitasylum/mode-operator/client/fingerprint"
	"github.com/bitasylum/mode-operator/client/integration/testenv"
	"github.com/bitasylum/mode-operator/client/startup"
)

// startupTest is a mode manager about to start: its Writer, fingerprint
// store and event recorder, with the nodes' boot IDs under the test's
// control.
type startupTest struct {
	t         *testing.T
	f         fixture
	namespace string
	cfg       *rest.Config
	writer    *clustermode.Writer
	events    *record.FakeRecorder
	live      fingerprint.BootIDs
}

var startupOptions = startup.Options{BuiltinDefault: "standby"}

func newStartupTest(t *testing.T, definition string) *startupTest {
	t.Helper()
	ctx := testContext(t)
	f := newFixture(t, false)
	if definition == "" {
		definition = f.definition
	}
	ns, err := env.Namespace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grants := append([]testenv.Grant{{Role: testenv.WriterRole, ResourceNames: []string{f.name}}},
		fingerprintGrants(ns, fingerprint.DefaultName(f.name))...)
	st := &startupTest{
		t: t, f: f, namespace: ns, cfg: user(t, grants...),
		events: record.NewFakeRecorder(10),
		live:   fingerprint.BootIDs{"node-a": "boot-a1", "node-b": "boot-b1"},
	}
	st.writer = clustermode.NewModeManagerWriter(f.reader(t, st.cfg), definition, "mode-manager.test")
	return st
}

func (st *startupTest) store(cfg *rest.Config) *fingerprint.Store {
	st.t.Helper()
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		st.t.Fatal(err)
	}
	return fingerprint.NewStore(client, st.namespace, fingerprint.DefaultName(st.f.name))
}

func (st *startupTest) deps(store startup.FingerprintStore) startup.Deps {
	return startup.Deps{
		Writer: st.writer, Fingerprints: store, Recorder: st.events,
		Nodes: func(context.Context) (fingerprint.BootIDs, error) { return st.live, nil },
	}
}

func (st *startupTest) run(o startup.Options) startup.Result {
	st.t.Helper()
	res, err := startup.Run(testContext(st.t), st.deps(st.store(st.cfg)), o)
	if err != nil {
		st.t.Fatal(err)
	}
	return res
}

// wantEvent checks the next event recorded, as "Type Reason message".
func (st *startupTest) wantEvent(prefix string) {
	st.t.Helper()
	select {
	case e := <-st.events.Events:
		if !strings.HasPrefix(e, prefix) {
			st.t.Errorf("event %q, want %q...", e, prefix)
		}
	default:
		st.t.Errorf("no event, want %q", prefix)
	}
}

func TestStartupKeepsTheModeUnlessTheClusterRebooted(t *testing.T) {
	st := newStartupTest(t, "")

	res := st.run(startupOptions)
	if res.Decision.Case != startup.CaseClusterModeNotFound || res.ClusterMode.Mode != "standby" ||
		!res.Fingerprint.BootIDs.Equal(st.live) {
		t.Fatalf("first start: %+v", res)
	}
	st.wantEvent("Normal ModeReset")

	if _, err := st.writer.Set(testContext(t), "peak"); err != nil {
		t.Fatal(err)
	}
	// The mode manager restarts; nothing else did.
	res = st.run(startupOptions)
	if res.Decision.Case != startup.CaseOverlap || res.ClusterMode.Mode != "peak" || !res.Fingerprint.RestoredOnStartup {
		t.Fatalf("restart: %+v", res)
	}
	st.wantEvent("Normal ModeRestored")

	// A rolling reboot: one node came back, the other stayed up.
	st.live = fingerprint.BootIDs{"node-a": "boot-a2", "node-b": "boot-b1"}
	if res = st.run(startupOptions); res.Decision.Case != startup.CaseOverlap || res.ClusterMode.Mode != "peak" {
		t.Fatalf("after a rolling reboot: %+v", res)
	}
	st.wantEvent("Normal ModeRestored")

	// Every node rebooted.
	st.live = fingerprint.BootIDs{"node-a": "boot-a3", "node-b": "boot-b3"}
	res = st.run(startupOptions)
	if res.Decision.Case != startup.CaseFullReboot || res.ClusterMode.Mode != "standby" ||
		res.Fingerprint.RestoredOnStartup {
		t.Fatalf("after a full reboot: %+v", res)
	}
	st.wantEvent("Normal ModeReset")
	stored, err := st.store(st.cfg).Load(testContext(t))
	if err != nil || !stored.BootIDs.Equal(st.live) {
		t.Errorf("stored fingerprint %+v, %v; want the live boot IDs", stored, err)
	}
}

// If saving the fingerprint fails after a full-reboot reset, the old
// fingerprint stays, so the next start still sees a full reboot.
func TestStartupIsSafeToRetry(t *testing.T) {
	ctx := testContext(t)
	st := newStartupTest(t, "")
	st.run(startupOptions)
	<-st.events.Events
	if _, err := st.writer.Set(ctx, "peak"); err != nil {
		t.Fatal(err)
	}

	st.live = fingerprint.BootIDs{"node-a": "boot-a9", "node-b": "boot-b9"}
	// A user that can read the ConfigMap but not write it.
	readOnly := user(t, testenv.Grant{Namespace: st.namespace, Rules: []rbacv1.PolicyRule{{
		APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"},
	}}})
	_, err := startup.Run(ctx, st.deps(st.store(readOnly)), startupOptions)
	if err == nil || !strings.Contains(err.Error(), "saving the fingerprint") {
		t.Fatalf("Run with a failing save = %v", err)
	}
	if cm := get(t, st.writer.Reader()); cm.Mode != "standby" {
		t.Errorf("the ClusterMode should already be reset, it is in %s", cm.Mode)
	}
	if len(st.events.Events) != 0 {
		t.Errorf("no event should be recorded before the fingerprint is saved")
	}

	if res := st.run(startupOptions); res.Decision.Case != startup.CaseFullReboot || res.ClusterMode.Mode != "standby" {
		t.Errorf("the retry should reach the same decision: %+v", res.Decision)
	}
}

func TestStartupResetsAModeItCantRun(t *testing.T) {
	st := newStartupTest(t, "")
	st.run(startupOptions)
	<-st.events.Events
	if _, err := st.writer.Set(testContext(t), "peak"); err != nil {
		t.Fatal(err)
	}
	o := startupOptions
	o.KnownModes = []string{"normal", "standby"}
	res := st.run(o)
	if res.Decision.Case != startup.CaseInvalidStoredMode || res.ClusterMode.Mode != "standby" {
		t.Fatalf("%+v", res.Decision)
	}
	st.wantEvent("Warning InvalidStoredMode")
}

func TestStartupWithoutTheDefinition(t *testing.T) {
	st := newStartupTest(t, "missing")
	res, err := startup.Run(testContext(t), st.deps(st.store(st.cfg)), startupOptions)
	if err != nil || res.Decision.Case != startup.CaseDefinitionNotFound || res.ClusterMode.Mode != "standby" {
		t.Fatalf("%+v, %v", res, err)
	}
	st.wantEvent("Warning DefinitionNotFound")
}

func TestStartupBeforeTheCRDs(t *testing.T) {
	ctx := testContext(t)
	bare, err := testenv.Start(testenv.Options{WithoutCRDs: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bare.Stop() }()
	ns, err := bare.Namespace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := clustermode.NewReaderForConfig(bare.Config, "default")
	if err != nil {
		t.Fatal(err)
	}
	deps := startup.Deps{
		Writer:       clustermode.NewModeManagerWriter(r, "standard", "mode-manager.test"),
		Fingerprints: fingerprint.NewStore(bare.Kube, ns, fingerprint.DefaultName("default")),
		Nodes:        func(ctx context.Context) (fingerprint.BootIDs, error) { return fingerprint.Take(ctx, bare.Kube) },
	}
	startedAt := time.Now()
	res, err := startup.Run(ctx, deps, startupOptions)
	if err != nil || res.Decision.Case != startup.CaseNotInstalled || res.Decision.Mode != "standby" ||
		res.ClusterMode != nil {
		t.Fatalf("Run without the CRDs = %+v, %v", res, err)
	}

	if err := bare.InstallCRDs(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, func() error {
		if ok, err := r.Available(ctx); err != nil || !ok {
			return fmt.Errorf("not served yet")
		}
		return nil
	})
	// The mode manager kept running in peak meanwhile.
	res, err = startup.Persist(ctx, deps, "peak", startedAt)
	if err != nil || res.ClusterMode.Mode != "peak" || res.Fingerprint == nil {
		t.Fatalf("Persist = %+v, %v", res, err)
	}
}

func TestStartupRecordsEventsInTheDefaultNamespace(t *testing.T) {
	ctx := testContext(t)
	st := newStartupTest(t, "")
	recorderUser := user(t, testenv.Grant{Namespace: "default", Rules: []rbacv1.PolicyRule{{
		APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"create", "patch"},
	}}})
	client, err := kubernetes.NewForConfig(recorderUser)
	if err != nil {
		t.Fatal(err)
	}
	recorder, stop := startup.NewRecorder(ctx, client, "mode-manager-test")
	defer stop()
	deps := st.deps(st.store(st.cfg))
	deps.Recorder = recorder
	res, err := startup.Run(ctx, deps, startupOptions)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, func() error {
		list, err := env.Kube.CoreV1().Events("default").List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, e := range list.Items {
			if e.InvolvedObject.Name == st.f.name && e.InvolvedObject.UID == res.ClusterMode.UID &&
				e.Reason == startup.ReasonModeReset && e.Source.Component == "mode-manager-test" {
				return nil
			}
		}
		return fmt.Errorf("no ModeReset event for %s among %d", st.f.name, len(list.Items))
	})
}

func TestStartupRefusesAnotherDefinition(t *testing.T) {
	st := newStartupTest(t, "")
	st.run(startupOptions)
	other, err := env.Definition(testContext(t), exampleModes, "standby")
	if err != nil {
		t.Fatal(err)
	}
	st.writer = clustermode.NewModeManagerWriter(st.f.reader(t, st.cfg), other, "mode-manager.test")
	_, err = startup.Run(testContext(t), st.deps(st.store(st.cfg)), startupOptions)
	wantReason(t, "Run with another definition", err, clustermode.ReasonDefinitionMismatch)
}
