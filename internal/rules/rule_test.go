package rules

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/ledger"
)

func cpuProfile() *v1alpha1.PackingProfile {
	return &v1alpha1.PackingProfile{
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:      "ResourceRequest",
				Resources: []string{"cpu"},
			},
		},
	}
}

func podWithCPU(name string, milliCPU int64) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PodSpec{
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

func TestBinPack_CapacityAvailable(t *testing.T) {
	l := ledger.New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})

	rule := &BinPackOnInflightCapacity{}
	release, nodeName, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true with available capacity")
	}
	if nodeName != "node-1" {
		t.Fatalf("expected node-1, got %s", nodeName)
	}
}

func TestBinPack_NoCapacity(t *testing.T) {
	l := ledger.New()
	l.AddNode("node-1", map[string]int64{"cpu": 500})

	rule := &BinPackOnInflightCapacity{}
	release, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release {
		t.Fatal("expected release=false with no capacity")
	}
}

func TestBinPack_InflightCapacity(t *testing.T) {
	l := ledger.New()
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})

	rule := &BinPackOnInflightCapacity{}
	release, nodeName, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true with inflight capacity")
	}
	if nodeName != "inflight-1" {
		t.Fatalf("expected inflight-1, got %s", nodeName)
	}
}

func TestBinPack_AnnotationDemand(t *testing.T) {
	l := ledger.New()
	l.AddNode("gpu-node", map[string]int64{"aliyun.com/gpu-mem": 16384})

	profile := &v1alpha1.PackingProfile{
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:       "Annotation",
				Annotation: "aliyun.com/gpu-mem",
				Unit:       "MiB",
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "gpu-pod",
			Namespace:   "default",
			Annotations: map[string]string{"aliyun.com/gpu-mem": "4096"},
		},
	}

	rule := &BinPackOnInflightCapacity{}
	release, nodeName, err := rule.Evaluate(context.Background(), pod, l, profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true for annotation demand")
	}
	if nodeName != "gpu-node" {
		t.Fatalf("expected gpu-node, got %s", nodeName)
	}
}

func TestExtractDemand_ResourceRequest(t *testing.T) {
	pod := podWithCPU("test", 2000)
	demand := ExtractDemand(pod, v1alpha1.DemandSource{
		Type:      "ResourceRequest",
		Resources: []string{"cpu"},
	})
	if demand["cpu"] != 2000 {
		t.Fatalf("expected 2000m cpu, got %d", demand["cpu"])
	}
}

func TestExtractDemand_Annotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"gpu-mem": "8192"},
		},
	}
	demand := ExtractDemand(pod, v1alpha1.DemandSource{
		Type:       "Annotation",
		Annotation: "gpu-mem",
	})
	if demand["gpu-mem"] != 8192 {
		t.Fatalf("expected 8192, got %d", demand["gpu-mem"])
	}
}

func TestRegistry_BinPackRegistered(t *testing.T) {
	_, ok := Registry["BinPackOnInflightCapacity"]
	if !ok {
		t.Fatal("BinPackOnInflightCapacity not registered in global registry")
	}
}

func TestBinPack_EmptyDemand_ReleasesImmediately(t *testing.T) {
	l := ledger.New()
	// No nodes at all -- but empty demand means release anyway
	profile := cpuProfile()

	// Pod with no resource requests
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "no-requests", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		},
	}

	rule := &BinPackOnInflightCapacity{}
	release, nodeName, err := rule.Evaluate(context.Background(), pod, l, profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !release {
		t.Fatal("expected release=true for pod with empty demand")
	}
	if nodeName != "" {
		t.Fatalf("expected empty node name for empty demand, got %s", nodeName)
	}
}

func TestBinPack_ReservationSideEffect(t *testing.T) {
	// Evaluate should reserve capacity so subsequent calls see reduced availability
	l := ledger.New()
	l.AddNode("node-1", map[string]int64{"cpu": 3000})

	rule := &BinPackOnInflightCapacity{}
	profile := cpuProfile()

	// First pod: 2000m, should fit
	release1, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 2000), l, profile)
	if err != nil || !release1 {
		t.Fatal("first pod should fit")
	}

	// Second pod: 2000m, should NOT fit (only 1000m left)
	release2, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-2", 2000), l, profile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release2 {
		t.Fatal("second pod should not fit, reservation from first pod should have reduced capacity")
	}
}

