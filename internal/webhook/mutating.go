package webhook

// PodGateInjector is a mutating admission webhook that intercepts pod creation
// and injects scheduling gates based on the packer.kompakt.io/packing-profile label.
//
// Webhook flow:
//  1. Intercept Pod CREATE request
//  2. Check for label packer.kompakt.io/packing-profile on the pod
//  3. If label is absent: allow the pod through, no gating
//  4. If label is present: look up the PackingProfile by name
//  5. If profile exists: inject spec.schedulingGates with the gate names
//     from the profile's configured rules
//  6. If profile does NOT exist: reject the pod with an error message:
//     "PackingProfile "<name>" not found"
//
// This ensures no silent failures. If a pod explicitly opts in to a profile
// that does not exist, the pod creation fails with a clear error, similar to
// referencing a non-existent ConfigMap or Secret.
//
// Failure policy is Ignore in v0.x (webhook outage does not block pod creation).
// Pods that bypass the webhook due to outage will not be gated.
//
// TODO(v0.1): Implement webhook handler.
type PodGateInjector struct{}
