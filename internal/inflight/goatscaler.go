package inflight

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	goatscalerComponent     = "GOATScaler"
	provisionNodeReason     = "ProvisionNode"
	notTriggerScaleUpReason = "NotTriggerScaleUp"
	noStockSignal           = "NodePool NoStock"
	// DefaultEventMaxAge is the max age for GOATScaler events to be considered.
	// GPU nodes can take 5+ minutes to provision, and under ECS stock shortage
	// this can extend further. 15 minutes covers most cases.
	DefaultEventMaxAge = 15 * time.Minute
)

var provisionNodeRegex = regexp.MustCompile(
	`Provision node (\S+) in Zone: (\S+) with InstanceType: (\S+), Triggered time (.+)`,
)

var _ Detector = (*GOATScalerDetector)(nil)

// GOATScalerDetector detects in-flight nodes from ACK GOATScaler ProvisionNode
// pod events. This is the earliest signal that a scale-up is happening on ACK,
// firing before the ECS API call and before the Node object exists.
type GOATScalerDetector struct {
	// EventMaxAge overrides the max age for events. Zero means use DefaultEventMaxAge.
	EventMaxAge time.Duration
	// NoStockDetected is set to true after Detect() if any recent NotTriggerScaleUp
	// events with "NodePool NoStock" were found. Used by the controller for logging.
	NoStockDetected bool
}

// Name returns the detector name.
func (d *GOATScalerDetector) Name() string {
	return "goatscaler"
}

// Detect lists ProvisionNode events from GOATScaler and parses node info.
func (d *GOATScalerDetector) Detect(ctx context.Context, reader client.Reader) ([]InflightNode, error) {
	eventList := &corev1.EventList{}
	if err := reader.List(ctx, eventList, client.MatchingFields{
		"reason": provisionNodeReason,
	}); err != nil {
		// Field selector might not be supported on uncached reader.
		// Fall back to unfiltered list.
		eventList = &corev1.EventList{}
		if err := reader.List(ctx, eventList); err != nil {
			return nil, nil //nolint:nilerr // can't list events = no inflight nodes
		}
	}

	maxAge := d.EventMaxAge
	if maxAge == 0 {
		maxAge = DefaultEventMaxAge
	}
	cutoff := time.Now().Add(-maxAge)
	seen := make(map[string]bool)
	var nodes []InflightNode
	d.NoStockDetected = false

	for i := range eventList.Items {
		ev := &eventList.Items[i]

		if ev.Source.Component != goatscalerComponent {
			continue
		}

		evTime := ev.LastTimestamp.Time
		if evTime.IsZero() {
			evTime = ev.CreationTimestamp.Time
		}
		if evTime.Before(cutoff) {
			continue
		}

		// Track NoStock signals
		if ev.Reason == notTriggerScaleUpReason && strings.Contains(ev.Message, noStockSignal) {
			d.NoStockDetected = true
			continue
		}

		if ev.Reason != provisionNodeReason {
			continue
		}

		nodeName, _, instanceType, err := parseProvisionNodeMessage(ev.Message)
		if err != nil {
			continue
		}

		if seen[nodeName] {
			continue
		}
		seen[nodeName] = true

		nodes = append(nodes, InflightNode{
			Name:         nodeName,
			Allocatable:  map[string]int64{},
			InstanceType: instanceType,
			DetectedAt:   evTime,
		})
	}

	return nodes, nil
}

// parseProvisionNodeMessage extracts node name, zone, and instance type from
// a GOATScaler ProvisionNode event message.
func parseProvisionNodeMessage(msg string) (nodeName, zone, instanceType string, err error) {
	matches := provisionNodeRegex.FindStringSubmatch(msg)
	if len(matches) < 4 {
		return "", "", "", fmt.Errorf("message does not match ProvisionNode format")
	}
	return matches[1], matches[2], matches[3], nil
}
