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
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	modev1alpha1 "github.com/Audiobuzz/mode-operator/api/v1alpha1"
	"github.com/Audiobuzz/mode-operator/internal/workload"
)

// Event actions on workloads.
const (
	actionScale            = "Scale"
	actionCheckAnnotations = "CheckAnnotations"
	actionFindClusterMode  = "FindClusterMode"
)

// maxEventNote is the longest note an events.k8s.io/v1 Event accepts.
const maxEventNote = 1024

// WorkloadReconciler scales one kind of workload to the count its replicas
// annotation gives for the current mode of the ClusterMode it follows.
type WorkloadReconciler struct {
	client.Client
	Kind     workload.Kind
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=mode.bitasylum.org,resources=clustermodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=mode.bitasylum.org,resources=clustermodedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile applies the current mode to one workload.
func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.Kind.NewObject()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		// A workload whose label was removed is gone from the filtered cache.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}

	cmName := obj.GetLabels()[LabelClusterMode]
	log := logf.FromContext(ctx).WithValues("clustermode", cmName)
	if cmName == "" {
		r.warn(obj, nil, ReasonClusterModeNotFound, actionFindClusterMode,
			fmt.Sprintf("The %s label is empty; it must name a ClusterMode. Leaving replicas unchanged", LabelClusterMode))
		return ctrl.Result{}, nil
	}
	cm := &modev1alpha1.ClusterMode{}
	if err := r.Get(ctx, client.ObjectKey{Name: cmName}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			// Creating the ClusterMode retriggers.
			r.warn(obj, nil, ReasonClusterModeNotFound, actionFindClusterMode,
				fmt.Sprintf("ClusterMode %q doesn't exist. Leaving replicas unchanged", cmName))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// A missing definition, or a mode that isn't in it, is reported in the
	// ClusterMode's status.
	def := &modev1alpha1.ClusterModeDefinition{}
	if err := r.Get(ctx, client.ObjectKey{Name: cm.Spec.Definition}, def); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("ClusterModeDefinition not found; leaving workload unchanged", "definition", cm.Spec.Definition)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	modes := sets.New[string]()
	for _, m := range def.Spec.Modes {
		modes.Insert(string(m))
	}
	mode := string(cm.Spec.Mode)
	log = log.WithValues("mode", mode)
	if !modes.Has(mode) {
		log.V(1).Info("Mode isn't in the ClusterModeDefinition; leaving workload unchanged", "definition", def.Name)
		return ctrl.Result{}, nil
	}

	ua := parseUserAnnotations(obj.GetAnnotations(), modes, r.Kind.MaxCount())
	r.reportAnnotationErrors(obj, cm, ua)
	if ua.replicasErr != nil {
		return ctrl.Result{}, nil
	}

	cur := r.Kind.Current(obj)
	ann := obj.GetAnnotations()
	d := decide(mode, cur, ua.replicas.ReplicasFor(mode), ua.delay, ua.delayErr == nil, ann, time.Now())
	if r.Kind.Holds(obj, d.target) &&
		ann[AnnotationAppliedMode] == d.appliedMode && ann[AnnotationZeroPendingSince] == d.pendingSince {
		return ctrl.Result{RequeueAfter: d.requeue}, nil
	}

	if err := r.Apply(ctx, r.Kind.ApplyConfiguration(obj, d.target, d.annotations()),
		client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying replicas %d: %w", d.target, err)
	}

	log = log.WithValues("from", cur, "to", d.target)
	switch d.event {
	case ReasonModeApplied:
		log.Info("Applied mode")
		r.Recorder.Eventf(obj, cm, corev1.EventTypeNormal, d.event, actionScale,
			"Mode %q: replicas %d -> %d", mode, cur, d.target)
	case ReasonScaleToZeroPending:
		in := d.requeue.Round(time.Second)
		log.Info("Holding replicas until the scale-to-zero delay ends", "in", in)
		r.Recorder.Eventf(obj, cm, corev1.EventTypeNormal, d.event, actionScale,
			"Mode %q: holding %d replicas, scaling to 0 in %s", mode, cur, in)
	case ReasonScaleToZeroCancelled:
		log.Info("Cancelled scale to zero")
		r.Recorder.Eventf(obj, cm, corev1.EventTypeNormal, d.event, actionScale,
			"Mode %q: scale to zero cancelled, replicas %d -> %d", mode, cur, d.target)
	default:
		log.V(1).Info("Updated controller annotations")
	}
	return ctrl.Result{RequeueAfter: d.requeue}, nil
}

// reportAnnotationErrors emits a Warning event for each annotation problem.
func (r *WorkloadReconciler) reportAnnotationErrors(obj client.Object, cm *modev1alpha1.ClusterMode, ua userAnnotations) {
	if ua.replicasErr != nil {
		reason := ReasonInvalidAnnotation
		if errors.Is(ua.replicasErr, errMissingReplicas) {
			reason = ReasonMissingAnnotation
		}
		r.warn(obj, cm, reason, actionCheckAnnotations, ua.replicasErr.Error()+". Leaving replicas unchanged")
	}
	if ua.delayErr != nil {
		r.warn(obj, cm, ReasonInvalidAnnotation, actionCheckAnnotations,
			ua.delayErr.Error()+". Scaling to zero waits until it is fixed")
	}
	if ua.settleTimeoutErr != nil {
		r.warn(obj, cm, ReasonInvalidAnnotation, actionCheckAnnotations,
			ua.settleTimeoutErr.Error()+". Using the default settle timeout")
	}
}

// warn emits a Warning event, cutting the note to the length the API allows.
func (r *WorkloadReconciler) warn(obj client.Object, related runtime.Object, reason, action, note string) {
	if len(note) > maxEventNote {
		note = strings.ToValidUTF8(note[:maxEventNote-3], "") + "..."
	}
	r.Recorder.Eventf(obj, related, corev1.EventTypeWarning, reason, action, "%s", note)
}

// SetupWithManager registers the reconciler for its kind.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// A ClusterMode or definition matters when it's created and when its spec
	// changes. Deleting one changes nothing: workloads stay as they are.
	specChanges := predicate.And(
		predicate.GenerationChangedPredicate{},
		predicate.Funcs{DeleteFunc: func(event.DeleteEvent) bool { return false }},
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named("mode-operator-"+strings.ToLower(r.Kind.Name())).
		For(r.Kind.NewObject(), builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			// A label change can move the workload to another ClusterMode.
			predicate.LabelChangedPredicate{},
		))).
		Watches(&modev1alpha1.ClusterMode{},
			handler.EnqueueRequestsFromMapFunc(r.workloadsFollowing), builder.WithPredicates(specChanges)).
		Watches(&modev1alpha1.ClusterModeDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.workloadsUsing), builder.WithPredicates(specChanges)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}

