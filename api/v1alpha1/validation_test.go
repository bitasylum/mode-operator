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

package v1alpha1_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	modev1alpha1 "github.com/Audiobuzz/mode-operator/api/v1alpha1"
)

const (
	// modeManager is the field manager the mode manager must use (design §6.1).
	modeManager = "mode-manager"
	// maintainerTool is a stand-in for a maintainer's kubectl patch.
	maintainerTool = "kubectl-patch"
	// notInDefinition is the CEL message for the write-time check (design §5.2).
	notInDefinition = "spec.mode must be one of the modes in the ClusterModeDefinition"
	// standby is the default mode in the test definitions.
	standby = "standby"
)

var standardModes = []modev1alpha1.ModeName{"normal", "peak", "offpeak", "maintenance", standby}

// expectInvalid asserts that err is a validation rejection whose message
// contains want.
func expectInvalid(err error, want string) {
	GinkgoHelper()
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got: %v", err)
	Expect(err.Error()).To(ContainSubstring(want))
}

// deleteAfter removes obj when the current spec finishes.
func deleteAfter(obj client.Object) {
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
	})
}

// applyAsModeManager writes spec.mode and spec.definition with server-side
// apply, the way the mode manager does: field manager mode-manager, Force.
func applyAsModeManager(name, definition, mode string) error {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(modev1alpha1.SchemeGroupVersion.String())
	u.SetKind("ClusterMode")
	u.SetName(name)
	u.SetLabels(map[string]string{"mode.bitasylum.org/writer": "validation-test"})
	Expect(unstructured.SetNestedField(u.Object, definition, "spec", "definition")).To(Succeed())
	Expect(unstructured.SetNestedField(u.Object, mode, "spec", "mode")).To(Succeed())
	return k8sClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(modeManager), client.ForceOwnership)
}

// patchModeAsMaintainer changes spec.mode with a merge patch, as a maintainer's
// kubectl patch would.
func patchModeAsMaintainer(name, mode string) error {
	cm := &modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: name}}
	patch := fmt.Appendf(nil, `{"spec":{"mode":%q}}`, mode)
	return k8sClient.Patch(ctx, cm, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(maintainerTool))
}

// setStatusModes writes status.modes through the status subresource, standing
// in for mode-operator copying the definition's modes.
func setStatusModes(name string, modes ...modev1alpha1.ModeName) error {
	cm := &modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: name}}
	quoted := make([]string, 0, len(modes))
	for _, m := range modes {
		quoted = append(quoted, fmt.Sprintf("%q", m))
	}
	patch := fmt.Appendf(nil, `{"status":{"modes":[%s]}}`, strings.Join(quoted, ","))
	return k8sClient.Status().Patch(ctx, cm, client.RawPatch(types.MergePatchType, patch))
}

func getMode(name string) modev1alpha1.ModeName {
	GinkgoHelper()
	cm := &modev1alpha1.ClusterMode{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, cm)).To(Succeed())
	return cm.Spec.Mode
}

var _ = Describe("ClusterModeDefinition validation", func() {
	create := func(name string, modes []modev1alpha1.ModeName, defaultMode modev1alpha1.ModeName) error {
		def := &modev1alpha1.ClusterModeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       modev1alpha1.ClusterModeDefinitionSpec{Modes: modes, DefaultMode: defaultMode},
		}
		deleteAfter(def)
		return k8sClient.Create(ctx, def)
	}

	It("accepts a valid definition", func() {
		Expect(create("standard", standardModes, standby)).To(Succeed())
	})

	It("accepts 32 modes, the most allowed", func() {
		modes := make([]modev1alpha1.ModeName, 32)
		for i := range modes {
			modes[i] = modev1alpha1.ModeName(fmt.Sprintf("mode-%d", i))
		}
		Expect(create("thirty-two", modes, "mode-0")).To(Succeed())
	})

	DescribeTable("rejects an invalid definition",
		func(modes []modev1alpha1.ModeName, defaultMode modev1alpha1.ModeName, want string) {
			expectInvalid(create("invalid", modes, defaultMode), want)
		},
		Entry("with no modes", []modev1alpha1.ModeName{}, modev1alpha1.ModeName(standby),
			"spec.modes"),
		Entry("with a duplicate mode", []modev1alpha1.ModeName{standby, standby}, modev1alpha1.ModeName(standby),
			"Duplicate value"),
		Entry("with an uppercase mode name", []modev1alpha1.ModeName{"Peak", standby}, modev1alpha1.ModeName(standby),
			"should match"),
		Entry("with a * in a mode name", []modev1alpha1.ModeName{"pe*k", standby}, modev1alpha1.ModeName(standby),
			"should match"),
		Entry("with an = in a mode name", []modev1alpha1.ModeName{"a=b", standby}, modev1alpha1.ModeName(standby),
			"should match"),
		Entry("with a mode name of 64 characters",
			[]modev1alpha1.ModeName{modev1alpha1.ModeName(strings.Repeat("a", 64)), standby}, modev1alpha1.ModeName(standby),
			"Too long"),
		Entry("with 33 modes", thirtyThreeModes(), modev1alpha1.ModeName("mode-0"),
			"Too many"),
		Entry("with a defaultMode that isn't in modes", standardModes, modev1alpha1.ModeName("holiday"),
			"defaultMode must be one of modes"),
	)
})

