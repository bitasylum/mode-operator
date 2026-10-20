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
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/bitasylum/mode-operator/client/clustermode"
	"github.com/bitasylum/mode-operator/client/integration/testenv"
)

// fieldOwners returns the field managers of the ClusterMode's spec and
// labels, by field.
func fieldOwners(t *testing.T, name string) map[string][]string {
	t.Helper()
	u, err := env.Dynamic.Resource(clustermode.ClusterModeResource).Get(testContext(t), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string][]string{}
	for _, mf := range u.GetManagedFields() {
		if mf.Subresource != "" || mf.FieldsV1 == nil {
			continue
		}
		var fields struct {
			Spec     map[string]any `json:"f:spec"`
			Metadata struct {
				Labels map[string]any `json:"f:labels"`
			} `json:"f:metadata"`
		}
		if err := json.Unmarshal(mf.FieldsV1.GetRawBytes(), &fields); err != nil {
			t.Fatal(err)
		}
		for f := range fields.Spec {
			owners["spec."+f[2:]] = append(owners["spec."+f[2:]], mf.Manager)
		}
		for l := range fields.Metadata.Labels {
			owners["label "+l[2:]] = append(owners["label "+l[2:]], mf.Manager)
		}
	}
	return owners
}

func TestModeManagerWriterCreatesTheClusterMode(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, false)
	w := f.modeManager(t)

	cm, err := w.Set(ctx, "standby")
	if err != nil {
		t.Fatal(err)
	}
	if cm.Mode != "standby" || cm.Definition != f.definition || cm.ModeOwner != clustermode.ModeManagerFieldManager ||
		cm.Labels[clustermode.WriterLabel] != "mode-manager.test" {
		t.Fatalf("created %+v", cm)
	}
	want := map[string][]string{
		"spec.mode":                        {"mode-manager"},
		"spec.definition":                  {"mode-manager"},
		"label " + clustermode.WriterLabel: {"mode-manager"},
	}
	if got := fieldOwners(t, f.name); !mapsEqual(got, want) {
		t.Errorf("field owners = %v, want %v", got, want)
	}

	cm, err = w.Set(ctx, "peak")
	if err != nil {
		t.Fatal(err)
	}
	if cm.Mode != "peak" || cm.Generation != 2 {
		t.Errorf("after a change: %+v", cm)
	}
	if mode, ok := w.Current(); !ok || mode != "peak" {
		t.Errorf("Current() = %q, %v", mode, ok)
	}
}

// The writer role limits create by name too: server-side apply creates the
// ClusterMode by name, and the API server authorizes that create by name.
func TestModeManagerWriterCreatesOnlyItsClusterMode(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, false)
	cfg := f.writerUser(t)

	other := fixture{name: env.Name("other"), definition: f.definition}
	t.Cleanup(func() { env.DeleteClusterMode(context.Background(), other.name) })
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{f.name: true, other.name: false} {
		review, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
			&authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Verb: "create", Group: clustermode.Group, Resource: "clustermodes", Name: name,
				},
			}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if review.Status.Allowed != want {
			t.Errorf("create of ClusterMode %s allowed = %v, want %v", name, review.Status.Allowed, want)
		}
	}
	// RBAC refuses the patch before the create is checked, so this guards
	// against regressions rather than testing the create rule.
	_, err = clustermode.NewModeManagerWriter(other.reader(t, cfg), f.definition, "mode-manager.test").
		Apply(ctx, "standby")
	wantReason(t, "creating another ClusterMode", err, clustermode.ReasonForbidden)

	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": clustermode.Group + "/" + clustermode.Version,
		"kind":       "ClusterMode",
		"metadata":   map[string]any{"name": f.name},
		"spec":       map[string]any{"definition": f.definition, "mode": "standby"},
	}}
	_, err = client.Resource(clustermode.ClusterModeResource).Create(ctx, u, metav1.CreateOptions{})
	if !apierrors.IsForbidden(err) {
		t.Errorf("a plain create of its own ClusterMode = %v, want Forbidden: only server-side apply names it", err)
	}

	if _, err := clustermode.NewModeManagerWriter(f.reader(t, cfg), f.definition, "mode-manager.test").
		Apply(ctx, "standby"); err != nil {
		t.Errorf("creating its own ClusterMode with server-side apply: %v", err)
	}
}

