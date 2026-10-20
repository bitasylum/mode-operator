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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Reader reads one ClusterMode, and the ClusterModeDefinitions it follows.
// It is safe for concurrent use.
type Reader struct {
	client    dynamic.Interface
	discovery discovery.DiscoveryInterface
	name      string
}

// NewReader returns a Reader for the ClusterMode called name.
func NewReader(client dynamic.Interface, disc discovery.DiscoveryInterface, name string) *Reader {
	return &Reader{client: client, discovery: disc, name: name}
}

// NewReaderForConfig returns a Reader for the ClusterMode called name, with
// clients made from cfg.
func NewReaderForConfig(cfg *rest.Config, name string) (*Reader, error) {
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating discovery client: %w", err)
	}
	return NewReader(client, disc, name), nil
}

// Name returns the name of the ClusterMode the Reader reads.
func (r *Reader) Name() string { return r.name }

// Available reports whether the API server serves ClusterModes, which it
// does once the CRDs are installed.
func (r *Reader) Available(_ context.Context) (bool, error) {
	list, err := r.discovery.ServerResourcesForGroupVersion(Group + "/" + Version)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("discovering %s/%s: %w", Group, Version, err)
	}
	for _, res := range list.APIResources {
		if res.Name == ClusterModeResource.Resource {
			return true, nil
		}
	}
	return false, nil
}

// Get returns the ClusterMode. It returns an [*Error] with [ReasonNotFound]
// if it doesn't exist, and [ReasonNotInstalled] if the CRDs aren't installed.
func (r *Reader) Get(ctx context.Context) (*ClusterMode, error) {
	u, err := r.client.Resource(ClusterModeResource).Get(ctx, r.name, metav1.GetOptions{})
	if err != nil {
		return nil, r.apiError(ctx, err, ReasonNotFound)
	}
	return clusterModeFrom(u)
}

// Definition returns the ClusterModeDefinition called name. It returns an
// [*Error] with [ReasonDefinitionNotFound] if it doesn't exist.
func (r *Reader) Definition(ctx context.Context, name string) (*Definition, error) {
	u, err := r.client.Resource(DefinitionResource).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, r.apiError(ctx, err, ReasonDefinitionNotFound)
	}
	return definitionFrom(u)
}

// Rules returns the rules for changing cm's mode: the copy in its status,
// which is what the API server checks changes against, or the definition's
// own rules until mode-operator has written that copy. Reading the
// definition needs read access to ClusterModeDefinitions; without it, Rules
// returns an [*Error] with [ReasonRulesUnavailable].
func (r *Reader) Rules(ctx context.Context, cm *ClusterMode) (Rules, error) {
	if rules, ok := cm.Rules(); ok {
		return rules, nil
	}
	def, err := r.Definition(ctx, cm.Definition)
	if err != nil {
		if ReasonOf(err) == ReasonForbidden {
			return Rules{}, &Error{Reason: ReasonRulesUnavailable, Err: err}
		}
		return Rules{}, err
	}
	return def.Rules, nil
}

// apiError returns err as an [*Error] where it has a reason a caller can act
// on: notFound for a missing object, [ReasonNotInstalled] when the CRDs are
// missing, and [ReasonForbidden].
func (r *Reader) apiError(ctx context.Context, err error, notFound Reason) error {
	switch {
	case apierrors.IsNotFound(err):
		if ok, derr := r.Available(ctx); derr == nil && !ok {
			return &Error{Reason: ReasonNotInstalled, Err: err}
		}
		return &Error{Reason: notFound, Err: err}
	case apierrors.IsForbidden(err):
		return &Error{Reason: ReasonForbidden, Err: err}
	}
	return err
}