// workloadsFollowing maps a ClusterMode to the workloads of this kind that
// follow it.
func (r *WorkloadReconciler) workloadsFollowing(ctx context.Context, cm client.Object) []reconcile.Request {
	return r.requestsFor(ctx, cm.GetName())
}

// workloadsUsing maps a ClusterModeDefinition to the workloads of this kind
// that follow a ClusterMode using it, so their annotations are checked again
// when its modes change.
func (r *WorkloadReconciler) workloadsUsing(ctx context.Context, def client.Object) []reconcile.Request {
	var cms modev1alpha1.ClusterModeList
	if err := r.List(ctx, &cms, client.MatchingFields{indexDefinition: def.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list ClusterModes", "definition", def.GetName())
		return nil
	}
	var reqs []reconcile.Request
	for _, cm := range cms.Items {
		reqs = append(reqs, r.requestsFor(ctx, cm.Name)...)
	}
	return reqs
}

func (r *WorkloadReconciler) requestsFor(ctx context.Context, clusterMode string) []reconcile.Request {
	list := r.Kind.NewList()
	if err := r.List(ctx, list, client.MatchingFields{indexClusterMode: clusterMode}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list workloads", "kind", r.Kind.Name(), "clustermode", clusterMode)
		return nil
	}
	var reqs []reconcile.Request
	_ = meta.EachListItem(list, func(o runtime.Object) error {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(o.(client.Object))})
		return nil
	})
	return reqs
}
