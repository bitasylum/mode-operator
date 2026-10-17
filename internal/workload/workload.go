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

// Package workload holds what differs between the kinds of workload that
// mode-operator scales. The reconcile and status logic is the same for every
// kind; each Kind supplies the parts that read and write its count.
package workload

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Kind is one kind of workload. Methods that take an object are only passed
// objects that the Kind's NewObject created.
type Kind interface {
	// Name is the kind, for example "Deployment".
	Name() string
	// NewObject returns an empty object of this kind.
	NewObject() client.Object
	// NewList returns an empty list of this kind.
	NewList() client.ObjectList
	// MaxCount is the largest count the kind allows.
	MaxCount() int32
	// Current returns the workload's current count.
	Current(obj client.Object) int32
	// Holds reports whether the workload's count field already holds the
	// value that target is written as.
	Holds(obj client.Object, target int32) bool
	// ApplyConfiguration returns the server-side apply request that sets the
	// count field for target and the given annotations, and nothing else.
	ApplyConfiguration(obj client.Object, target int32, annotations map[string]string) runtime.ApplyConfiguration
	// Progress returns the workload's ready count and whether it has reached
	// its count.
	Progress(obj client.Object) (ready int32, reached bool)
}

// replicasOrDefault returns *replicas, or 1 when it's nil, as the API server
// defaults it.
func replicasOrDefault(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
}
