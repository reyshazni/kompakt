package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/ledger"
	_ "github.com/reyshazni/kompakt/internal/rules" // register BinPackOnInflightCapacity
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

func testProfile() *v1alpha1.PackingProfile {
	return &v1alpha1.PackingProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cpu"},
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:      "ResourceRequest",
				Resources: []string{"cpu"},
			},
			CapacitySource: v1alpha1.CapacitySource{
				Type:      "NodeAllocatable",
				Resources: []string{"cpu"},
			},
			ReadinessSignal: v1alpha1.ReadinessSignal{},
			Rules: []v1alpha1.RuleRef{
				{Name: "BinPackOnInflightCapacity"},
			},
			ReservationTimeout: "3m",
		},
	}
}

func gatedPod(name string, milliCPU int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			Labels:            map[string]string{labelProfile: "test-cpu"},
			CreationTimestamp: metav1.Now(),
		},
		Spec: corev1.PodSpec{
			SchedulingGates: []corev1.PodSchedulingGate{
				{Name: "kompakt.io/awaiting-bin-pack"},
			},
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: *resource.NewMilliQuantity(milliCPU, resource.DecimalSI),
						},
					},
				},
			},
		},
	}
}

func testNode(name string, milliCPU int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: *resource.NewMilliQuantity(milliCPU, resource.DecimalSI),
			},
		},
	}
}

func setupReconciler(objs ...client.Object) (*PackingProfileReconciler, client.Client) {
	fc := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.PackingProfile{}).
		Build()

	l := ledger.New()
	return &PackingProfileReconciler{
		Client:    fc,
		Ledger:    l,
		Detectors: nil,
		Recorder:  record.NewFakeRecorder(10),
	}, fc
}

func TestReconcile_GateReleased_WhenCapacityAvailable(t *testing.T) {
	pod := gatedPod("pod-1", 1000)
	node := testNode("node-1", 4000)
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue, got %v", result.RequeueAfter)
	}

	// Verify gate removed
	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get updated pod: %v", err)
	}
	if hasKompaktGates(updated) {
		t.Fatal("expected gates to be removed")
	}
}

func TestReconcile_GateStays_WhenNoCapacity(t *testing.T) {
	pod := gatedPod("pod-1", 8000)   // needs 8 cores
	node := testNode("node-1", 2000) // only has 2 cores
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != time.Second {
		t.Fatalf("expected requeue after 1s, got %v", result.RequeueAfter)
	}

	// Verify gate still present
	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if !hasKompaktGates(updated) {
		t.Fatal("expected gates to still be present")
	}
}

func TestReconcile_NoOp_WhenNoGates(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	r, _ := setupReconciler(pod)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "plain-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for ungated pod, got %v", result.RequeueAfter)
	}
}

func TestReconcile_StatusUpdated_AfterGateRelease(t *testing.T) {
	pod := gatedPod("pod-1", 1000)
	node := testNode("node-1", 4000)
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PackingProfile{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "test-cpu"}, updated); err != nil {
		t.Fatalf("failed to get profile: %v", err)
	}
	if updated.Status.ActiveGates != 0 {
		t.Fatalf("expected 0 active gates after release, got %d", updated.Status.ActiveGates)
	}
}

func TestReconcile_StatusUpdated_WhileStillGated(t *testing.T) {
	pod := gatedPod("pod-1", 8000)
	node := testNode("node-1", 2000)
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PackingProfile{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "test-cpu"}, updated); err != nil {
		t.Fatalf("failed to get profile: %v", err)
	}
	if updated.Status.ActiveGates != 1 {
		t.Fatalf("expected 1 active gate, got %d", updated.Status.ActiveGates)
	}
}

func TestReconcile_PriorityHigh_ReleasedImmediately(t *testing.T) {
	pod := gatedPod("priority-pod", 1000)
	pod.Annotations = map[string]string{annotationPriority: "high"}
	profile := testProfile()
	// No nodes at all -- but priority=high should release anyway

	r, fc := setupReconciler(pod, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "priority-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for priority pod, got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if hasKompaktGates(updated) {
		t.Fatal("expected gates removed for priority=high pod")
	}
}

func TestReconcile_PodNotFound(t *testing.T) {
	r, _ := setupReconciler()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "deleted-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("expected no error for deleted pod, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for deleted pod, got %v", result.RequeueAfter)
	}
}

func TestReconcile_ProfileNotFound_ReleasesGates(t *testing.T) {
	pod := gatedPod("orphan-pod", 1000)
	// Profile label points to a profile that doesn't exist
	node := testNode("node-1", 4000)

	r, fc := setupReconciler(pod, node)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "orphan-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue, got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if hasKompaktGates(updated) {
		t.Fatal("expected gates released when profile not found")
	}
}

func TestReconcile_Timeout_ReleasesGates(t *testing.T) {
	pod := gatedPod("timeout-pod", 8000)
	// Set creation timestamp to 5 minutes ago (profile timeout is 3m)
	pod.CreationTimestamp = metav1.NewTime(time.Now().Add(-5 * time.Minute))
	node := testNode("node-1", 2000) // insufficient capacity
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "timeout-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for timed-out pod, got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if hasKompaktGates(updated) {
		t.Fatal("expected gates released on timeout")
	}
}

