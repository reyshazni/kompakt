package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
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
	"github.com/reyshazni/kompakt/internal/inflight"
	"github.com/reyshazni/kompakt/internal/ledger"
	_ "github.com/reyshazni/kompakt/internal/rules" // register BinPackOnInflightCapacity
)

// fakeDetector returns a fixed list of inflight nodes.
type fakeDetector struct {
	nodes []inflight.InflightNode
}

func (d *fakeDetector) Name() string { return "fake" }
func (d *fakeDetector) Detect(_ context.Context, _ client.Reader) ([]inflight.InflightNode, error) {
	return d.nodes, nil
}

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

func TestReconcile_BinPack_IgnoresInflightNode(t *testing.T) {
	// BinPack only considers existing nodes. With only inflight capacity,
	// the pod should stay gated (BinPack returns hold).
	pod := gatedPod("inflight-pod", 1000)
	profile := testProfile()

	r, fc := setupReconciler(pod, profile)
	r.Ledger.AddInflightNode("inflight-gpu-pool-0", map[string]int64{"cpu": 4000}, nil, nil)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "inflight-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != time.Second {
		t.Fatalf("expected requeue (BinPack ignores inflight), got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if !hasKompaktGates(updated) {
		t.Fatal("expected gates to stay (BinPack does not use inflight nodes)")
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
	nopLogger := logr.Discard()
	if !isTimedOut(pod, profile, nopLogger) {
		t.Fatal("expected timeout with invalid duration (fallback 3m) and 4m-old pod")
	}
}

func TestIsTimedOut_ZeroTimestamp(t *testing.T) {
	pod := &corev1.Pod{}
	profile := testProfile()
	if isTimedOut(pod, profile, logr.Discard()) {
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

func TestReconcile_InflightNodeEnrichedFromTemplate_WaitForScaleUp(t *testing.T) {
	// WaitForScaleUp with enriched inflight node: pod fits on inflight -> hold gate.
	// This verifies template enrichment works with WaitForScaleUp (hold, not release).
	pod := scaleUpGatedPod("gpu-pod", 1000)
	profile := scaleUpProfile()
	profile.Spec.CapacitySource.NodeGroupTemplates = []v1alpha1.NodeGroupTemplate{
		{
			NamePrefix:  "pool-gpu",
			Allocatable: map[string]int64{"cpu": 4000},
		},
	}

	detector := &fakeDetector{
		nodes: []inflight.InflightNode{
			{Name: "pool-gpu-pending-0", Allocatable: map[string]int64{}},
		},
	}

	r, fc := setupReconciler(pod, profile)
	r.Detectors = []inflight.Detector{detector}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "gpu-pod", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// WaitForScaleUp holds when inflight fits
	if result.RequeueAfter != time.Second {
		t.Fatalf("expected requeue (hold on inflight), got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if !hasKompaktGates(updated) {
		t.Fatal("expected gates to stay (WaitForScaleUp holds on inflight)")
	}
}

func TestReconcile_InflightNodeNoMatchingTemplate(t *testing.T) {
	// Detector reports a pending node but profile has no matching template.
	// Allocatable stays empty, pod should NOT fit.
	pod := gatedPod("no-match-pod", 1000)
	profile := testProfile()
	profile.Spec.CapacitySource.NodeGroupTemplates = []v1alpha1.NodeGroupTemplate{
		{
			NamePrefix:  "pool-cpu",
			Allocatable: map[string]int64{"cpu": 4000},
		},
	}

	detector := &fakeDetector{
		nodes: []inflight.InflightNode{
			{Name: "pool-gpu-pending-0", Allocatable: map[string]int64{}},
		},
	}

	r, fc := setupReconciler(pod, profile)
	r.Detectors = []inflight.Detector{detector}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "no-match-pod", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != time.Second {
		t.Fatalf("expected requeue (no matching template), got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if !hasKompaktGates(updated) {
		t.Fatal("expected gates to stay, pod should NOT fit on inflight with empty allocatable")
	}
}

func scaleUpProfile() *v1alpha1.PackingProfile {
	return &v1alpha1.PackingProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "test-scaleup"},
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
				{Name: "WaitForScaleUp"},
			},
			ReservationTimeout: "3m",
		},
	}
}

func scaleUpGatedPod(name string, milliCPU int64) *corev1.Pod {
	pod := gatedPod(name, milliCPU)
	pod.Labels[labelProfile] = "test-scaleup"
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{
		{Name: "kompakt.io/awaiting-scale-up"},
	}
	return pod
}

func TestReconcile_WaitForScaleUp_Passthrough(t *testing.T) {
	// No nodes, no inflight. Pod should be released (passthrough) to trigger autoscaler.
	pod := scaleUpGatedPod("first-pod", 1000)
	profile := scaleUpProfile()

	r, fc := setupReconciler(pod, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "first-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue (passthrough), got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if hasKompaktGates(updated) {
		t.Fatal("expected gates released for passthrough (no capacity anywhere)")
	}
	// No affinity should be set -- pod passes through to autoscaler
	if updated.Spec.Affinity != nil {
		t.Fatal("expected no affinity for passthrough release")
	}
}

func TestReconcile_WaitForScaleUp_HoldOnInflight(t *testing.T) {
	// Inflight node can fit. Pod should stay gated.
	pod := scaleUpGatedPod("second-pod", 1000)
	profile := scaleUpProfile()
	profile.Spec.CapacitySource.NodeGroupTemplates = []v1alpha1.NodeGroupTemplate{
		{NamePrefix: "pool-gpu", Allocatable: map[string]int64{"cpu": 4000}},
	}

	detector := &fakeDetector{
		nodes: []inflight.InflightNode{
			{Name: "pool-gpu-pending-0", Allocatable: map[string]int64{}},
		},
	}

	r, fc := setupReconciler(pod, profile)
	r.Detectors = []inflight.Detector{detector}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "second-pod", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != time.Second {
		t.Fatalf("expected requeue (hold), got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if !hasKompaktGates(updated) {
		t.Fatal("expected gates to stay when inflight node can fit")
	}
}

func TestReconcile_WaitForScaleUp_ReleaseWithAffinity(t *testing.T) {
	// Existing node has capacity. Pod should be released with real node affinity.
	pod := scaleUpGatedPod("ready-pod", 1000)
	node := testNode("cn-jakarta.172.16.1.10", 4000)
	profile := scaleUpProfile()

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "ready-pod", Namespace: "default"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue (release), got %v", result.RequeueAfter)
	}

	updated := &corev1.Pod{}
	if err := fc.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	if hasKompaktGates(updated) {
		t.Fatal("expected gates released when existing node has capacity")
	}
	// Should have affinity to real node
	if updated.Spec.Affinity == nil ||
		updated.Spec.Affinity.NodeAffinity == nil ||
		updated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatal("expected node affinity to real node")
	}
	terms := updated.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if terms[0].MatchExpressions[0].Values[0] != "cn-jakarta.172.16.1.10" {
		t.Fatalf("expected affinity to cn-jakarta.172.16.1.10, got %s", terms[0].MatchExpressions[0].Values[0])
	}
}

