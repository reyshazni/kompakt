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

	// Verify the patch adds schedulingGates
	found := false
	for _, p := range resp.Patches {
		if p.Path == "/spec/schedulingGates" {
			found = true
		}
	}
	if !found {
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
