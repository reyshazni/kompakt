package matcher

// ProfileResolver looks up a PackingProfile by name from the informer cache.
// Used by the webhook to validate that a pod's packer.kompakt.io/packing-profile
// label references an existing profile.
//
// TODO(v0.1): Implement resolver backed by controller-runtime client cache.
type ProfileResolver struct{}
