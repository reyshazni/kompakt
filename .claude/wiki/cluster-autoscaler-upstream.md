# Cluster Autoscaler (upstream)

Used on EKS, GKE, AKS, and self-managed clusters.

## Status ConfigMap

Writes `cluster-autoscaler-status` ConfigMap in `kube-system`. Kompakt's `ClusterAutoscalerDetector` reads this.

Format:
```
Name: pool-cpu-4xlarge
Health: ready=3, cloudProviderTarget=5
```

Pending nodes = `cloudProviderTarget - ready`. Node group name is in the `Name:` field.

## Events (on pods)

**TriggeredScaleUp**:
```
reason: TriggeredScaleUp
source.component: cluster-autoscaler
message: "pod triggered scale-up: [{pool-cpu-4xlarge 3->5 (max: 10)}]"
```

Message format: `pod triggered scale-up: [{nodegroup current->new (max: maximum)}]`

Truncated to 50 pods in the triggering list. `triggeringPodsTotalCount` field has the real count.

**NotTriggerScaleUp**:
```
reason: NotTriggerScaleUp
message: "pod didn't trigger scale-up (it wouldn't fit if a new node is added)"
```

## Scan cycle

Default interval: 10 seconds, configurable. Evaluates pending pods per cycle. Pods arriving in different cycles are NOT batched together. This is the root cause of over-provisioning that Kompakt solves.

## Node template simulation

CA simulates whether pending pods would fit on an upcoming node using a "node template" derived from the node group configuration. This template only contains resources that the cloud provider declares. Extended resources (e.g., `aliyun.com/gpu-mem`) that come from device plugins are NOT in the template. CA assumes 0 for unknown resources.

## Source

- [kubernetes/autoscaler FAQ](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md)
- [kubernetes/autoscaler source: eventing_scale_up_processor_test.go](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/processors/status/eventing_scale_up_processor_test.go)
