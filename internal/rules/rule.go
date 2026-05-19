package rules

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
	"github.com/reyshazni/kompakt/internal/ledger"
)

// Rule evaluates a gated pod against cluster state and decides whether
// the scheduling gate should be released.
type Rule interface {
	// Name returns the rule plugin name as referenced in PackingProfile.spec.rules.
	Name() string

	// Evaluate decides whether the gate should be released for the given pod.
	// Returns release=true and the target node name if capacity is available.
	Evaluate(ctx context.Context, pod *corev1.Pod, l *ledger.NodeLedger, profile *v1alpha1.PackingProfile) (release bool, nodeName string, err error)
}

// Registry maps rule names to implementations.
var Registry = map[string]Rule{}

// Register adds a rule to the global registry.
func Register(r Rule) {
	Registry[r.Name()] = r
}