func TestBinPack_MultiContainer_SumsDemand(t *testing.T) {
	l := ledger.New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: *resource.NewMilliQuantity(1500, resource.DecimalSI),
						},
					},
				},
				{
					Name: "sidecar",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: *resource.NewMilliQuantity(1500, resource.DecimalSI),
						},
					},
				},
			},
		},
	}

	rule := &BinPackOnInflightCapacity{}
	release, _, err := rule.Evaluate(context.Background(), pod, l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Total demand = 3000m, node has 4000m, should fit
	if !release {
		t.Fatal("expected release=true for multi-container pod with 3000m total on 4000m node")
	}
}

func TestBinPack_MultiContainer_ExceedsCapacity(t *testing.T) {
	l := ledger.New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "big-multi", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: *resource.NewMilliQuantity(2500, resource.DecimalSI),
						},
					},
				},
				{
					Name: "sidecar",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: *resource.NewMilliQuantity(2500, resource.DecimalSI),
						},
					},
				},
			},
		},
	}

	rule := &BinPackOnInflightCapacity{}
	release, _, err := rule.Evaluate(context.Background(), pod, l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Total demand = 5000m, node has 4000m
	if release {
		t.Fatal("expected release=false for multi-container pod exceeding capacity")
	}
}

func TestExtractDemand_AnnotationMissing(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
	}
	demand := ExtractDemand(pod, v1alpha1.DemandSource{
		Type:       "Annotation",
		Annotation: "gpu-mem",
	})
	if demand != nil {
		t.Fatalf("expected nil demand for missing annotation, got %v", demand)
	}
}

func TestExtractDemand_AnnotationInvalidValue(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"gpu-mem": "not-a-number"},
		},
	}
	demand := ExtractDemand(pod, v1alpha1.DemandSource{
		Type:       "Annotation",
		Annotation: "gpu-mem",
	})
	if demand != nil {
		t.Fatalf("expected nil demand for non-numeric annotation, got %v", demand)
	}
}

func TestExtractDemand_AnnotationNilAnnotations(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{},
	}
	demand := ExtractDemand(pod, v1alpha1.DemandSource{
		Type:       "Annotation",
		Annotation: "gpu-mem",
	})
	if demand != nil {
		t.Fatalf("expected nil demand for nil annotations map, got %v", demand)
	}
}

func TestExtractDemand_UnknownType(t *testing.T) {
	pod := podWithCPU("test", 1000)
	demand := ExtractDemand(pod, v1alpha1.DemandSource{
		Type: "UnknownSource",
	})
	if demand != nil {
		t.Fatalf("expected nil demand for unknown type, got %v", demand)
	}
}

func TestExtractDemand_MultipleResources(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-res", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
							corev1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.BinarySI),
						},
					},
				},
			},
		},
	}
	demand := ExtractDemand(pod, v1alpha1.DemandSource{
		Type:      "ResourceRequest",
		Resources: []string{"cpu", "memory"},
	})
	if demand["cpu"] != 2000 {
		t.Fatalf("expected 2000m cpu, got %d", demand["cpu"])
	}
	if demand["memory"] == 0 {
		t.Fatal("expected non-zero memory demand")
	}
}

func TestBinPack_InflightEmptyAllocatable_NoFit(t *testing.T) {
	// Simulates the real-world bug: detector creates inflight nodes with
	// empty allocatable, so bin-packing should not find a fit.
	l := ledger.New()
	l.AddInflightNode("inflight-1", map[string]int64{})

	rule := &BinPackOnInflightCapacity{}
	release, _, err := rule.Evaluate(context.Background(), podWithCPU("pod-1", 1000), l, cpuProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if release {
		t.Fatal("expected release=false for inflight node with empty allocatable")
	}
}

func TestBinPackName(t *testing.T) {
	rule := &BinPackOnInflightCapacity{}
	if rule.Name() != "BinPackOnInflightCapacity" {
		t.Fatalf("expected BinPackOnInflightCapacity, got %s", rule.Name())
	}
}
