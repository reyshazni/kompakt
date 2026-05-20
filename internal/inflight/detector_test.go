package inflight

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return s
}

func TestDetect_PendingNodes(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"status": `Name: pool-cpu-4xlarge
Health: ready=3, cloudProviderTarget=5
Name: pool-gpu
Health: ready=1, cloudProviderTarget=1`,
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// pool-cpu-4xlarge: 5-3 = 2 pending, pool-gpu: 1-1 = 0 pending
	if len(nodes) != 2 {
		t.Fatalf("expected 2 inflight nodes, got %d", len(nodes))
	}
}

func TestDetect_MissingConfigMap(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 inflight nodes, got %d", len(nodes))
	}
}

func TestDetect_EmptyStatus(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 inflight nodes, got %d", len(nodes))
	}
}

func TestDetect_PendingNodes_AllocatableIsEmpty(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"status": "Name: pool-gpu\nHealth: ready=0, cloudProviderTarget=2",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 inflight nodes, got %d", len(nodes))
	}
	// BUG: Allocatable is empty, making these nodes useless for bin-packing.
	// The ledger's FindFit checks available(res) which returns 0 for all
	// resources when allocatable is empty. No pod will ever fit on these nodes.
	for i, n := range nodes {
		if len(n.Allocatable) != 0 {
			t.Fatalf("node %d: expected empty allocatable (known limitation), got %v", i, n.Allocatable)
		}
	}
}

func TestDetect_NodeNameFormat(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"status": "Name: my-pool\nHealth: ready=1, cloudProviderTarget=3",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Name != "my-pool-pending-0" {
		t.Fatalf("expected my-pool-pending-0, got %s", nodes[0].Name)
	}
	if nodes[1].Name != "my-pool-pending-1" {
		t.Fatalf("expected my-pool-pending-1, got %s", nodes[1].Name)
	}
}

func TestDetect_NegativePending_ScaleDown(t *testing.T) {
	// ready > target means scale-down in progress, should produce 0 pending
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"status": "Name: shrinking-pool\nHealth: ready=5, cloudProviderTarget=3",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 inflight nodes for scale-down, got %d", len(nodes))
	}
}

func TestDetect_MalformedHealthLine(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"status": "Name: broken-pool\nHealth: garbage data here",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Malformed line should parse as 0-0=0 pending, not crash
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes from malformed input, got %d", len(nodes))
	}
}

func TestDetect_MultipleGroups_MixedPending(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"status": `Name: pool-a
Health: ready=2, cloudProviderTarget=4
Name: pool-b
Health: ready=3, cloudProviderTarget=3
Name: pool-c
Health: ready=0, cloudProviderTarget=1`,
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// pool-a: 2 pending, pool-b: 0 pending, pool-c: 1 pending = 3 total
	if len(nodes) != 3 {
		t.Fatalf("expected 3 inflight nodes, got %d", len(nodes))
	}
}

func TestDetectorName(t *testing.T) {
	d := &ClusterAutoscalerDetector{}
	if d.Name() != "cluster-autoscaler" {
		t.Fatalf("expected 'cluster-autoscaler', got %s", d.Name())
	}
}

func TestDetect_StatusKeyMissing(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"other-key": "some value",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes when 'status' key missing, got %d", len(nodes))
	}
}

func TestDetect_HealthWithoutName(t *testing.T) {
	// Health line with no preceding Name line should be ignored
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-autoscaler-status",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"status": "Health: ready=0, cloudProviderTarget=5",
		},
	}

	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(cm).Build()
	d := &ClusterAutoscalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// currentGroup is "" so the if condition `currentGroup != ""` skips it
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes for Health without Name, got %d", len(nodes))
	}
}
