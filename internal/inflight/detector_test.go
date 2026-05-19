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