func TestOtherWriters(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, false)
	mm := f.modeManager(t)
	orchestrator := clustermode.NewWriter(f.reader(t, f.writerUser(t)), "orchestrator")

	_, err := orchestrator.Set(ctx, "standby")
	wantReason(t, "another writer creating the ClusterMode", err, clustermode.ReasonNotFound)

	if _, err := mm.Set(ctx, "standby"); err != nil {
		t.Fatal(err)
	}
	cm, err := orchestrator.Set(ctx, "peak")
	if err != nil {
		t.Fatal(err)
	}
	if cm.ModeOwner != "orchestrator" || cm.Labels[clustermode.WriterLabel] != "mode-manager.test" {
		t.Errorf("after the orchestrator's change: %+v", cm)
	}
	if got := fieldOwners(t, f.name)["spec.definition"]; !slices.Equal(got, []string{"mode-manager"}) {
		t.Errorf("another writer must leave spec.definition to the mode manager, owners %v", got)
	}
	if adopted, by := mm.Observe(cm); !adopted || by != "orchestrator" {
		t.Errorf("mode manager Observe = %v, %q; want adopted from orchestrator", adopted, by)
	}

	// The mode manager's next change takes spec.mode back.
	cm, err = mm.Set(ctx, "normal")
	if err != nil {
		t.Fatal(err)
	}
	if cm.ModeOwner != "mode-manager" {
		t.Errorf("after the mode manager's change, the owner is %q", cm.ModeOwner)
	}
	if adopted, _ := mm.Observe(cm); adopted {
		t.Errorf("the mode manager must not adopt its own write")
	}
}

func TestWatch(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, false)
	mm := f.modeManager(t)

	events := make(chan clustermode.Event, 20)
	watchCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- f.reader(t, f.writerUser(t)).Watch(watchCtx, func(e clustermode.Event) { events <- e })
	}()
	next := func(want clustermode.EventType) clustermode.Event {
		t.Helper()
		select {
		case e := <-events:
			if e.Type != want {
				t.Fatalf("got a %s event, want %s: %+v", e.Type, want, e)
			}
			return e
		case <-time.After(20 * time.Second):
			t.Fatalf("no %s event", want)
		}
		return clustermode.Event{}
	}

	if e := next(clustermode.Missing); !e.Initial {
		t.Errorf("the Missing event should be initial")
	}
	if _, err := mm.Set(ctx, "standby"); err != nil {
		t.Fatal(err)
	}
	if e := next(clustermode.Added); e.ClusterMode.Mode != "standby" || e.Initial {
		t.Errorf("Added: %+v", e)
	}
	if err := env.PatchMode(ctx, f.name, "peak", "kubectl-patch"); err != nil {
		t.Fatal(err)
	}
	e := next(clustermode.Updated)
	if e.ClusterMode.Mode != "peak" || e.ClusterMode.ModeOwner != "kubectl-patch" || e.Previous.Mode != "standby" {
		t.Errorf("Updated: %+v", e)
	}
	env.DeleteClusterMode(ctx, f.name)
	if e := next(clustermode.Deleted); e.Previous == nil || e.ClusterMode != nil {
		t.Errorf("Deleted: %+v", e)
	}
	stop()
	if err := <-done; err != context.Canceled {
		t.Errorf("Watch returned %v after its context ended", err)
	}

	// The viewer role can watch it too, and sees it as it is.
	if _, err := mm.Set(ctx, "normal"); err != nil {
		t.Fatal(err)
	}
	viewer := f.reader(t, user(t, testenv.Grant{Role: testenv.ViewerRole}))
	cm, err := viewer.WaitFor(ctx, func(cm *clustermode.ClusterMode) (bool, error) {
		return cm != nil && cm.Mode == "normal", nil
	})
	if err != nil || cm.Mode != "normal" {
		t.Errorf("viewer WaitFor = %+v, %v", cm, err)
	}
}

