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

package startup_test

import (
	"context"
	"log"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/bitasylum/mode-operator/client/clustermode"
	"github.com/bitasylum/mode-operator/client/fingerprint"
	"github.com/bitasylum/mode-operator/client/startup"
)

// The mode manager decides its mode on startup, then keeps the fingerprint
// current while it runs.
func Example() {
	ctx := context.Background()
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	client := kubernetes.NewForConfigOrDie(cfg)
	reader, err := clustermode.NewReaderForConfig(cfg, "default")
	if err != nil {
		log.Fatal(err)
	}
	recorder, stop := startup.NewRecorder(ctx, client, "mode-manager")
	defer stop()
	store := fingerprint.NewStore(client, "mode-manager", fingerprint.DefaultName("default"))

	res, err := startup.Run(ctx, startup.Deps{
		Writer:       clustermode.NewModeManagerWriter(reader, "standard", "mode-manager.ops"),
		Fingerprints: store,
		Nodes:        func(ctx context.Context) (fingerprint.BootIDs, error) { return fingerprint.Take(ctx, client) },
		Recorder:     recorder,
	}, startup.Options{BuiltinDefault: "standby"})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("starting in %s (%s)", res.Decision.Mode, res.Decision.Case)
	if res.Fingerprint == nil {
		log.Print("the CRDs aren't installed; call startup.Persist once they are")
		return
	}

	keeper := fingerprint.NewKeeper(client, fingerprint.KeeperOptions{Saver: store, Base: *res.Fingerprint})
	go func() { _ = keeper.Run(ctx) }()
}
