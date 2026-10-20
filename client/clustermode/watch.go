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

package clustermode

import (
	"context"
	"sync"
	"sync/atomic"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// EventType says what happened to the ClusterMode.
type EventType string

// Event types.
const (
	// Added: the ClusterMode was created, or existed when the watch started.
	Added EventType = "Added"
	// Updated: the ClusterMode changed.
	Updated EventType = "Updated"
	// Deleted: the ClusterMode was deleted.
	Deleted EventType = "Deleted"
	// Missing: the ClusterMode didn't exist when the watch started.
	Missing EventType = "Missing"
)

// Event is a change to the watched ClusterMode.
type Event struct {
	Type EventType
	// ClusterMode is the ClusterMode after the change. It is nil for Deleted
	// and Missing.
	ClusterMode *ClusterMode
	// Previous is the ClusterMode before the change. It is nil for Added and
	// Missing.
	Previous *ClusterMode
	// Initial is true for the events that report the ClusterMode as it was
	// when the watch started.
	Initial bool
}

// Watch calls fn with every change to the ClusterMode, one at a time, until
// ctx is done. It starts with an Added or Missing event for the ClusterMode
// as it is. It returns an [*Error] with [ReasonNotInstalled] if the CRDs
// aren't installed or are removed, and ctx's error otherwise.
//
// Watch lists and watches the one ClusterMode by name, so read access to it
// alone is enough, as RBAC rules limited by resourceNames give.
func (r *Reader) Watch(ctx context.Context, fn func(Event)) error {
	log := klog.FromContext(ctx).WithValues("clustermode", r.name)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var notInstalled atomic.Bool
	missingCRD := func(err error) {
		if apierrors.IsNotFound(err) {
			if ok, derr := r.Available(runCtx); derr == nil && !ok {
				notInstalled.Store(true)
				cancel()
			}
		}
	}
	selector := fields.OneTermEqualSelector("metadata.name", r.name).String()
	res := r.client.Resource(ClusterModeResource)
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			o.FieldSelector = selector
			// A list at resourceVersion "0" may come from the API server's
			// cache and be older than a write the caller just made, which
			// would make WaitFor see a stale ClusterMode first. Reading the
			// one object from etcd costs little.
			if o.ResourceVersion == "0" {
				o.ResourceVersion = ""
			}
			list, err := res.List(ctx, o)
			if err != nil {
				missingCRD(err)
			}
			return list, err
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			o.FieldSelector = selector
			w, err := res.Watch(ctx, o)
			if err != nil {
				missingCRD(err)
			}
			return w, err
		},
	}

	// Calls to fn are serialized: the informer calls handlers one at a time,
	// and the Missing event comes from this goroutine.
	var (
		mu        sync.Mutex
		delivered bool
	)
	call := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		delivered = true
		fn(e)
	}
	decode := func(obj any) *ClusterMode {
		if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = d.Obj
		}
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil
		}
		cm, err := clusterModeFrom(u)
		if err != nil {
			log.Error(err, "Could not decode ClusterMode")
			return nil
		}
		return cm
	}
	handler := cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(obj any, initial bool) {
			if cm := decode(obj); cm != nil {
				call(Event{Type: Added, ClusterMode: cm, Initial: initial})
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			if cm := decode(newObj); cm != nil {
				call(Event{Type: Updated, ClusterMode: cm, Previous: decode(oldObj)})
			}
		},
		DeleteFunc: func(obj any) {
			call(Event{Type: Deleted, Previous: decode(obj)})
		},
	}
	_, controller := cache.NewInformerWithOptions(cache.InformerOptions{
		// Clients without watch-list support, such as the fake dynamic
		// client, would otherwise never deliver the first list.
		ListerWatcher: cache.ToListWatcherWithWatchListSemantics(lw, r.client),
		ObjectType:    &unstructured.Unstructured{},
		Handler:       handler,
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		controller.RunWithContext(runCtx)
	}()
	// No event by the time the first list has synced means the ClusterMode
	// didn't exist.
	if cache.WaitForCacheSync(runCtx.Done(), controller.HasSynced) {
		mu.Lock()
		if !delivered {
			delivered = true
			fn(Event{Type: Missing, Initial: true})
		}
		mu.Unlock()
	}
	<-done

	if notInstalled.Load() {
		return &Error{Reason: ReasonNotInstalled}
	}
	return ctx.Err()
}

// WaitFor watches the ClusterMode until done returns true or an error, and
// returns the ClusterMode it stopped at. done is called with nil while the
// ClusterMode doesn't exist.
func (r *Reader) WaitFor(ctx context.Context, done func(*ClusterMode) (bool, error)) (*ClusterMode, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		result    *ClusterMode
		resultErr error
		finished  bool
	)
	err := r.Watch(ctx, func(e Event) {
		if finished {
			return
		}
		ok, err := done(e.ClusterMode)
		if ok || err != nil {
			result, resultErr, finished = e.ClusterMode, err, true
			cancel()
		}
	})
	if finished {
		return result, resultErr
	}
	return nil, err
}

// WaitSettled waits until the ClusterMode has settled in mode: every
// workload has reached the mode's replica counts and no pod is still
// stopping, as mode-operator reports for the latest spec. It returns an
// [*Error] with [ReasonInterrupted] if the mode changes to another mode, and
// [ReasonNotFound] if the ClusterMode doesn't exist or is deleted.
func (r *Reader) WaitSettled(ctx context.Context, mode string) (*ClusterMode, error) {
	return r.WaitFor(ctx, func(cm *ClusterMode) (bool, error) {
		if cm == nil {
			return false, &Error{Reason: ReasonNotFound, Mode: mode}
		}
		if cm.Mode != mode {
			return false, &Error{Reason: ReasonInterrupted, Mode: cm.Mode, From: mode, By: cm.ModeOwner}
		}
		return cm.Settled(), nil
	})
}