func TestRulesComeFromTheClusterMode(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, true)
	cm, err := f.modeManager(t).Set(ctx, "standby")
	if err != nil {
		t.Fatal(err)
	}

	// Before mode-operator has written status, the definition's rules apply,
	// for anyone allowed to read it.
	writer := f.reader(t, f.writerUser(t))
	rules, err := writer.Rules(ctx, cm)
	if err != nil || !rules.Restricted() || !rules.Allowed("standby", "maintenance") {
		t.Errorf("writer's rules before status: %+v, %v", rules, err)
	}
	viewer := f.reader(t, user(t, testenv.Grant{Role: testenv.ViewerRole}))
	_, err = viewer.Rules(ctx, get(t, viewer))
	wantReason(t, "viewer reading rules before status", err, clustermode.ReasonRulesUnavailable)
	definitionViewer := f.reader(t, user(t, testenv.Grant{Role: testenv.ViewerRole},
		testenv.Grant{Role: testenv.DefinitionViewerRole}))
	if _, err := definitionViewer.Rules(ctx, get(t, definitionViewer)); err != nil {
		t.Fatal(err)
	}

	// Once it has, the ClusterMode alone is enough.
	if err := env.Operate(ctx, f.name, testenv.ClusterModeStatus{Ready: true}); err != nil {
		t.Fatal(err)
	}
	rules, err = viewer.Rules(ctx, get(t, viewer))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rules.Next("standby"), []string{"normal", "maintenance"}) {
		t.Errorf("viewer's next modes from standby = %v", rules.Next("standby"))
	}
	clusterModeOnly := user(t, testenv.Grant{Rules: []rbacv1.PolicyRule{{
		APIGroups: []string{clustermode.Group}, Resources: []string{"clustermodes"},
		ResourceNames: []string{f.name}, Verbs: []string{"get", "list", "watch", "patch"},
	}}})
	orchestrator := clustermode.NewWriter(f.reader(t, clusterModeOnly), "orchestrator")
	if cm, err := orchestrator.Set(ctx, "normal"); err != nil || cm.Mode != "normal" {
		t.Errorf("orchestrator with ClusterMode access only: %+v, %v", cm, err)
	}
}

func TestCheckReasons(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, true)
	mm := f.modeManager(t)
	if _, err := mm.Set(ctx, "normal"); err != nil {
		t.Fatal(err)
	}
	operate := func(s testenv.ClusterModeStatus) {
		t.Helper()
		if err := env.Operate(ctx, f.name, s); err != nil {
			t.Fatal(err)
		}
	}
	operate(testenv.ClusterModeStatus{Ready: false})

	cases := []struct {
		mode   string
		reason clustermode.Reason
	}{
		{"holiday", clustermode.ReasonUnknownMode},
		{"maintenance", clustermode.ReasonTransitionNotAllowed},
		{"peak", clustermode.ReasonNotSettled},
		{"standby", ""},
		{"normal", ""},
	}
	for _, c := range cases {
		_, err := mm.Check(ctx, c.mode)
		wantReason(t, "Check "+c.mode, err, c.reason)
	}

	operate(testenv.ClusterModeStatus{Ready: true})
	if _, err := mm.Set(ctx, "peak"); err != nil {
		t.Fatal(err)
	}

	// Ready, but from before the last change: only the admission policy
	// catches this, and the rejection is classified by re-reading.
	operate(testenv.ClusterModeStatus{Ready: true, Stale: true})
	_, err := mm.Check(ctx, "normal")
	wantReason(t, "Check on a stale status", err, clustermode.ReasonNotSettled)
	_, err = mm.Apply(ctx, "normal")
	wantReason(t, "Apply on a stale status", err, clustermode.ReasonNotSettled)
	if e, ok := err.(*clustermode.Error); !ok || e.Err == nil {
		t.Errorf("the API server's rejection should be kept: %v", err)
	}

	// The CRD's own rule rejects a change the transitions don't list.
	operate(testenv.ClusterModeStatus{Ready: true})
	_, err = mm.Apply(ctx, "offpeak")
	wantReason(t, "Apply peak to offpeak", err, clustermode.ReasonTransitionNotAllowed)

	// A writer without write access.
	viewer := clustermode.NewWriter(f.reader(t, user(t, testenv.Grant{Role: testenv.ViewerRole})), "viewer")
	_, err = viewer.Apply(ctx, "standby")
	wantReason(t, "viewer writing", err, clustermode.ReasonForbidden)
}

