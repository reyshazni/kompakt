package inflight

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// neverReadyThreshold is the max delta between creationTimestamp and
	// Ready condition lastTransitionTime for a node to be considered
	// "never been Ready" (newly provisioning).
	neverReadyThreshold = 10 * time.Second

	instanceTypeLabel = "node.kubernetes.io/instance-type"
)

// NotReadyNodeDetector detects nodes that exist in Kubernetes but have never
// been Ready. This is a universal fallback that works on every cloud and
// every autoscaler.
type NotReadyNodeDetector struct{}

// Name returns the detector name.
func (d *NotReadyNodeDetector) Name() string {
	return "not-ready-nodes"
}

// Detect lists nodes and filters for ones that have never been Ready.
func (d *NotReadyNodeDetector) Detect(ctx context.Context, reader client.Reader) ([]InflightNode, error) {
	nodeList := &corev1.NodeList{}
	if err := reader.List(ctx, nodeList); err != nil {
		return nil, nil //nolint:nilerr // can't list nodes = no inflight nodes
	}

	var nodes []InflightNode
	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if !isNeverReady(node) {
			continue
		}

		alloc := make(map[string]int64)
		for res, qty := range node.Status.Allocatable {
			alloc[string(res)] = qty.MilliValue()
		}

		instanceType := ""
		if node.Labels != nil {
			instanceType = node.Labels[instanceTypeLabel]
		}

		labels := make(map[string]string, len(node.Labels))
		for k, v := range node.Labels {
			labels[k] = v
		}

		nodes = append(nodes, InflightNode{
			Name:         node.Name,
			Allocatable:  alloc,
			Labels:       labels,
			InstanceType: instanceType,
		})
	}

	return nodes, nil
}

// isNeverReady returns true if the node has never transitioned to Ready=True.
// Detection: Ready condition's lastTransitionTime is within neverReadyThreshold
// of creationTimestamp, meaning the node was born NotReady and stayed NotReady.
// Also returns true if the node has no Ready condition at all (just registered).
func isNeverReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type != corev1.NodeReady {
			continue
		}
		if cond.Status == corev1.ConditionTrue {
			return false
		}
		// Ready is False or Unknown. Check if it was ever True by comparing
		// lastTransitionTime to creationTimestamp.
		delta := cond.LastTransitionTime.Sub(node.CreationTimestamp.Time)
		if delta < 0 {
			delta = -delta
		}
		return delta <= neverReadyThreshold
	}
	// No Ready condition at all = just registered, never been Ready
	return true
}
