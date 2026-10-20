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

package fingerprint_test

import (
	"context"
	"log"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/bitasylum/mode-operator/client/fingerprint"
)

// After power cycling servers through their management interfaces, check
// that each of them really booted again.
func ExampleCompare() {
	ctx := context.Background()
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	client := kubernetes.NewForConfigOrDie(cfg)

	before, err := fingerprint.Take(ctx, client)
	if err != nil {
		log.Fatal(err)
	}
	// ... power cycle the servers, and wait for their Nodes to be Ready ...
	after, err := fingerprint.Take(ctx, client)
	if err != nil {
		log.Fatal(err)
	}
	d := fingerprint.Compare(before, after)
	if len(d.Unchanged) > 0 {
		log.Printf("these nodes didn't reboot: %v", d.Unchanged)
	}
	if len(d.Gone) > 0 {
		log.Printf("these nodes haven't come back: %v", d.Gone)
	}
}
