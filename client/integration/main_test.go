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

// Package integration tests the client packages against a real API server,
// with mode-operator's CRDs, admission policy and roles.
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/bitasylum/mode-operator/client/clustermode"
	"github.com/bitasylum/mode-operator/client/integration/testenv"
)

var env *testenv.Env

func TestMain(m *testing.M) {
	var err error
	env, err = testenv.Start(testenv.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// eventually retries fn until it returns nil or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still failing after %s: %v", timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// exampleTransitions are the README's: standby is the default, and
// maintenance has no rule.
var (
	exampleModes       = []string{"normal", "peak", "offpeak", "maintenance", "standby"}
	exampleTransitions = []testenv.Transition{
		{From: "normal", To: []string{"peak", "offpeak"}},
		{From: "peak", To: []string{"normal"}},
		{From: "offpeak", To: []string{"normal"}},
		{From: "standby", To: []string{"normal", "maintenance"}},
	}
)

// fixture is a ClusterMode name and a definition for it.
type fixture struct {
	name, definition string
}

func newFixture(t *testing.T, withTransitions bool) fixture {
	t.Helper()
	var transitions []testenv.Transition
	if withTransitions {
		transitions = exampleTransitions
	}
	def, err := env.Definition(testContext(t), exampleModes, "standby", transitions...)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{name: env.Name("cluster"), definition: def}
	t.Cleanup(func() { env.DeleteClusterMode(context.Background(), f.name) })
	return f
}

// user returns a config for a new user with the given grants.
func user(t *testing.T, grants ...testenv.Grant) *rest.Config {
	t.Helper()
	cfg, _, err := env.User(testContext(t), grants...)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// writerUser is bound to the mode manager's writer role, limited to the
// fixture's ClusterMode.
func (f fixture) writerUser(t *testing.T) *rest.Config {
	t.Helper()
	return user(t, testenv.Grant{Role: testenv.WriterRole, ResourceNames: []string{f.name}})
}

func (f fixture) reader(t *testing.T, cfg *rest.Config) *clustermode.Reader {
	t.Helper()
	r, err := clustermode.NewReaderForConfig(cfg, f.name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f fixture) modeManager(t *testing.T) *clustermode.Writer {
	t.Helper()
	return clustermode.NewModeManagerWriter(f.reader(t, f.writerUser(t)), f.definition, "mode-manager.test")
}

func wantReason(t *testing.T, what string, err error, want clustermode.Reason) {
	t.Helper()
	if got := clustermode.ReasonOf(err); got != want {
		t.Fatalf("%s: got reason %q (%v), want %q", what, got, err, want)
	}
}

// get reads the reader's ClusterMode, failing the test if it can't.
func get(t *testing.T, r *clustermode.Reader) *clustermode.ClusterMode {
	t.Helper()
	cm, err := r.Get(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	return cm
}