func thirtyThreeModes() []modev1alpha1.ModeName {
	modes := make([]modev1alpha1.ModeName, 33)
	for i := range modes {
		modes[i] = modev1alpha1.ModeName(fmt.Sprintf("mode-%d", i))
	}
	return modes
}

var _ = Describe("ClusterMode validation", func() {
	// track deletes the named ClusterMode when the current spec finishes.
	track := func(name string) {
		deleteAfter(&modev1alpha1.ClusterMode{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}

	It("lets the mode manager create a ClusterMode and change its mode with server-side apply", func() {
		track("ssa")
		Expect(applyAsModeManager("ssa", "standard", standby)).To(Succeed())
		Expect(applyAsModeManager("ssa", "standard", "peak")).To(Succeed())
		Expect(getMode("ssa")).To(Equal(modev1alpha1.ModeName("peak")))

		cm := &modev1alpha1.ClusterMode{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "ssa"}, cm)).To(Succeed())
		managers := make([]string, 0, len(cm.ManagedFields))
		for _, mf := range cm.ManagedFields {
			managers = append(managers, mf.Manager)
		}
		Expect(managers).To(ContainElement(modeManager))
	})

	It("rejects a malformed mode name", func() {
		track("malformed")
		expectInvalid(applyAsModeManager("malformed", "standard", "Peak"), "should match")
	})

	It("rejects a name longer than 63 characters", func() {
		name := strings.Repeat("a", 64)
		track(name)
		expectInvalid(applyAsModeManager(name, "standard", standby), "63 characters or fewer")
	})

	It("rejects a change to spec.definition", func() {
		track("definition")
		Expect(applyAsModeManager("definition", "standard", standby)).To(Succeed())
		expectInvalid(applyAsModeManager("definition", "other", standby), "definition can't be changed once set")
	})

	Context("before mode-operator has filled status.modes", func() {
		It("accepts any well-formed mode, which is the gap the design documents", func() {
			track("unchecked")
			Expect(applyAsModeManager("unchecked", "standard", "anything")).To(Succeed())
			Expect(applyAsModeManager("unchecked", "standard", "something-else")).To(Succeed())
		})
	})

	Context("once mode-operator has filled status.modes", func() {
		const name = "checked"

		BeforeEach(func() {
			track(name)
			Expect(applyAsModeManager(name, "standard", standby)).To(Succeed())
			Expect(setStatusModes(name, standardModes...)).To(Succeed())
		})

		It("rejects the mode manager setting a mode outside the definition", func() {
			expectInvalid(applyAsModeManager(name, "standard", "holiday"), notInDefinition)
			Expect(getMode(name)).To(Equal(modev1alpha1.ModeName(standby)))
		})

		It("rejects a maintainer setting a mode outside the definition", func() {
			expectInvalid(patchModeAsMaintainer(name, "holiday"), notInDefinition)
			Expect(getMode(name)).To(Equal(modev1alpha1.ModeName(standby)))
		})

		It("accepts a mode inside the definition from the mode manager and from a maintainer", func() {
			Expect(applyAsModeManager(name, "standard", "peak")).To(Succeed())
			Expect(patchModeAsMaintainer(name, "maintenance")).To(Succeed())
			Expect(getMode(name)).To(Equal(modev1alpha1.ModeName("maintenance")))
		})

		It("accepts a status update that drops the current mode", func() {
			Expect(setStatusModes(name, "normal", "peak")).To(Succeed())
		})

		It("still lets the mode move to an allowed mode after the current one was dropped", func() {
			Expect(setStatusModes(name, "normal", "peak")).To(Succeed())
			Expect(applyAsModeManager(name, "standard", "normal")).To(Succeed())
			expectInvalid(applyAsModeManager(name, "standard", standby), notInDefinition)
		})
	})
})
