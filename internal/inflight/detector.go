package inflight

// Detector is the interface for cloud-specific in-flight node detection.
// Each implementation reads public signals from the autoscaler to determine
// which nodes are being provisioned but not yet Ready.
//
// Implementations:
//   - ClusterAutoscaler: reads cluster-autoscaler-status ConfigMap
//   - Karpenter: watches NodeClaim resources
//   - AckGoatscaler: TBD (research pending)
//   - GKENAP: watches new NodePools in pending state
type Detector interface {
	// Name returns the detector name for logging and metrics.
	Name() string
}
