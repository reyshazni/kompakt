package rules

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/ledger"
)

func init() {
	Register(&BinPackOnInflightCapacity{})
}

var _ Rule = (*BinPackOnInflightCapacity)(nil)

// BinPackOnInflightCapacity is the core rule for v0.1.
// It extracts demand from the pod, finds a node with sufficient capacity
// (existing or in-flight), reserves the capacity, and releases the gate.
type BinPackOnInflightCapacity struct{}

// Name returns the rule plugin name.
func (r *BinPackOnInflightCapacity) Name() string {
	return "BinPackOnInflightCapacity"
}

// Evaluate checks if the pod can be placed on existing node capacity.
// Only considers nodes that are already Ready, not in-flight nodes.
func (r *BinPackOnInflightCapacity) Evaluate(
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
	nodeName, err := l.FindFitExisting(demand, constraints)
	if err != nil {
		return false, "", nil
	}

	if reserveErr := l.Reserve(nodeName, demand); reserveErr != nil {
		return false, "", nil
	}

	return true, nodeName, nil
}
