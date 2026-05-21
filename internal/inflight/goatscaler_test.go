package inflight

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func goatscalerEvent(podName, nodeName, zone, instanceType string, age time.Duration) *corev1.Event {
	now := time.Now()
	triggerTime := now.Add(-age)
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("goat-event-%s", podName),
			Namespace: "default",
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       podName,
			Namespace:  "default",
		},
		Reason:        "ProvisionNode",
		Message:       fmt.Sprintf("Provision node %s in Zone: %s with InstanceType: %s, Triggered time %s", nodeName, zone, instanceType, triggerTime.Format("2006-01-02 15:04:05.000")),
		Source:        corev1.EventSource{Component: "GOATScaler"},
		Type:          corev1.EventTypeNormal,
		LastTimestamp: metav1.NewTime(triggerTime),
	}
}

func eventsScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return s
}

func TestGOATScaler_ProvisionNodeDetected(t *testing.T) {
	ev := goatscalerEvent("pod-1", "asa-k1abc123", "ap-southeast-5a", "ecs.gn8is.4xlarge", 30*time.Second)
	fc := fake.NewClientBuilder().WithScheme(eventsScheme()).WithObjects(ev).Build()
	d := &GOATScalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 inflight node, got %d", len(nodes))
	}
	if nodes[0].Name != "asa-k1abc123" {
		t.Fatalf("expected asa-k1abc123, got %s", nodes[0].Name)
	}
	if nodes[0].InstanceType != "ecs.gn8is.4xlarge" {
		t.Fatalf("expected ecs.gn8is.4xlarge, got %s", nodes[0].InstanceType)
	}
}

func TestGOATScaler_ParseMessage(t *testing.T) {
	msg := "Provision node asa-xyz in Zone: cn-jakarta-b with InstanceType: ecs.gn7i-c16g1.4xlarge, Triggered time 2026-05-20 13:31:07.774"
	name, _, instanceType, err := parseProvisionNodeMessage(msg)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if name != "asa-xyz" {
		t.Fatalf("expected asa-xyz, got %s", name)
	}
	if instanceType != "ecs.gn7i-c16g1.4xlarge" {
		t.Fatalf("expected ecs.gn7i-c16g1.4xlarge, got %s", instanceType)
	}
}

func TestGOATScaler_OldEventIgnored(t *testing.T) {
	ev := goatscalerEvent("pod-old", "asa-old", "zone", "type", 20*time.Minute)
	fc := fake.NewClientBuilder().WithScheme(eventsScheme()).WithObjects(ev).Build()
	d := &GOATScalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes (old event), got %d", len(nodes))
	}
}

func TestGOATScaler_NoEvents(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(eventsScheme()).Build()
	d := &GOATScalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestGOATScaler_DeduplicateByName(t *testing.T) {
	// Same node triggered by two different pods
	ev1 := goatscalerEvent("pod-a", "asa-same-node", "zone", "type", 30*time.Second)
	ev2 := goatscalerEvent("pod-b", "asa-same-node", "zone", "type", 20*time.Second)
	fc := fake.NewClientBuilder().WithScheme(eventsScheme()).WithObjects(ev1, ev2).Build()
	d := &GOATScalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node (deduplicated), got %d", len(nodes))
	}
}

func TestGOATScaler_NonProvisionEventIgnored(t *testing.T) {
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "not-trigger", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "v1", Kind: "Pod", Name: "pod-x", Namespace: "default",
		},
		Reason:        "NotTriggerScaleUp",
		Message:       "pod didn't trigger scale-up",
		Source:        corev1.EventSource{Component: "GOATScaler"},
		Type:          corev1.EventTypeNormal,
		LastTimestamp: metav1.NewTime(time.Now()),
	}
	fc := fake.NewClientBuilder().WithScheme(eventsScheme()).WithObjects(ev).Build()
	d := &GOATScalerDetector{}

	nodes, err := d.Detect(context.Background(), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes (wrong reason), got %d", len(nodes))
	}
}

func TestGOATScaler_Name(t *testing.T) {
	d := &GOATScalerDetector{}
	if d.Name() != "goatscaler" {
		t.Fatalf("expected 'goatscaler', got %s", d.Name())
	}
}