// --- Status condition tests ---

func findCondition(profile *v1alpha1.PackingProfile, condType string) *metav1.Condition {
	for i := range profile.Status.Conditions {
		if profile.Status.Conditions[i].Type == condType {
			return &profile.Status.Conditions[i]
		}
	}
	return nil
}

func TestStatus_ProfileValid_True(t *testing.T) {
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

	cond := findCondition(updated, "ProfileValid")
	if cond == nil {
		t.Fatal("expected ProfileValid condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected ProfileValid=True, got %s: %s", cond.Status, cond.Message)
	}
}

func TestStatus_ProfileValid_False_MissingResources(t *testing.T) {
	pod := gatedPod("pod-1", 1000)
	node := testNode("node-1", 4000)
	profile := testProfile()
	profile.Spec.DemandSource.Resources = nil // empty resources with ResourceRequest type

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PackingProfile{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "test-cpu"}, updated); err != nil {
		t.Fatalf("failed to get profile: %v", err)
	}

	cond := findCondition(updated, "ProfileValid")
	if cond == nil {
		t.Fatal("expected ProfileValid condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected ProfileValid=False for empty resources, got %s", cond.Status)
	}
	if cond.Reason != "ConfigurationError" {
		t.Fatalf("expected reason ConfigurationError, got %s", cond.Reason)
	}
}

