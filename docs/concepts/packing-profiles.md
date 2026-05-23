# PackingProfiles

*How do you tell Kompakt what to coordinate?*

A `PackingProfile` is a cluster-scoped custom resource that defines how Kompakt coordinates a class of workloads. Pods opt in by label:

```yaml
labels:
  packer.kompakt.io/packing-profile: <profile-name>
```

If the referenced profile does not exist, pod creation is rejected.

## Structure

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: my-profile
spec:
  demandSource: {}
  capacitySource: {}
  readinessSignal: {}
  rules: []
  reservationTimeout: 3m
```

A PackingProfile answers three questions: What does this pod need? (`demandSource`) What can nodes provide? (`capacitySource`) When is a node ready to accept pods? (`readinessSignal`)

## Demand source

Extracts resource demand from container resource requests or a pod annotation.

```yaml
demandSource: { type: ResourceRequest, resources: [cpu, memory] }
demandSource: { type: Annotation, annotation: aliyun.com/gpu-mem, unit: MiB }
```

## Capacity source

Determines node capacity from `node.status.allocatable` or a node label.

```yaml
capacitySource: { type: NodeAllocatable, resources: [cpu, memory] }
capacitySource: { type: NodeLabel, label: aliyun.accelerator/gpu-memory-mib,
  perDeviceCount: { label: aliyun.accelerator/gpu-count } }
```

## Readiness signal

Gates are not released until all specified node conditions and required labels are present.

```yaml
readinessSignal:
  nodeConditions: [{ type: Ready, status: "True" }]
  requiredLabels: [aliyun.accelerator/gpu-count]
```

## Rules

Ordered [rule plugins](rule-plugins.md) that decide whether to release a pod's gate.

```yaml
rules: [{ name: WaitForWorkloadPacking }, { name: WaitForNodeReady }]
```

## Reservation timeout

Maximum hold time before unconditional release (recommended: `3m` for CPU, `5m` for GPU).

## Multiple profiles

Create multiple profiles for different workload classes. Each pod references exactly one profile by label. See the [CPU/Memory](../guides/cpu-memory-packing.md) and [GPU](../guides/gpu-packing.md) guides for examples.

## Checking profile status

Run `kubectl get packingprofiles`. The `ACTIVE GATES` column shows how many pods are currently gated by each profile. For the complete field reference, see [API Reference](../reference/api-reference.md).

To understand how these fields drive gating decisions, see [Rule Plugins](rule-plugins.md). For the complete field reference, see [API Reference](../reference/api-reference.md).
