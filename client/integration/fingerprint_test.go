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
	"slices"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bitasylum/mode-operator/client/fingerprint"
	"github.com/bitasylum/mode-operator/client/integration/testenv"
)

// fingerprintGrants are the access the mode manager's fingerprint needs:
// Nodes cluster-wide, and its ConfigMap in its own namespace.
func fingerprintGrants(namespace, configMap string) []testenv.Grant {
	return []testenv.Grant{
		{Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"nodes"},
			Verbs: []string{"list", "watch"},
		}}},
		// Server-side apply creates the ConfigMap by name, so create is
		// limited by name too.
		{Namespace: namespace, Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{configMap},
			Verbs: []string{"create", "get", "patch"},
		}}},
	}
}

func kubeFor(t *testing.T, grants ...testenv.Grant) kubernetes.Interface {
	t.Helper()
	client, err := kubernetes.NewForConfig(user(t, grants...))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestStoreWithItsOwnRole(t *testing.T) {
	ctx := testContext(t)
	ns, err := env.Namespace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := fingerprint.DefaultName(env.Name("cluster"))
	s := fingerprint.NewStore(kubeFor(t, fingerprintGrants(ns, name)...), ns, name)

	if r, err := s.Load(ctx); err != nil || r != nil {
		t.Fatalf("Load before Save = %+v, %v", r, err)
	}
	r := fingerprint.Record{
		ClusterMode: "default", BootIDs: fingerprint.BootIDs{"node-a": "1"},
		LastStartupAt: time.Now().UTC().Truncate(time.Second), RestoredOnStartup: true,
	}
	if err := s.Save(ctx, r); err != nil {
		t.Fatalf("creating the record: %v", err)
	}
	r.BootIDs = fingerprint.BootIDs{"node-a": "1", "node-b": "2"}
	if err := s.Save(ctx, r); err != nil {
		t.Fatalf("updating the record: %v", err)
	}
	got, err := s.Load(ctx)
	if err != nil || !got.BootIDs.Equal(r.BootIDs) || !got.LastStartupAt.Equal(r.LastStartupAt) || !got.RestoredOnStartup {
		t.Errorf("Load = %+v, %v; want %+v", got, err, r)
	}

	// Another ConfigMap in the namespace is out of reach.
	other := fingerprint.NewStore(kubeFor(t, fingerprintGrants(ns, name)...), ns, "someone-elses")
	if err := other.Save(ctx, r); err == nil {
		t.Errorf("the role should only allow the store's own ConfigMap")
	}
}

func TestKeeperOnNodes(t *testing.T) {
	ctx := testContext(t)
	ns, err := env.Namespace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var nodes [3]string
	for i, id := range []string{"boot-1", "boot-2", ""} {
		if nodes[i], err = env.Node(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	name := fingerprint.DefaultName(env.Name("cluster"))
	client := kubeFor(t, fingerprintGrants(ns, name)...)
	store := fingerprint.NewStore(client, ns, name)

	before, err := fingerprint.Take(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if before[nodes[0]] != "boot-1" || before[nodes[1]] != "boot-2" {
		t.Fatalf("Take = %v", before)
	}
	if _, ok := before[nodes[2]]; ok {
		t.Errorf("a node without a boot ID must be left out")
	}

	keeperCtx, stop := context.WithCancel(ctx)
	defer stop()
	keeper := fingerprint.NewKeeper(client, fingerprint.KeeperOptions{
		Saver: store, Base: fingerprint.Record{ClusterMode: "default", BootIDs: before}, Debounce: 100 * time.Millisecond,
	})
	go func() { _ = keeper.Run(keeperCtx) }()

	stored := func(check func(fingerprint.BootIDs) bool) func() error {
		return func() error {
			r, err := store.Load(ctx)
			if err != nil {
				return err
			}
			if r == nil || !check(r.BootIDs) {
				return fmt.Errorf("stored %v", r)
			}
			return nil
		}
	}
	if err := env.SetBootID(ctx, nodes[0], "boot-1b"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, stored(func(ids fingerprint.BootIDs) bool { return ids[nodes[0]] == "boot-1b" }))
	if err := env.DeleteNode(ctx, nodes[1]); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, stored(func(ids fingerprint.BootIDs) bool {
		_, ok := ids[nodes[1]]
		return !ok
	}))

	after, err := fingerprint.Take(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	d := fingerprint.Compare(before, after)
	if !slices.Contains(d.Rebooted, nodes[0]) || !slices.Contains(d.Gone, nodes[1]) {
		t.Errorf("Compare = %+v", d)
	}
	if slices.Contains(fingerprint.Overlap(before, after), nodes[0]) {
		t.Errorf("a rebooted node isn't one that stayed up")
	}
}