func TestStatus_ProfileValid_False_WaitForScaleUpNoTemplates(t *testing.T) {
	pod := scaleUpGatedPod("pod-1", 1000)
	profile := scaleUpProfile()
	// WaitForScaleUp but no nodeGroupTemplates

	r, fc := setupReconciler(pod, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PackingProfile{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "test-scaleup"}, updated); err != nil {
		t.Fatalf("failed to get profile: %v", err)
	}

	cond := findCondition(updated, "ProfileValid")
	if cond == nil {
		t.Fatal("expected ProfileValid condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected ProfileValid=False for WaitForScaleUp without templates, got %s", cond.Status)
	}
}

func TestStatus_LedgerReady_True(t *testing.T) {
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

	cond := findCondition(updated, "LedgerReady")
	if cond == nil {
		t.Fatal("expected LedgerReady condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected LedgerReady=True, got %s: %s", cond.Status, cond.Message)
	}
}

func TestStatus_InflightNodes_Count(t *testing.T) {
	pod := scaleUpGatedPod("pod-1", 1000)
	profile := scaleUpProfile()
	profile.Spec.CapacitySource.NodeGroupTemplates = []v1alpha1.NodeGroupTemplate{
		{NamePrefix: "pool-gpu", Allocatable: map[string]int64{"cpu": 4000}},
	}

	detector := &fakeDetector{
		nodes: []inflight.InflightNode{
			{Name: "pool-gpu-pending-0", Allocatable: map[string]int64{}},
			{Name: "pool-gpu-pending-1", Allocatable: map[string]int64{}},
		},
	}

	r, fc := setupReconciler(pod, profile)
	r.Detectors = []inflight.Detector{detector}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PackingProfile{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "test-scaleup"}, updated); err != nil {
		t.Fatalf("failed to get profile: %v", err)
	}

	if updated.Status.InflightNodes != 2 {
		t.Fatalf("expected 2 inflight nodes in status, got %d", updated.Status.InflightNodes)
	}

	cond := findCondition(updated, "InflightDetectionActive")
	if cond == nil {
		t.Fatal("expected InflightDetectionActive condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected InflightDetectionActive=True, got %s", cond.Status)
	}
}

func TestStatus_InflightDetection_NoDetectors(t *testing.T) {
	pod := gatedPod("pod-1", 1000)
	node := testNode("node-1", 4000)
	profile := testProfile()

	r, fc := setupReconciler(pod, node, profile)
	// Detectors is nil by default in setupReconciler
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PackingProfile{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "test-cpu"}, updated); err != nil {
		t.Fatalf("failed to get profile: %v", err)
	}

	cond := findCondition(updated, "InflightDetectionActive")
	if cond == nil {
		t.Fatal("expected InflightDetectionActive condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected InflightDetectionActive=False with no detectors, got %s", cond.Status)
	}
	if cond.Reason != "NoDetectors" {
		t.Fatalf("expected reason NoDetectors, got %s", cond.Reason)
	}
}

func TestStatus_Ready_True(t *testing.T) {
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

	cond := findCondition(updated, "Ready")
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready=True, got %s: %s", cond.Status, cond.Message)
	}
}

func TestStatus_Ready_False_WhenProfileInvalid(t *testing.T) {
	pod := gatedPod("pod-1", 1000)
	node := testNode("node-1", 4000)
	profile := testProfile()
	profile.Spec.DemandSource.Resources = nil // invalid: ResourceRequest with no resources

	r, fc := setupReconciler(pod, node, profile)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pod-1", Namespace: "default"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &v1alpha1.PackingProfile{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "test-cpu"}, updated); err != nil {
		t.Fatalf("failed to get profile: %v", err)
	}

	ready := findCondition(updated, "Ready")
	if ready == nil {
		t.Fatal("expected Ready condition")
	}
	if ready.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False when ProfileValid=False, got %s", ready.Status)
	}
	if ready.Reason != "NotReady" {
		t.Fatalf("expected reason NotReady, got %s", ready.Reason)
	}
}
