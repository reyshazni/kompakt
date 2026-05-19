package ledger

// NodeLedger tracks in-flight node capacity: existing nodes, pending
// autoscaler scale-up events, and their available resources.
//
// TODO(v0.1): Implement in-memory ledger.
// TODO(v0.3): Persistent ledger for HA.
type NodeLedger struct{}
