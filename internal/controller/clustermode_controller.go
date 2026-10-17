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

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	modev1alpha1 "github.com/bitasylum/mode-operator/api/v1alpha1"
	"github.com/bitasylum/mode-operator/internal/workload"
)

// minWriteInterval is the shortest time between two status writes for one
// ClusterMode.
const minWriteInterval = 2 * time.Second

// ClusterModeReconciler writes a ClusterMode's status: how far its mode has
// been applied to the workloads that follow it.
type ClusterModeReconciler struct {
	client.Client
	// Kinds are the workload kinds to count.
	Kinds []workload.Kind
	// DefaultSettleTimeout is how long a workload may take to reach its
	// count, unless its settle-timeout annotation says otherwise.
	DefaultSettleTimeout time.Duration

	mu sync.Mutex
	// lastWrite is when each ClusterMode's status was last written.
	lastWrite map[string]time.Time
	// history is, for each ClusterMode, the history its last summary
	// returned.
	history map[string]map[string]workloadHistory
}

// mode-operator writes ClusterMode status only, never spec (design §7.7).
// +kubebuilder:rbac:groups=mode.bitasylum.org,resources=clustermodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=mode.bitasylum.org,resources=clustermodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mode.bitasylum.org,resources=clustermodedefinitions,verbs=get;list;watch

// Reconcile works out a ClusterMode's status and writes it if it changed.
func (r *ClusterModeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cm := &modev1alpha1.ClusterMode{}
	if err := r.Get(ctx, req.NamespacedName, cm); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(req.Name)
			forgetMetrics(req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// def stays nil if the definition doesn't exist.
	var def *modev1alpha1.ClusterModeDefinition
	found := &modev1alpha1.ClusterModeDefinition{}
	switch err := r.Get(ctx, client.ObjectKey{Name: cm.Spec.Definition}, found); {
	case err == nil:
		def = found
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	workloads, err := r.workloadsFollowing(ctx, cm.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	now := time.Now()
	sum := summarize(cm, def, workloads, r.historyOf(cm.Name), now, r.DefaultSettleTimeout)
	r.setHistory(cm.Name, sum.history)
	result := ctrl.Result{RequeueAfter: sum.requeue}

	if equality.Semantic.DeepEqual(cm.Status, sum.status) {
		recordStatus(cm.Name, &sum.status)
		return result, nil
	}
	if wait := r.writeWait(cm.Name, now); wait > 0 {
		if result.RequeueAfter == 0 || wait < result.RequeueAfter {
			result.RequeueAfter = wait
		}
		return result, nil
	}

	// The lock makes a patch worked out from a stale cache fail with a
	// Conflict, to be retried once the cache catches up, rather than leave
	// fields the stale copy didn't know about.
	patch := client.MergeFromWithOptions(cm.DeepCopy(), client.MergeFromWithOptimisticLock{})
	cm.Status = sum.status
	if err := r.Status().Patch(ctx, cm, patch, client.FieldOwner(FieldManager)); err != nil {
		return ctrl.Result{}, fmt.Errorf("writing status: %w", err)
	}
	r.wrote(cm.Name, now)
	if sum.modeChanged {
		countModeChange(cm.Name)
	}
	recordStatus(cm.Name, &sum.status)
	logf.FromContext(ctx).V(1).Info("Wrote ClusterMode status", "mode", cm.Spec.Mode,
		"workloads", sum.status.Workloads.Total, "settled", sum.status.Workloads.Settled)
	return result, nil
}

// workloadsFollowing lists the workloads of every kind that follow a
// ClusterMode.
func (r *ClusterModeReconciler) workloadsFollowing(ctx context.Context, clusterMode string) ([]observedWorkload, error) {
	var out []observedWorkload
	for _, k := range r.Kinds {
		list := k.NewList()
		if err := r.List(ctx, list, client.MatchingFields{indexClusterMode: clusterMode}); err != nil {
			return nil, fmt.Errorf("listing %ss: %w", k.Name(), err)
		}
		if err := meta.EachListItem(list, func(o runtime.Object) error {
			out = append(out, observedWorkload{kind: k, obj: o.(client.Object)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// writeWait returns how long to wait before the next status write for a
// ClusterMode, or 0 to write now.
func (r *ClusterModeReconciler) writeWait(name string, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.lastWrite[name]
	if !ok {
		return 0
	}
	return max(0, minWriteInterval-now.Sub(last))
}

func (r *ClusterModeReconciler) wrote(name string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastWrite == nil {
		r.lastWrite = map[string]time.Time{}
	}
	r.lastWrite[name] = now
}

func (r *ClusterModeReconciler) historyOf(name string) map[string]workloadHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.history[name]
}

func (r *ClusterModeReconciler) setHistory(name string, m map[string]workloadHistory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.history == nil {
		r.history = map[string]map[string]workloadHistory{}
	}
	r.history[name] = m
}

func (r *ClusterModeReconciler) forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lastWrite, name)
	delete(r.history, name)
}

// SetupWithManager registers the status reconciler.
func (r *ClusterModeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("mode-operator-clustermode").
		// Status writes don't change the generation, so they don't retrigger.
		For(&modev1alpha1.ClusterMode{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&modev1alpha1.ClusterModeDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.clusterModesUsing),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	for _, k := range r.Kinds {
		// No predicate: pod readiness arrives as changes to the workload's
		// status. On an update both the old and the new ClusterMode are
		// queued, so a workload that moves updates both.
		b = b.Watches(k.NewObject(), handler.EnqueueRequestsFromMapFunc(clusterModeOf))
	}
	return b.WithOptions(controller.Options{MaxConcurrentReconciles: 2}).Complete(r)
}

// clusterModeOf maps a workload to the ClusterMode its label names.
func clusterModeOf(_ context.Context, obj client.Object) []reconcile.Request {
	if name := obj.GetLabels()[LabelClusterMode]; name != "" {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: name}}}
	}
	return nil
}

// clusterModesUsing maps a ClusterModeDefinition to the ClusterModes that use
// it.
func (r *ClusterModeReconciler) clusterModesUsing(ctx context.Context, def client.Object) []reconcile.Request {
	cms, err := listClusterModesUsing(ctx, r, def.GetName())
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list ClusterModes", "definition", def.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(cms))
	for _, cm := range cms {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKey{Name: cm.Name}})
	}
	return reqs
}

// listClusterModesUsing lists the ClusterModes that use a definition.
func listClusterModesUsing(ctx context.Context, c client.Reader, definition string) ([]modev1alpha1.ClusterMode, error) {
	var cms modev1alpha1.ClusterModeList
	if err := c.List(ctx, &cms, client.MatchingFields{indexDefinition: definition}); err != nil {
		return nil, err
	}
	return cms.Items, nil
}