func TestReconcile_NotTimedOut_StaysGated(t *testing.T) {
	pod := gatedPod("fresh-pod", 8000)
	// Created just now -- not timed out
	node := testNode("node-1", 2000) // insufficient capacity
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "fresh-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != time.Second {
		t.Fatalf("expected requeue after 1s, got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if !hasKompaktGates(updated) {
		t.Fatal("expected gates to stay for non-timed-out pod with insufficient capacity")
	}
}

func TestReconcile_NodeAffinityInjected(t *testing.T) {
	pod := gatedPod("affinity-pod", 1000)
	node := testNode("node-1", 4000)
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "affinity-pod", Namespace: "default"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}

	if updated.Spec.Affinity == nil ||
		updated.Spec.Affinity.NodeAffinity == nil ||
		updated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatal("expected node affinity to be injected")
	}

	terms := updated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 {
		t.Fatalf("expected 1 node selector term, got %d", len(terms))
	}
	if terms[0].MatchExpressions[0].Values[0] != "node-1" {
		t.Fatalf("expected affinity to target node-1, got %s", terms[0].MatchExpressions[0].Values[0])
	}
}

func TestReconcile_InflightNode_NoAffinityInjected(t *testing.T) {
	pod := gatedPod("inflight-pod", 1000)
	profile := testProfile()

	r, _ := setupReconciler(pod, profile)
	// Manually add inflight node to ledger (name starts with "inflight-")
	r.Ledger.AddInflightNode("inflight-gpu-pool-0", map[string]int64{"cpu": 4000})

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "inflight-pod", Namespace: "default"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.Pod{}
	if err := r.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}

	if hasKompaktGates(updated) {
		t.Fatal("expected gates released")
	}

	// Should NOT inject affinity for inflight nodes (they don't exist yet)
	if updated.Spec.Affinity != nil {
		t.Fatal("expected no affinity for inflight node target")
	}
}

func TestReconcile_PodWithNoProfileLabel(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "no-label", Namespace: "default"},
		Spec: corev1.PodSpec{
			SchedulingGates: []corev1.PodSchedulingGate{
				{Name: "kompakt.io/awaiting-bin-pack"},
			},
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	r, _ := setupReconciler(pod)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-label", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for pod without profile label, got %v", result.RequeueAfter)
	}
}

func TestReconcile_ThirdPartyGatesPreserved(t *testing.T) {
	pod := gatedPod("mixed-gates", 1000)
	pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates,
		corev1.PodSchedulingGate{Name: "other-system.io/some-gate"},
	)
	node := testNode("node-1", 4000)
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "mixed-gates", Namespace: "default"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}

	// Kompakt gates should be removed
	if hasKompaktGates(updated) {
		t.Fatal("expected kompakt gates removed")
	}
	// Third-party gate should still be present
	if len(updated.Spec.SchedulingGates) != 1 {
		t.Fatalf("expected 1 remaining gate (third-party), got %d", len(updated.Spec.SchedulingGates))
	}
	if updated.Spec.SchedulingGates[0].Name != "other-system.io/some-gate" {
		t.Fatalf("expected third-party gate preserved, got %s", updated.Spec.SchedulingGates[0].Name)
	}
}

func TestIsTimedOut_InvalidDuration(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			CreationTimestamp: metav1.NewTime(time.Now().Add(-4 * time.Minute)),
		},
	}
	profile := &v1alpha1.PackingProfile{
		Spec: v1alpha1.PackingProfileSpec{
			ReservationTimeout: "invalid-duration",
		},
	}
	// Invalid duration falls back to 3m. Pod created 4m ago > 3m -> timed out
	if !isTimedOut(pod, profile) {
		t.Fatal("expected timeout with invalid duration (fallback 3m) and 4m-old pod")
	}
}

func TestIsTimedOut_ZeroTimestamp(t *testing.T) {
	pod := &corev1.Pod{}
	profile := testProfile()
	if isTimedOut(pod, profile) {
		t.Fatal("expected not timed out with zero creation timestamp")
	}
}

func TestHasKompaktGates(t *testing.T) {
	tests := []struct {
		name   string
		gates  []corev1.PodSchedulingGate
		expect bool
	}{
		{"no gates", nil, false},
		{"empty gates", []corev1.PodSchedulingGate{}, false},
		{"kompakt gate", []corev1.PodSchedulingGate{{Name: "kompakt.io/awaiting-bin-pack"}}, true},
		{"other gate only", []corev1.PodSchedulingGate{{Name: "other.io/gate"}}, false},
		{"mixed gates", []corev1.PodSchedulingGate{
			{Name: "other.io/gate"},
			{Name: "kompakt.io/awaiting-image-prepull"},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{SchedulingGates: tt.gates}}
			if got := hasKompaktGates(pod); got != tt.expect {
				t.Fatalf("hasKompaktGates=%v, expected %v", got, tt.expect)
			}
		})
	}
}

func TestReconcile_UnknownRuleName_Skipped(t *testing.T) {
	pod := gatedPod("unknown-rule-pod", 1000)
	node := testNode("node-1", 4000)
	profile := testProfile()
	// Add a rule that doesn't exist in the registry
	profile.Spec.Rules = append(profile.Spec.Rules, v1alpha1.RuleRef{Name: "DoesNotExist"})

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "unknown-rule-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue (unknown rule skipped), got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if hasKompaktGates(updated) {
		t.Fatal("expected gates released (unknown rule should be skipped)")
	}
}