func TestCheckFollowsTheStatusCopy(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, true)
	mm := f.modeManager(t)
	if _, err := mm.Set(ctx, "normal"); err != nil {
		t.Fatal(err)
	}
	if err := env.Operate(ctx, f.name, testenv.ClusterModeStatus{Ready: true}); err != nil {
		t.Fatal(err)
	}

	// The definition now lets normal go to maintenance, but mode-operator
	// hasn't copied that yet, so the API server still wouldn't.
	changed := slices.Clone(exampleTransitions)
	changed[0] = testenv.Transition{From: "normal", To: []string{"peak", "offpeak", "maintenance"}}
	if err := env.SetTransitions(ctx, f.definition, changed); err != nil {
		t.Fatal(err)
	}
	_, err := mm.Check(ctx, "maintenance")
	wantReason(t, "Check before the copy is updated", err, clustermode.ReasonTransitionNotAllowed)

	if err := env.Operate(ctx, f.name, testenv.ClusterModeStatus{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := mm.Set(ctx, "maintenance"); err != nil {
		t.Fatal(err)
	}
}

func TestMoveTo(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, true)
	mm := f.modeManager(t)
	if _, err := mm.Set(ctx, "peak"); err != nil {
		t.Fatal(err)
	}
	if err := env.Operate(ctx, f.name, testenv.ClusterModeStatus{Ready: true}); err != nil {
		t.Fatal(err)
	}

	// The test plays mode-operator, settling each mode as soon as it is
	// written.
	var steps []string
	cm, err := mm.MoveTo(ctx, "maintenance", clustermode.MoveOptions{
		StepTimeout: 20 * time.Second,
		OnStep: func(s clustermode.Step) error {
			steps = append(steps, s.From+">"+s.To+":"+string(s.Phase))
			if s.Phase == clustermode.PhaseWritten {
				return env.Operate(ctx, f.name, testenv.ClusterModeStatus{Ready: true})
			}
			return nil
		},
	})
	if err != nil || cm.Mode != "maintenance" {
		t.Fatalf("MoveTo = %+v, %v", cm, err)
	}
	want := []string{"peak>standby:Written", "standby>maintenance:Waiting", "standby>maintenance:Written"}
	if !slices.Equal(steps, want) {
		t.Errorf("steps = %v, want %v", steps, want)
	}

	// A move waiting for normal to settle is interrupted by a maintainer's
	// change to the default mode, which is always allowed.
	if _, err := mm.MoveTo(ctx, "normal", clustermode.MoveOptions{
		OnStep: func(s clustermode.Step) error {
			if s.Phase == clustermode.PhaseWritten {
				return env.Operate(ctx, f.name, testenv.ClusterModeStatus{Ready: s.To == "standby"})
			}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	_, err = mm.MoveTo(ctx, "peak", clustermode.MoveOptions{
		OnStep: func(s clustermode.Step) error {
			if s.Phase == clustermode.PhaseWaiting {
				once.Do(func() {
					go func() { _ = env.PatchMode(ctx, f.name, "standby", "kubectl-patch") }()
				})
			}
			return nil
		},
	})
	wantReason(t, "MoveTo interrupted", err, clustermode.ReasonInterrupted)
	if e, _ := err.(*clustermode.Error); e.Mode != "standby" || e.By != "kubectl-patch" {
		t.Errorf("interruption = %+v", e)
	}
}

func TestWaitSettled(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, false)
	mm := f.modeManager(t)
	if _, err := mm.Set(ctx, "standby"); err != nil {
		t.Fatal(err)
	}
	r := f.reader(t, f.writerUser(t))

	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := r.WaitSettled(short, "standby"); err != context.DeadlineExceeded {
		t.Errorf("WaitSettled before mode-operator reports = %v, want a timeout", err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = env.Operate(ctx, f.name, testenv.ClusterModeStatus{Ready: true})
	}()
	if cm, err := r.WaitSettled(ctx, "standby"); err != nil || !cm.Settled() {
		t.Errorf("WaitSettled = %+v, %v", cm, err)
	}
	_, err := r.WaitSettled(ctx, "peak")
	wantReason(t, "WaitSettled for another mode", err, clustermode.ReasonInterrupted)
}

func TestWithoutTheCRDs(t *testing.T) {
	ctx := testContext(t)
	bare, err := testenv.Start(testenv.Options{WithoutCRDs: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bare.Stop() }()
	r, err := clustermode.NewReaderForConfig(bare.Config, "default")
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := r.Available(ctx); err != nil || ok {
		t.Fatalf("Available without the CRDs = %v, %v", ok, err)
	}
	_, err = r.Get(ctx)
	wantReason(t, "Get", err, clustermode.ReasonNotInstalled)
	_, err = clustermode.NewModeManagerWriter(r, "standard", "test").Set(ctx, "standby")
	wantReason(t, "Set", err, clustermode.ReasonNotInstalled)
	err = r.Watch(ctx, func(clustermode.Event) {})
	wantReason(t, "Watch", err, clustermode.ReasonNotInstalled)

	if err := bare.InstallCRDs(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, func() error {
		if ok, err := r.Available(ctx); err != nil || !ok {
			return errNotYet
		}
		return nil
	})
}

var errNotYet = errorString("not yet")

type errorString string

func (e errorString) Error() string { return string(e) }

func mapsEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// OnChange reports the mode manager's own writes and the changes it adopts
// from a live watch, in the order they were made.
func TestOnChangeKeepsOrder(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, false)
	mm := f.modeManager(t)
	var (
		mu      sync.Mutex
		changes []string
	)
	mm.OnChange(func(c clustermode.Change) {
		mu.Lock()
		defer mu.Unlock()
		changes = append(changes, fmt.Sprintf("%s by %s adopted=%v", c.Mode, c.By, c.Adopted))
	})
	recorded := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(changes)
	}
	if _, err := mm.Set(ctx, "standby"); err != nil {
		t.Fatal(err)
	}
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = f.reader(t, f.writerUser(t)).Watch(watchCtx, func(e clustermode.Event) { mm.Observe(e.ClusterMode) })
	}()

	waitFor := func(n int) {
		t.Helper()
		eventually(t, 20*time.Second, func() error {
			if got := recorded(); len(got) < n {
				return fmt.Errorf("changes so far: %v", got)
			}
			return nil
		})
	}
	if err := env.PatchMode(ctx, f.name, "peak", "kubectl-patch"); err != nil {
		t.Fatal(err)
	}
	waitFor(2)
	if _, err := mm.Set(ctx, "normal"); err != nil {
		t.Fatal(err)
	}
	if err := env.PatchMode(ctx, f.name, "standby", "orchestrator"); err != nil {
		t.Fatal(err)
	}
	waitFor(4)
	time.Sleep(500 * time.Millisecond) // nothing more should arrive
	want := []string{
		"standby by mode-manager adopted=false",
		"peak by kubectl-patch adopted=true",
		"normal by mode-manager adopted=false",
		"standby by orchestrator adopted=true",
	}
	if got := recorded(); !slices.Equal(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if mode, _ := mm.Current(); mode != get(t, mm.Reader()).Mode {
		t.Errorf("the Writer's current mode %s doesn't match the ClusterMode's", mode)
	}
}

func TestMoveToCreatesAMissingClusterMode(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, true)
	var steps []string
	cm, err := f.modeManager(t).MoveTo(ctx, "standby", clustermode.MoveOptions{
		OnStep: func(s clustermode.Step) error {
			steps = append(steps, fmt.Sprintf("%d/%d %s:%s", s.N, s.Of, s.To, s.Phase))
			return nil
		},
	})
	if err != nil || cm.Mode != "standby" {
		t.Fatalf("MoveTo = %+v, %v", cm, err)
	}
	if !slices.Equal(steps, []string{"1/1 standby:Written"}) {
		t.Errorf("steps = %v", steps)
	}
}

func TestDefinitionMismatch(t *testing.T) {
	ctx := testContext(t)
	f := newFixture(t, false)
	if _, err := f.modeManager(t).Set(ctx, "standby"); err != nil {
		t.Fatal(err)
	}
	other, err := env.Definition(ctx, exampleModes, "standby")
	if err != nil {
		t.Fatal(err)
	}
	w := clustermode.NewModeManagerWriter(f.reader(t, f.writerUser(t)), other, "mode-manager.test")
	_, err = w.Set(ctx, "normal")
	wantReason(t, "Set with another definition", err, clustermode.ReasonDefinitionMismatch)
	_, err = w.Apply(ctx, "normal")
	wantReason(t, "Apply with another definition", err, clustermode.ReasonDefinitionMismatch)
}

func TestWatchEndsWhenTheCRDsAreRemoved(t *testing.T) {
	ctx := testContext(t)
	bare, err := testenv.Start(testenv.Options{WithoutCRDs: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bare.Stop() }()
	if err := bare.InstallCRDs(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := clustermode.NewReaderForConfig(bare.Config, "default")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, func() error {
		if ok, err := r.Available(ctx); err != nil || !ok {
			return fmt.Errorf("not served yet")
		}
		return nil
	})
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		var once sync.Once
		done <- r.Watch(ctx, func(clustermode.Event) { once.Do(func() { close(started) }) })
	}()
	<-started
	if err := bare.UninstallCRDs(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		wantReason(t, "Watch after the CRDs were removed", err, clustermode.ReasonNotInstalled)
	case <-time.After(60 * time.Second):
		t.Fatal("Watch didn't end after the CRDs were removed")
	}
}
