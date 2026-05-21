package inflight

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InflightNode represents a node being provisioned by the autoscaler.
type InflightNode struct {
	Name         string
	Allocatable  map[string]int64
	Labels       map[string]string
	InstanceType string
	DetectedAt   time.Time
}

// Detector detects in-flight nodes from cloud-specific signals.
type Detector interface {
	Name() string
	Detect(ctx context.Context, reader client.Reader) ([]InflightNode, error)
}

// ClusterAutoscalerDetector reads the cluster-autoscaler-status ConfigMap
// to detect pending scale-up events.
type ClusterAutoscalerDetector struct{}

// Name returns the detector name.
func (d *ClusterAutoscalerDetector) Name() string {
	return "cluster-autoscaler"
}

// Detect reads the CA status ConfigMap and extracts pending nodes.
// The ConfigMap status field contains lines like:
//
//	ScaleUp: node-group-name (ready=3, cloudProviderTarget=5)
//
// We parse these to determine how many nodes are pending (target - ready).
func (d *ClusterAutoscalerDetector) Detect(ctx context.Context, reader client.Reader) ([]InflightNode, error) {
	cm := &corev1.ConfigMap{}
	err := reader.Get(ctx, types.NamespacedName{
		Name:      "cluster-autoscaler-status",
		Namespace: "kube-system",
	}, cm)
	if err != nil {
		// Missing ConfigMap is not an error, just means no CA or no pending nodes
		return nil, nil //nolint:nilerr // missing CM = no inflight nodes
	}

	status, ok := cm.Data["status"]
	if !ok {
		return nil, nil
	}

	return parseCAStatus(status), nil
}

// parseCAStatus extracts pending nodes from the CA status string.
// Format per node group line:
//
//	Name: <group>
//	Health: ready=N, cloudProviderTarget=M
//
// Pending count = cloudProviderTarget - ready.
func parseCAStatus(status string) []InflightNode {
	var nodes []InflightNode
	lines := strings.Split(status, "\n")

	var currentGroup string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Name:") {
			currentGroup = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
			continue
		}
		if strings.HasPrefix(line, "Health:") && currentGroup != "" {
			ready, target := parseHealthLine(line)
			pending := target - ready
			for i := 0; i < pending; i++ {
				nodes = append(nodes, InflightNode{
					Name:        fmt.Sprintf("%s-pending-%d", currentGroup, i),
					Allocatable: map[string]int64{}, // unknown until node arrives
				})
			}
			currentGroup = ""
		}
	}
	return nodes
}

func parseHealthLine(line string) (ready, target int) {
	// "Health: ready=3, cloudProviderTarget=5"
	parts := strings.Split(line, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.Contains(p, "ready=") {
			val := extractValue(p, "ready=")
			ready, _ = strconv.Atoi(val)
		}
		if strings.Contains(p, "cloudProviderTarget=") {
			val := extractValue(p, "cloudProviderTarget=")
			target, _ = strconv.Atoi(val)
		}
	}
	return
}

func extractValue(s, prefix string) string {
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(s[idx+len(prefix):])
}
