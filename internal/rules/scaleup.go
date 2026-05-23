package rules

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/ledger"
)

func init() {
	Register(&WaitForScaleUp{})
}

// WaitForScaleUp coordinates pods during node scale-up events.
// It prevents the cluster autoscaler from over-provisioning by controlling
// pod visibility through three-state decision logic:
//
//  1. No capacity anywhere: release (passthrough to trigger autoscaler)
//  2. In-flight node can fit: hold (wait for node to arrive)
//  3. Existing node can fit: release with real node affinity
type WaitForScaleUp struct{}

// Name returns the rule plugin name.
func (r *WaitForScaleUp) Name() string {
	return "WaitForScaleUp"
}

// Evaluate decides whether the gate should be released for the given pod.
func (r *WaitForScaleUp) Evaluate(
	_ context.Context,
	pod *corev1.Pod,
	l *ledger.NodeLedger,
	profile *v1alpha1.PackingProfile,
) (bool, string, error) {
	demand := ExtractDemand(pod, profile.Spec.DemandSource)
	if len(demand) == 0 {
		return true, "", nil
	}

	constraints := extractConstraints(pod)
	nodeName, isInflight, err := l.FindFit(demand, constraints)
	if err != nil {
		// No capacity match. But if inflight nodes exist (Layer 1 signal),
		// hold anyway. A node is coming but we don't have capacity data yet.
		if l.HasInflightSignal(profile.Name + "/") {
			return false, "", nil
		}
		// No capacity, no inflight signal -- release to trigger autoscaler
		return true, "", nil
	}

	if isInflight {
		// In-flight node can fit -- hold gate, reserve capacity
		if err := l.Reserve(nodeName, demand); err != nil {
			// Reserve failed (race with another pod), treat as no fit
			return true, "", nil
		}
		return false, "", nil
	}

	// Existing node can fit -- release with real node affinity
	if err := l.Reserve(nodeName, demand); err != nil {
		return false, "", nil
	}
	return true, nodeName, nil
}
