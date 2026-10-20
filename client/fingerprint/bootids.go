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

// Package fingerprint records which boot of each node a cluster is on, from
// the boot ID every kubelet reports in its Node's status.nodeInfo.bootID. A
// node's boot ID changes each time it boots, so comparing two fingerprints
// tells which nodes have rebooted in between, and whether any node stayed up.
//
// [Take] and [Compare] need nothing but read access to Nodes, for example to
// confirm that servers really rebooted after a power cycle. [Store] keeps a
// [Record] in a ConfigMap, and a [Keeper] keeps that record current while
// nodes come and go, which is how the mode manager tells a full cluster
// reboot from a restart of its own.
package fingerprint

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// BootIDs maps node names to boot IDs. Nodes that don't report a boot ID are
// left out, so two missing IDs never count as a match.
type BootIDs map[string]string

// FromNodes returns the boot IDs of nodes.
func FromNodes(nodes []corev1.Node) BootIDs {
	ids := BootIDs{}
	for i := range nodes {
		if id := nodes[i].Status.NodeInfo.BootID; id != "" {
			ids[nodes[i].Name] = id
		}
	}
	return ids
}

// Take lists the cluster's Nodes and returns their boot IDs. It needs list
// access to Nodes.
func Take(ctx context.Context, client kubernetes.Interface) (BootIDs, error) {
	ids := BootIDs{}
	opts := metav1.ListOptions{Limit: 500}
	for {
		list, err := client.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("listing Nodes: %w", err)
		}
		maps.Copy(ids, FromNodes(list.Items))
		if list.Continue == "" {
			return ids, nil
		}
		opts.Continue = list.Continue
	}
}

// Equal reports whether b and other hold the same nodes and boot IDs.
func (b BootIDs) Equal(other BootIDs) bool { return maps.Equal(b, other) }

// Nodes returns the node names, sorted.
func (b BootIDs) Nodes() []string { return slices.Sorted(maps.Keys(b)) }

// String returns one node=bootID line per node, sorted by node name, as a
// [Record] stores them.
func (b BootIDs) String() string {
	var sb strings.Builder
	for _, node := range b.Nodes() {
		sb.WriteString(node + "=" + b[node] + "\n")
	}
	return sb.String()
}

// Overlap returns the nodes of stored whose boot ID is also in live, sorted:
// the nodes that have stayed up since stored was taken. No overlap means
// every node has rebooted since, or been replaced.
func Overlap(stored, live BootIDs) []string {
	liveIDs := make(map[string]bool, len(live))
	for _, id := range live {
		liveIDs[id] = true
	}
	var up []string
	for node, id := range stored {
		if liveIDs[id] {
			up = append(up, node)
		}
	}
	slices.Sort(up)
	return up
}

// Diff is how a cluster's nodes changed from one fingerprint to another.
// Each list is sorted.
type Diff struct {
	// Unchanged nodes kept their boot ID: they haven't rebooted.
	Unchanged []string
	// Rebooted nodes have a new boot ID.
	Rebooted []string
	// Gone nodes are only in the first fingerprint.
	Gone []string
	// Added nodes are only in the second.
	Added []string
}

// Compare compares two fingerprints node by node, for example taken before
// and after a power cycle.
func Compare(before, after BootIDs) Diff {
	var d Diff
	for _, node := range before.Nodes() {
		id, ok := after[node]
		switch {
		case !ok:
			d.Gone = append(d.Gone, node)
		case id == before[node]:
			d.Unchanged = append(d.Unchanged, node)
		default:
			d.Rebooted = append(d.Rebooted, node)
		}
	}
	for _, node := range after.Nodes() {
		if _, ok := before[node]; !ok {
			d.Added = append(d.Added, node)
		}
	}
	return d
}
