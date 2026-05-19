package rules

// Rule is the interface that all rule plugins must implement.
// A rule evaluates a gated pod against available and in-flight capacity
// and decides whether the gate should be released.
//
// v0.1 ships BinPackOnInflightCapacity.
// v0.2 adds WaitForImagePrePull.
// v0.3 adds WaitForMIGProfile, WaitForCoLocation.
type Rule interface {
	// Name returns the rule plugin name as referenced in PackingProfile.spec.rules.
	Name() string
}
