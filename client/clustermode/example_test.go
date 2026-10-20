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

package clustermode_test

import (
	"context"
	"log"
	"time"

	"k8s.io/client-go/rest"

	"github.com/bitasylum/mode-operator/client/clustermode"
)

func broadcast(mode string) { log.Printf("broadcasting %s", mode) }

// The mode manager offers its users the modes the rules allow, changes the
// mode in as many steps as the transitions need, and adopts changes that
// maintainers and other writers make.
func Example_modeManager() {
	ctx := context.Background()
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	reader, err := clustermode.NewReaderForConfig(cfg, "default")
	if err != nil {
		log.Fatal(err)
	}
	writer := clustermode.NewModeManagerWriter(reader, "standard", "mode-manager.ops")
	// Broadcast every change of the current mode: the writer's own, once
	// written, and those it adopts from others, in the order they were made.
	writer.OnChange(func(c clustermode.Change) {
		if c.Adopted {
			log.Printf("%s changed the mode to %s", c.By, c.Mode)
		}
		broadcast(c.Mode)
	})

	cm, err := reader.Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	rules, err := reader.Rules(ctx, cm)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("from %s, users may choose %v", cm.Mode, rules.Next(cm.Mode))

	// Each step is written, and so broadcast, before the next.
	_, err = writer.MoveTo(ctx, "maintenance", clustermode.MoveOptions{
		StepTimeout: 15 * time.Minute,
		OnStep: func(s clustermode.Step) error {
			log.Printf("step %d of %d: %s to %s, %s", s.N, s.Of, s.From, s.To, s.Phase)
			return nil
		},
	})
	switch clustermode.ReasonOf(err) {
	case "":
	case clustermode.ReasonInterrupted:
		log.Printf("someone else changed the mode: %v", err)
	default:
		log.Printf("can't move to maintenance: %v", err)
	}

	// Take changes made by anyone else as the current mode, without writing
	// them back; OnChange broadcasts them.
	err = reader.Watch(ctx, func(e clustermode.Event) { writer.Observe(e.ClusterMode) })
	log.Print(err)
}

// An orchestration server moves the cluster to a mode with no servers
// needed, waits until every workload has settled there, which means its
// pods have stopped, and only then powers servers off.
func Example_orchestrator() {
	ctx := context.Background()
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	reader, err := clustermode.NewReaderForConfig(cfg, "default")
	if err != nil {
		log.Fatal(err)
	}
	writer := clustermode.NewWriter(reader, "orchestrator")

	if _, err := writer.Set(ctx, "standby"); err != nil {
		log.Fatalf("can't change to standby: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if _, err := reader.WaitSettled(waitCtx, "standby"); err != nil {
		log.Fatalf("standby didn't settle: %v", err)
	}
	log.Print("every workload has settled in standby; powering servers off")
}
