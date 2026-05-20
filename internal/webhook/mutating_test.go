package webhook

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/matcher"
)

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

func podToRaw(pod *corev1.Pod) []byte {
	raw, _ := json.Marshal(pod)
	return raw
}

func makeRequest(pod *corev1.Pod) admission.Request {
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object: runtime.RawExtension{Raw: podToRaw(pod)},
		},
	}
}

func testProfile() *v1alpha1.PackingProfile {
	return &v1alpha1.PackingProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "cpu-packing"},
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:      "ResourceRequest",
				Resources: []string{"cpu", "memory"},
			},
			CapacitySource: v1alpha1.CapacitySource{
				Type:      "NodeAllocatable",
				Resources: []string{"cpu", "memory"},
			},
			ReadinessSignal: v1alpha1.ReadinessSignal{},
			Rules: []v1alpha1.RuleRef{
				{Name: "BinPackOnInflightCapacity"},
			},
		},
	}
}

// patchHasPath checks whether any patch operation targets the given path.
func patchHasPath(resp admission.Response, path string) bool {
	for _, p := range resp.Patches {
		if p.Path == path {
			return true
		}
	}
	return false
}

// patchValueForPath extracts the value set by a patch operation at the given path.
func patchValueForPath(resp admission.Response, path string) interface{} {
	for _, p := range resp.Patches {
		if p.Path == path {
			return p.Value
		}
	}
	return nil
}

func TestHandle_NoLabel(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(scheme()).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-pod", Namespace: "default"},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
}

func TestHandle_ProfileNotFound(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(scheme()).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "labeled-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "does-not-exist"},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if resp.Allowed {
		t.Fatal("expected denied for non-existent profile, got allowed")
	}
}

func TestHandle_ProfileFound_GateInjected(t *testing.T) {
	profile := testProfile()
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gated-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "cpu-packing"},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	if len(resp.Patches) == 0 {
		t.Fatal("expected patches for scheduling gates, got none")
	}
	if !patchHasPath(resp, "/spec/schedulingGates") {
		t.Fatal("expected patch for /spec/schedulingGates")
	}
}

func TestHandle_ExcludeLabel(t *testing.T) {
	profile := testProfile()
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "excluded-pod",
			Namespace: "default",
			Labels: map[string]string{
				"packer.kompakt.io/packing-profile": "cpu-packing",
				"kompakt.io/exclude":                "true",
			},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed for excluded pod, got denied: %s", resp.Result.Message)
	}
	if len(resp.Patches) != 0 {
		t.Fatalf("expected no patches for excluded pod, got %d", len(resp.Patches))
	}
}

func TestHandle_PriorityAnnotation(t *testing.T) {
	profile := testProfile()
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "priority-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "cpu-packing"},
			Annotations: map[string]string{
				"kompakt.io/priority": "high",
			},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	// Priority pods still get gated (controller releases immediately)
	if len(resp.Patches) == 0 {
		t.Fatal("expected patches for priority pod (gates still injected)")
	}
}

func TestHandle_PreExistingGatesPreserved(t *testing.T) {
	profile := testProfile()
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pre-gated-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "cpu-packing"},
		},
		Spec: corev1.PodSpec{
			SchedulingGates: []corev1.PodSchedulingGate{
				{Name: "other-system.io/custom-gate"},
			},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}

	// The webhook uses PatchResponseFromRaw which produces a diff between
	// the original and mutated pod. When the pod already has schedulingGates,
	// the diff may use a "replace" on the entire array or individual "add"
	// operations. Collect all gate names from all patch operations that
	// touch schedulingGates.
	allGates := make(map[string]bool)
	for _, p := range resp.Patches {
		if p.Path == "/spec/schedulingGates" {
			gatesJSON, _ := json.Marshal(p.Value)
			var gates []corev1.PodSchedulingGate
			if err := json.Unmarshal(gatesJSON, &gates); err == nil {
				for _, g := range gates {
					allGates[g.Name] = true
				}
			}
		}
		// Individual gate appended via /spec/schedulingGates/- or /spec/schedulingGates/1
		if len(p.Path) > len("/spec/schedulingGates") && p.Path[:len("/spec/schedulingGates")] == "/spec/schedulingGates" {
			gateJSON, _ := json.Marshal(p.Value)
			var gate corev1.PodSchedulingGate
			if err := json.Unmarshal(gateJSON, &gate); err == nil {
				allGates[gate.Name] = true
			}
		}
	}
	// Pre-existing gate is in the original pod, it will be in the patched
	// result even if not in the patch itself. The key assertion is that
	// the kompakt gate was added.
	if !allGates["kompakt.io/awaiting-bin-pack"] {
		t.Fatal("kompakt gate should be injected")
	}
	// Also verify that the patch preserves existing gates by checking
	// the full schedulingGates array in the patch value (if it replaces
	// the whole array) includes both.
	if patchHasPath(resp, "/spec/schedulingGates") {
		if !allGates["other-system.io/custom-gate"] {
			t.Fatal("pre-existing gate should be preserved in full array replacement")
		}
	}
}

