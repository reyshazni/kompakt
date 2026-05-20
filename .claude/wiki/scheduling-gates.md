# Scheduling Gates

Kubernetes feature that allows external controllers to hold pods from being scheduled.

## Timeline

- v1.26: alpha (feature gate `PodSchedulingReadiness`)
- v1.27: beta (enabled by default)
- v1.30: GA (April 2024, feature gate removed)

Kompakt requires K8s >= 1.30.

## How it works

A pod with `spec.schedulingGates` is in `SchedulingGated` phase. The scheduler sees it but skips it entirely. The pod does NOT appear as Pending/Unschedulable.

This is the key property Kompakt exploits: a gated pod is invisible to the cluster autoscaler. The autoscaler only reacts to Pending pods. Gated pods don't trigger scale-up.

## Interaction with autoscaler

- Gated pod: autoscaler does NOT see it, does NOT trigger scale-up
- Gate removed, pod becomes Pending: autoscaler sees it, may trigger scale-up
- This is how WaitForScaleUp passthrough works: removing the gate makes the pod visible, triggering the autoscaler

## Multiple gates

A pod can have multiple gates from different controllers. Each controller manages its own gate. Pod schedules only when ALL gates are removed.

Kompakt uses this: BinPackOnInflightCapacity manages `kompakt.io/awaiting-bin-pack`, WaitForScaleUp manages `kompakt.io/awaiting-scale-up`. Both must release for the pod to schedule.

## Immutability

`spec.schedulingGates` can only be modified (reduced) after creation, not added to. The webhook must inject all gates at admission time. The controller can only remove gates, not add new ones.

## Source

- [Pod Scheduling Readiness (official docs)](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/)
- [KEP-3521](https://github.com/kubernetes/enhancements/tree/master/keps/sig-scheduling/3521-pod-scheduling-readiness)
