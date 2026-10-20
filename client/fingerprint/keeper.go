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
	"maps"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const (
	defaultDebounce = 2 * time.Second
	minRetry        = time.Second
	maxRetry        = 30 * time.Second
)

// Saver saves a [Record]. A [*Store] is one.
type Saver interface {
	Save(ctx context.Context, r Record) error
}

// KeeperOptions configure a [Keeper].
type KeeperOptions struct {
	// Saver saves the record whenever the boot IDs change. Without one, the
	// Keeper only tracks them.
	Saver Saver
	// Base is the record the Keeper saves, with BootIDs and UpdatedAt
	// replaced. Its BootIDs are taken as already saved, so the same set
	// isn't saved again. Saving the whole record keeps server-side apply
	// from dropping the fields the Keeper doesn't change.
	Base Record
	// Debounce is how long the Keeper waits after a change before saving,
	// so a burst of changes is saved once. Changes during the wait don't
	// extend it. It defaults to 2s.
	Debounce time.Duration
	// OnChange, if set, is called with the boot IDs after each change that
	// was saved, or after each change when there is no Saver.
	OnChange func(BootIDs)
	// Clock, if set, replaces the real clock, for tests.
	Clock clock.WithTicker
}

// Keeper watches Nodes and keeps a fingerprint current while nodes join,
// leave and reboot. A node that reboots while the Keeper runs has its new
// boot ID recorded, so a rolling reboot never looks like a full one.
type Keeper struct {
	client kubernetes.Interface
	opts   KeeperOptions
	clock  clock.WithTicker

	mu      sync.Mutex
	current BootIDs
	saved   BootIDs
}

// NewKeeper returns a Keeper. It needs list and watch access to Nodes.
func NewKeeper(client kubernetes.Interface, o KeeperOptions) *Keeper {
	if o.Debounce <= 0 {
		o.Debounce = defaultDebounce
	}
	k := &Keeper{client: client, opts: o, clock: o.Clock, saved: o.Base.BootIDs}
	if k.clock == nil {
		k.clock = clock.RealClock{}
	}
	return k
}

// Current returns the boot IDs the Keeper last saw.
func (k *Keeper) Current() BootIDs {
	k.mu.Lock()
	defer k.mu.Unlock()
	return maps.Clone(k.current)
}

// Run keeps the fingerprint current until ctx is done, then returns ctx's
// error. A failed save is logged and retried with backoff.
func (k *Keeper) Run(ctx context.Context) error {
	log := klog.FromContext(ctx)
	factory := informers.NewSharedInformerFactoryWithOptions(k.client, 0, informers.WithTransform(bootIDOnly))
	nodes := factory.Core().V1().Nodes()
	informer := nodes.Informer()

	changed := make(chan struct{}, 1)
	poke := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(any) { poke() },
		UpdateFunc: func(oldObj, newObj any) {
			if bootID(oldObj) != bootID(newObj) {
				poke()
			}
		},
		DeleteFunc: func(any) { poke() },
	}); err != nil {
		return err
	}
	// The informers stop on informerCtx, so Shutdown can't block on a Run
	// that returns early.
	informerCtx, stop := context.WithCancel(ctx)
	factory.Start(informerCtx.Done())
	defer factory.Shutdown()
	defer stop()
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return ctx.Err()
	}

	var (
		timer clock.Timer
		fire  <-chan time.Time
		retry = minRetry
	)
	schedule := func(d time.Duration) {
		if timer != nil {
			timer.Stop()
		}
		timer = k.clock.NewTimer(d)
		fire = timer.C()
	}
	// check saves the boot IDs if they have changed, and schedules a retry if
	// saving fails.
	check := func() error {
		list, err := nodes.Lister().List(labels.Everything())
		if err != nil {
			return err
		}
		if err := k.update(ctx, FromNodes(derefAll(list))); err != nil {
			log.Error(err, "Could not save the node fingerprint, will retry", "retryAfter", retry)
			schedule(retry)
			retry = min(2*retry, maxRetry)
			return nil
		}
		retry = minRetry
		return nil
	}

	// The first check covers the Nodes the initial list added.
	select {
	case <-changed:
	default:
	}
	if err := check(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()
		case <-changed:
			// A change waits for a save that is already due, rather than
			// putting it off, so a long rolling reboot is still saved at
			// least every Debounce.
			if timer == nil {
				schedule(k.opts.Debounce)
			}
		case <-fire:
			timer, fire = nil, nil
			if err := check(); err != nil {
				return err
			}
		}
	}
}

// update records ids, and saves them if they differ from the saved set.
func (k *Keeper) update(ctx context.Context, ids BootIDs) error {
	k.mu.Lock()
	k.current = ids
	unchanged := ids.Equal(k.saved)
	k.mu.Unlock()
	if unchanged {
		return nil
	}
	if k.opts.Saver != nil {
		r := k.opts.Base
		r.BootIDs, r.UpdatedAt = ids, k.clock.Now().UTC()
		if err := k.opts.Saver.Save(ctx, r); err != nil {
			return err
		}
	}
	k.mu.Lock()
	k.saved = ids
	k.mu.Unlock()
	if k.opts.OnChange != nil {
		k.opts.OnChange(maps.Clone(ids))
	}
	return nil
}

// bootIDOnly keeps only a Node's name and boot ID, to keep the cache small.
func bootIDOnly(obj any) (any, error) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return obj, nil
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: node.Name, ResourceVersion: node.ResourceVersion},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{BootID: node.Status.NodeInfo.BootID}},
	}, nil
}

func bootID(obj any) string {
	if node, ok := obj.(*corev1.Node); ok {
		return node.Status.NodeInfo.BootID
	}
	return ""
}

func derefAll(nodes []*corev1.Node) []corev1.Node {
	out := make([]corev1.Node, len(nodes))
	for i, n := range nodes {
		out[i] = *n
	}
	return out
}
