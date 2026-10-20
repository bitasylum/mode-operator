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
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/kubernetes"
)

// The ConfigMap keys a Record is stored under.
const (
	keyClusterMode       = "clusterMode"
	keyBootIDs           = "bootIDs"
	keyUpdatedAt         = "updatedAt"
	keyLastStartupAt     = "lastStartupAt"
	keyRestoredOnStartup = "restoredOnStartup"
)

// DefaultFieldManager is the field manager a [Store] writes with unless
// [WithFieldManager] says otherwise.
const DefaultFieldManager = "mode-manager"

// Record is the fingerprint the mode manager keeps for a ClusterMode.
type Record struct {
	// ClusterMode is the ClusterMode the record belongs to.
	ClusterMode string
	// BootIDs are the nodes' boot IDs when the record was last updated.
	BootIDs BootIDs
	// UpdatedAt is when BootIDs were last written.
	UpdatedAt time.Time
	// LastStartupAt is when the mode manager last started.
	LastStartupAt time.Time
	// RestoredOnStartup is false if the last startup reset the ClusterMode
	// to its default mode, and true if it kept the stored mode.
	RestoredOnStartup bool
}

// Data returns the record as ConfigMap data.
func (r Record) Data() map[string]string {
	data := map[string]string{
		keyClusterMode:       r.ClusterMode,
		keyBootIDs:           r.BootIDs.String(),
		keyRestoredOnStartup: fmt.Sprint(r.RestoredOnStartup),
	}
	if !r.UpdatedAt.IsZero() {
		data[keyUpdatedAt] = r.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !r.LastStartupAt.IsZero() {
		data[keyLastStartupAt] = r.LastStartupAt.UTC().Format(time.RFC3339)
	}
	return data
}

// ParseRecord reads a record from ConfigMap data. Lines of bootIDs that
// aren't node=bootID, or have an empty boot ID, are left out, and times that
// don't parse are zero.
func ParseRecord(data map[string]string) Record {
	r := Record{
		ClusterMode:       data[keyClusterMode],
		BootIDs:           BootIDs{},
		RestoredOnStartup: data[keyRestoredOnStartup] == "true",
	}
	for line := range strings.SplitSeq(data[keyBootIDs], "\n") {
		node, id, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && node != "" && id != "" {
			r.BootIDs[node] = id
		}
	}
	r.UpdatedAt, _ = time.Parse(time.RFC3339, data[keyUpdatedAt])
	r.LastStartupAt, _ = time.Parse(time.RFC3339, data[keyLastStartupAt])
	return r
}

// Store keeps a [Record] in a ConfigMap, written with server-side apply. It
// needs create, get and patch access to its own ConfigMap, which can be
// limited by name: server-side apply creates the ConfigMap by name.
type Store struct {
	client       kubernetes.Interface
	namespace    string
	name         string
	fieldManager string
}

// StoreOption configures a [Store].
type StoreOption func(*Store)

// WithFieldManager sets the field manager the Store writes with.
func WithFieldManager(fieldManager string) StoreOption {
	return func(s *Store) { s.fieldManager = fieldManager }
}

// DefaultName is the name of the mode manager's fingerprint ConfigMap for a
// ClusterMode.
func DefaultName(clusterMode string) string { return "mode-manager-fingerprint-" + clusterMode }

// NewStore returns a Store for the ConfigMap called name in namespace.
func NewStore(client kubernetes.Interface, namespace, name string, opts ...StoreOption) *Store {
	s := &Store{client: client, namespace: namespace, name: name, fieldManager: DefaultFieldManager}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Load returns the stored record, or nil if the ConfigMap doesn't exist.
func (s *Store) Load(ctx context.Context) (*Record, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, s.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading ConfigMap %s/%s: %w", s.namespace, s.name, err)
	}
	r := ParseRecord(cm.Data)
	return &r, nil
}

// Save stores r, replacing the whole record, and creates the ConfigMap if
// it doesn't exist.
func (s *Store) Save(ctx context.Context, r Record) error {
	cm := corev1ac.ConfigMap(s.name, s.namespace).WithData(r.Data())
	_, err := s.client.CoreV1().ConfigMaps(s.namespace).Apply(ctx, cm,
		metav1.ApplyOptions{FieldManager: s.fieldManager, Force: true})
	if err != nil {
		return fmt.Errorf("writing ConfigMap %s/%s: %w", s.namespace, s.name, err)
	}
	return nil
}
