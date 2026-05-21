package rules

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/reyshazni/kompakt/internal/ledger"
)

func TestWaitForScaleUp_NoCapacity_Passthrough(t *testing.T) {
	// No nodes, no inflight. First pod must pass through to trigger autoscaler.
	l := ledger.New()

	rule := &WaitForScaleUp{}
	release, nodeName, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true (passthrough) when no capacity exists anywhere")
	}
	if nodeName != "" {
		t.Fatalf("expected empty nodeName for passthrough, got %s", nodeName)
	}
}

func TestWaitForScaleUp_InflightFits_Hold(t *testing.T) {
	// Inflight node can fit the pod. Hold the gate to prevent redundant scale-up.
	l := ledger.New()
	l.AddInflightNode("pool-gpu-pending-0", map[string]int64{"cpu": 4000}, nil, nil)

	rule := &WaitForScaleUp{}
	release, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release {
		t.Fatal("expected release=false (hold) when inflight node can fit")
	}
}

func TestWaitForScaleUp_ExistingFits_Release(t *testing.T) {
	// Existing node has capacity. Release with real node name for affinity.
	l := ledger.New()
	l.AddNode("cn-jakarta.172.16.1.10", map[string]int64{"cpu": 4000}, nil, nil)

	rule := &WaitForScaleUp{}
	release, nodeName, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true when existing node has capacity")
	}
	if nodeName != "cn-jakarta.172.16.1.10" {
		t.Fatalf("expected real node name, got %s", nodeName)
	}
}

func TestWaitForScaleUp_EmptyDemand_Release(t *testing.T) {
	// Pod with no resource requests should release immediately.
	l := ledger.New()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "no-requests", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	rule := &WaitForScaleUp{}
	release, nodeName, err := rule.Evaluate(context.Background(), pod, l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true for empty demand")
	}
	if nodeName != "" {
		t.Fatalf("expected empty nodeName, got %s", nodeName)
	}
}

func TestWaitForScaleUp_ReservationOnInflight(t *testing.T) {
	// After holding for an inflight node, capacity should be reserved.
	// Second pod with same demand should see reduced capacity.
	l := ledger.New()
	l.AddInflightNode("pool-gpu-pending-0", map[string]int64{"cpu": 3000}, nil, nil)

	rule := &WaitForScaleUp{}
	profile := cpuProfile()

	// First pod: 2000m, inflight has 3000m. Hold + reserve.
	release1, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 2000), l, profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release1 {
		t.Fatal("expected hold for first pod on inflight")
	}

	// Second pod: 2000m, only 1000m left on inflight. No fit anywhere -> passthrough.
	release2, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-2", 2000), l, profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release2 {
		t.Fatal("expected passthrough for second pod (inflight capacity exhausted)")
	}
}

func TestWaitForScaleUp_ReservationOnExisting(t *testing.T) {
	// Release on existing node should also reserve capacity.
	l := ledger.New()
	l.AddNode("node-1", map[string]int64{"cpu": 3000}, nil, nil)

	rule := &WaitForScaleUp{}
	profile := cpuProfile()

	// First pod: 2000m, existing has 3000m. Release + reserve.
	release1, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 2000), l, profile)
	if err != nil || !release1 {
		t.Fatal("expected release for first pod on existing")
	}

	// Second pod: 2000m, only 1000m left. No fit -> passthrough.
	release2, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-2", 2000), l, profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release2 {
		t.Fatal("expected passthrough for second pod (existing capacity exhausted)")
	}
}

func TestWaitForScaleUp_ExistingPreferredOverInflight(t *testing.T) {
	// When both existing and inflight fit with same slack, prefer existing (release > hold).
	l := ledger.New()
	l.AddNode("existing", map[string]int64{"cpu": 2000}, nil, nil)
	l.AddInflightNode("inflight", map[string]int64{"cpu": 2000}, nil, nil)

	rule := &WaitForScaleUp{}
	release, nodeName, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true when existing node fits (should prefer existing over inflight)")
	}
	if nodeName != "existing" {
		t.Fatalf("expected existing node name, got %s", nodeName)
	}
}

func TestWaitForScaleUp_Registered(t *testing.T) {
	_, ok := Registry["WaitForScaleUp"]
	if !ok {
		t.Fatal("WaitForScaleUp not registered in global registry")
	}
}

func TestWaitForScaleUpName(t *testing.T) {
	rule := &WaitForScaleUp{}
	if rule.Name() != "WaitForScaleUp" {
		t.Fatalf("expected WaitForScaleUp, got %s", rule.Name())
	}
}
