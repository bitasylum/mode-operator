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

// Package testenv runs a Kubernetes API server for the package tests, with
// mode-operator's CRDs, admission policy and roles from this repository.
package testenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Env is a running API server.
type Env struct {
	env  *envtest.Environment
	root string
	// Config is an administrator's config.
	Config  *rest.Config
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface

	names atomic.Int64
}

// Options configure [Start].
type Options struct {
	// WithoutCRDs starts the API server without mode-operator's CRDs and
	// admission policy. [Env.InstallCRDs] installs them later.
	WithoutCRDs bool
}

// Start starts an API server. Its binaries come from KUBEBUILDER_ASSETS, or
// from bin/k8s at the repository root, where make setup-envtest puts them.
func Start(o Options) (*Env, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	e := &Env{root: root, env: &envtest.Environment{}}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		e.env.BinaryAssetsDirectory = firstDir(filepath.Join(root, "bin", "k8s"))
	}
	if !o.WithoutCRDs {
		e.env.CRDDirectoryPaths = []string{filepath.Join(root, "config", "crd", "bases")}
		e.env.ErrorIfCRDPathMissing = true
	}
	if e.Config, err = e.env.Start(); err != nil {
		return nil, fmt.Errorf("starting the API server: %w", err)
	}
	if e.Dynamic, err = dynamic.NewForConfig(e.Config); err != nil {
		return nil, err
	}
	if e.Kube, err = kubernetes.NewForConfig(e.Config); err != nil {
		return nil, err
	}
	if !o.WithoutCRDs {
		if err := e.installAdmission(context.Background()); err != nil {
			_ = e.env.Stop()
			return nil, err
		}
	}
	return e, nil
}

// Stop stops the API server.
func (e *Env) Stop() error { return e.env.Stop() }

// InstallCRDs installs mode-operator's CRDs and admission policy, for an Env
// started without them.
func (e *Env) InstallCRDs(ctx context.Context) error {
	if _, err := envtest.InstallCRDs(e.Config, envtest.CRDInstallOptions{
		Paths:              []string{filepath.Join(e.root, "config", "crd", "bases")},
		ErrorIfPathMissing: true,
	}); err != nil {
		return err
	}
	return e.installAdmission(ctx)
}

// UninstallCRDs deletes mode-operator's CRDs, and with them every ClusterMode
// and ClusterModeDefinition.
func (e *Env) UninstallCRDs() error {
	return envtest.UninstallCRDs(e.Config, envtest.CRDInstallOptions{
		Paths: []string{filepath.Join(e.root, "config", "crd", "bases")},
	})
}

// Name returns a name no other call returns, for objects a test creates.
func (e *Env) Name(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, e.names.Add(1))
}

// Path returns a path in the repository, from its root.
func (e *Env) Path(elem ...string) string {
	return filepath.Join(append([]string{e.root}, elem...)...)
}

// repoRoot is the mode-operator repository, three levels above this file.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("finding the repository root")
	}
	return filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func firstDir(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(base, entry.Name())
		}
	}
	return ""
}
