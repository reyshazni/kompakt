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
