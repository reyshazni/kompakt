package inflight

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newNode(name string, createdAgo, transitionAgo time.Duration, readyStatus corev1.ConditionStatus, labels map[string]string) *corev1.Node {
	now := time.Now()
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(now.Add(-createdAgo)),
			Labels:            labels,
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Status:             readyStatus,
					LastTransitionTime: metav1.NewTime(now.Add(-transitionAgo)),
				},
			},
		},
	}
}

func TestNotReady_NewNodeDetected(t *testing.T) {
	// Created 30s ago, Ready=False since creation. Never been Ready.
	node := newNode("new-gpu-node", 30*time.Second, 30*time.Second, corev1.ConditionFalse, nil)
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(node).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 inflight node, got %d", len(nodes))
	}
	if nodes[0].Name != "new-gpu-node" {
		t.Fatalf("expected new-gpu-node, got %s", nodes[0].Name)
	}
}

func TestNotReady_CrashedNodeIgnored(t *testing.T) {
	// Created 2h ago, was Ready, crashed 5min ago. Large delta.
	node := newNode("crashed-node", 2*time.Hour, 5*time.Minute, corev1.ConditionFalse, nil)
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(node).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 (crashed node ignored), got %d", len(nodes))
	}
}

func TestNotReady_ReadyNodeIgnored(t *testing.T) {
	node := newNode("healthy-node", time.Hour, 30*time.Minute, corev1.ConditionTrue, nil)
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(node).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 (ready node ignored), got %d", len(nodes))
	}
}

func TestNotReady_NoConditionDetected(t *testing.T) {
	// Just registered, no conditions at all.
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "bare-node",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Second)),
		},
		Status: corev1.NodeStatus{},
	}
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(node).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 (no conditions = just registered), got %d", len(nodes))
	}
}

func TestNotReady_AllocatablePreserved(t *testing.T) {
	node := newNode("gpu-node", 20*time.Second, 20*time.Second, corev1.ConditionFalse, nil)
	node.Status.Allocatable = corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(16000, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(64*1024*1024*1024, resource.BinarySI),
	}
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(node).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1, got %d", len(nodes))
	}
	if nodes[0].Allocatable["cpu"] != 16000 {
		t.Fatalf("expected cpu=16000, got %d", nodes[0].Allocatable["cpu"])
	}
}

func TestNotReady_LabelsPreserved(t *testing.T) {
	labels := map[string]string{
		"node.kubernetes.io/instance-type": "ecs.gn8is.4xlarge",
		"alibabacloud.com/nodepool-id":     "np123",
	}
	node := newNode("labeled-node", 15*time.Second, 15*time.Second, corev1.ConditionFalse, labels)
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(node).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1, got %d", len(nodes))
	}
	if nodes[0].Labels["node.kubernetes.io/instance-type"] != "ecs.gn8is.4xlarge" {
		t.Fatal("expected instance-type label preserved")
	}
}

func TestNotReady_InstanceTypeFromLabel(t *testing.T) {
	labels := map[string]string{
		"node.kubernetes.io/instance-type": "ecs.gn8is.4xlarge",
	}
	node := newNode("gpu-node", 10*time.Second, 10*time.Second, corev1.ConditionFalse, labels)
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(node).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1, got %d", len(nodes))
	}
	if nodes[0].InstanceType != "ecs.gn8is.4xlarge" {
		t.Fatalf("expected InstanceType from label, got %q", nodes[0].InstanceType)
	}
}

func TestNotReady_MixedNodes(t *testing.T) {
	newN := newNode("new-node", 10*time.Second, 10*time.Second, corev1.ConditionFalse, nil)
	crashedN := newNode("crashed-node", 24*time.Hour, 10*time.Minute, corev1.ConditionFalse, nil)
	healthyN := newNode("healthy-node", time.Hour, 30*time.Minute, corev1.ConditionTrue, nil)
	fc := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(newN, crashedN, healthyN).Build()
	d := &NotReadyNodeDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 (only new), got %d", len(nodes))
	}
	if nodes[0].Name != "new-node" {
		t.Fatalf("expected new-node, got %s", nodes[0].Name)
	}
}

func TestNotReady_Name(t *testing.T) {
	d := &NotReadyNodeDetector{}
	if d.Name() != "not-ready-nodes" {
		t.Fatalf("expected 'not-ready-nodes', got %s", d.Name())
	}
}