func TestHandle_TraceIDInjected(t *testing.T) {
	profile := testProfile()
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "traced-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "cpu-packing"},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}

	// Check that an annotation patch was added for the trace ID
	traceIDPath := "/metadata/annotations/kompakt.io~1trace-id"
	traceVal := patchValueForPath(resp, traceIDPath)
	if traceVal == nil {
		// The annotation might be set via a replace on the whole annotations map
		// Check for /metadata/annotations path
		annsVal := patchValueForPath(resp, "/metadata/annotations")
		if annsVal == nil {
			t.Fatal("expected trace ID annotation patch")
		}
		annsJSON, _ := json.Marshal(annsVal)
		var anns map[string]string
		if err := json.Unmarshal(annsJSON, &anns); err != nil {
			t.Fatalf("unmarshal annotations: %v", err)
		}
		traceID, ok := anns["kompakt.io/trace-id"]
		if !ok {
			t.Fatal("expected kompakt.io/trace-id in annotations patch")
		}
		if len(traceID) != 8 {
			t.Fatalf("expected 8-char trace ID, got %q (len=%d)", traceID, len(traceID))
		}
	} else {
		traceID, ok := traceVal.(string)
		if !ok {
			t.Fatalf("expected string trace ID, got %T", traceVal)
		}
		if len(traceID) != 8 {
			t.Fatalf("expected 8-char trace ID, got %q (len=%d)", traceID, len(traceID))
		}
	}
}

func TestHandle_EmptyProfileLabel(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(scheme()).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty-label-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": ""},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	// Empty string profile name should be treated as "profile not found" -> denied
	if resp.Allowed {
		t.Fatal("expected denied for empty profile label value")
	}
}

func TestHandle_ProfileWithUnknownRule_Passthrough(t *testing.T) {
	profile := &v1alpha1.PackingProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "unknown-rules"},
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:      "ResourceRequest",
				Resources: []string{"cpu"},
			},
			Rules: []v1alpha1.RuleRef{
				{Name: "DoesNotExist"},
			},
		},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unknown-rule-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "unknown-rules"},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed (passthrough), got denied: %s", resp.Result.Message)
	}
	if len(resp.Patches) != 0 {
		t.Fatalf("expected no patches for unknown rule (no gate mapping), got %d", len(resp.Patches))
	}
}

func TestHandle_MultipleRules_MultipleGates(t *testing.T) {
	profile := &v1alpha1.PackingProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-rule"},
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:      "ResourceRequest",
				Resources: []string{"cpu"},
			},
			Rules: []v1alpha1.RuleRef{
				{Name: "BinPackOnInflightCapacity"},
				{Name: "WaitForImagePrePull"},
			},
		},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "multi-gate-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "multi-rule"},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}

	// Check the schedulingGates patch value contains both gates
	gatesValue := patchValueForPath(resp, "/spec/schedulingGates")
	if gatesValue == nil {
		t.Fatal("expected patch for /spec/schedulingGates")
	}

	gatesJSON, _ := json.Marshal(gatesValue)
	var gates []corev1.PodSchedulingGate
	if err := json.Unmarshal(gatesJSON, &gates); err != nil {
		t.Fatalf("unmarshal gates: %v", err)
	}

	if len(gates) != 2 {
		t.Fatalf("expected 2 gates, got %d", len(gates))
	}

	gateSet := make(map[string]bool)
	for _, g := range gates {
		gateSet[g.Name] = true
	}
	if !gateSet["kompakt.io/awaiting-bin-pack"] {
		t.Fatal("expected awaiting-bin-pack gate")
	}
	if !gateSet["kompakt.io/awaiting-image-prepull"] {
		t.Fatal("expected awaiting-image-prepull gate")
	}
}

func TestHandle_MalformedPodBody(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(scheme()).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object: runtime.RawExtension{Raw: []byte("not-json")},
		},
	}

	resp := injector.Handle(context.Background(), req)
	if resp.Allowed {
		t.Fatal("expected denied for malformed pod body")
	}
}

func TestHandle_ExcludeLabel_NonTrueValue(t *testing.T) {
	profile := testProfile()
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "not-excluded-pod",
			Namespace: "default",
			Labels: map[string]string{
				"packer.kompakt.io/packing-profile": "cpu-packing",
				"kompakt.io/exclude":                "false",
			},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	// exclude=false should NOT exclude, so gates should be injected
	if len(resp.Patches) == 0 {
		t.Fatal("expected gates injected when exclude=false")
	}
}

func TestHandle_WaitForScaleUp_GateInjected(t *testing.T) {
	profile := &v1alpha1.PackingProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "scaleup-profile"},
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:      "ResourceRequest",
				Resources: []string{"cpu"},
			},
			Rules: []v1alpha1.RuleRef{
				{Name: "WaitForScaleUp"},
			},
		},
	}
	fc := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(profile).Build()
	resolver := matcher.NewProfileResolver(fc)
	injector := NewPodGateInjector(resolver)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "scaleup-pod",
			Namespace: "default",
			Labels:    map[string]string{"packer.kompakt.io/packing-profile": "scaleup-profile"},
		},
	}

	resp := injector.Handle(context.Background(), makeRequest(pod))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	if len(resp.Patches) == 0 {
		t.Fatal("expected patches for WaitForScaleUp gate injection")
	}

	// Check that the correct gate name is in the patch
	gatesValue := patchValueForPath(resp, "/spec/schedulingGates")
	if gatesValue == nil {
		t.Fatal("expected patch for /spec/schedulingGates")
	}
	gatesJSON, _ := json.Marshal(gatesValue)
	var gates []corev1.PodSchedulingGate
	if err := json.Unmarshal(gatesJSON, &gates); err != nil {
		t.Fatalf("unmarshal gates: %v", err)
	}
	if len(gates) != 1 {
		t.Fatalf("expected 1 gate, got %d", len(gates))
	}
	if gates[0].Name != "kompakt.io/awaiting-scale-up" {
		t.Fatalf("expected kompakt.io/awaiting-scale-up, got %s", gates[0].Name)
	}
}
