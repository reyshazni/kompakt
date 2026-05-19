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
