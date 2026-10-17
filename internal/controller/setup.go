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

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	modev1alpha1 "github.com/bitasylum/mode-operator/api/v1alpha1"
	"github.com/bitasylum/mode-operator/internal/workload"
)

// Field indexes.
const (
	// indexClusterMode indexes workloads by the value of LabelClusterMode.
	indexClusterMode = "clustermode"
	// indexDefinition indexes ClusterModes by spec.definition.
	indexDefinition = "spec.definition"
)

// BuiltinKinds returns the workload kinds that are always handled.
func BuiltinKinds() []workload.Kind {
	return []workload.Kind{workload.Deployment{}, workload.StatefulSet{}}
}

// CacheByObject limits the cache of each kind to workloads that have the
// clustermode label, so unlabelled workloads are never cached. A workload
// whose label is removed then looks deleted.
func CacheByObject(kinds []workload.Kind) (map[client.Object]cache.ByObject, error) {
	req, err := labels.NewRequirement(LabelClusterMode, selection.Exists, nil)
	if err != nil {
		return nil, err
	}
	selector := labels.NewSelector().Add(*req)
	byObject := make(map[client.Object]cache.ByObject, len(kinds))
	for _, k := range kinds {
		byObject[k.NewObject()] = cache.ByObject{Label: selector}
	}
	return byObject, nil
}

// SetupIndexes registers the field indexes that find a ClusterMode's
// workloads and a definition's ClusterModes.
func SetupIndexes(ctx context.Context, mgr manager.Manager, kinds []workload.Kind) error {
	indexer := mgr.GetFieldIndexer()
	if err := indexer.IndexField(ctx, &modev1alpha1.ClusterMode{}, indexDefinition, func(o client.Object) []string {
		return []string{o.(*modev1alpha1.ClusterMode).Spec.Definition}
	}); err != nil {
		return fmt.Errorf("indexing ClusterModes by %s: %w", indexDefinition, err)
	}
	for _, k := range kinds {
		if err := indexer.IndexField(ctx, k.NewObject(), indexClusterMode, func(o client.Object) []string {
			if name, ok := o.GetLabels()[LabelClusterMode]; ok {
				return []string{name}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("indexing %ss by %s: %w", k.Name(), LabelClusterMode, err)
		}
	}
	return nil
}
