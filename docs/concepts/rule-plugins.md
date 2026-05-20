# Rule Plugins

Rule plugins are the decision-making components of Kompakt. Each rule evaluates a gated pod against cluster state and decides whether the gate should be released.

## How rules work

When the controller reconciles a gated pod, it:

1. Reads the matching PackingProfile
2. Executes each rule in the profile's `rules` list, in order
3. Each rule returns a decision: release the gate, or keep it
4. All rules must agree to release before the gate is removed

Rules are configured per PackingProfile:

```yaml
spec:
  rules:
    - name: BinPackOnInflightCapacity
```

## Available rules

### BinPackOnInflightCapacity

**Available since**: v0.1

Evaluates whether the gated pod can be placed on an existing node with available capacity using bin-packing. Only considers nodes that are already Ready in the cluster.

The algorithm:

1. Read the pod's demand from the PackingProfile's `demandSource`
2. Query the [node ledger](node-ledger.md) for existing nodes with available capacity
3. Pick BestFit (smallest sufficient node) to minimize wasted space
4. If a fit is found, reserve the capacity and release the gate with node affinity
5. If no fit exists, keep the gate

This rule handles both CPU/memory and fractional GPU workloads. The demand and capacity sources determine what resources are considered.

**Gate name**: `kompakt.io/awaiting-bin-pack`

### WaitForScaleUp

**Available since**: v0.1

Coordinates pods during node scale-up events. Prevents the cluster autoscaler from over-provisioning by controlling pod visibility.

Three-state decision logic:

1. **No capacity anywhere** (no existing nodes fit, no in-flight nodes): release the gate immediately. The pod becomes visible to the autoscaler and triggers a scale-up. This is the "first mover" -- someone has to signal the autoscaler.
2. **In-flight node can fit**: hold the gate and reserve capacity on the incoming node. The pod stays invisible to the autoscaler, preventing a redundant scale-up. When the node becomes Ready, the controller re-evaluates and releases with real node affinity.
3. **Existing node can fit**: release the gate with node affinity to the real node.

Use `capacitySource.nodeGroupTemplates` to declare expected allocatable resources for each node group. Without templates, in-flight nodes have unknown capacity and pods cannot be matched to them.

```yaml
capacitySource:
  type: NodeAllocatable
  resources: [cpu, memory]
  nodeGroupTemplates:
    - namePrefix: pool-gpu
      allocatable:
        cpu: 16000
        memory: 64000000000
        aliyun.com/gpu-mem: 49152
```

**Gate name**: `kompakt.io/awaiting-scale-up`

### WaitForImagePrePull

**Available since**: v0.2

Holds the gate until the pod's container images are pre-pulled on the target node. Useful for workloads with multi-GB images (common in ML inference) where image pull time dominates startup latency.

The rule coordinates with a pre-pull DaemonSet or Job to ensure images are available before releasing the gate.

**Gate name**: `kompakt.io/awaiting-image-prepull`

### WaitForMIGProfile

**Available since**: v0.3

Holds the gate until the target GPU node's MIG (Multi-Instance GPU) profile matches what the pod needs. MIG reconfiguration requires draining the GPU, changing the profile, and restarting the device plugin. This rule coordinates that process.

**Gate name**: `kompakt.io/awaiting-mig-reconfig`

### WaitForCoLocation

**Available since**: v0.3

Holds the gate until a set of related pods can be placed on the same node or in the same topology zone. Used for workloads that benefit from data locality or low-latency inter-pod communication.

**Gate name**: `kompakt.io/awaiting-colocation`

## Multiple rules

A profile can specify multiple rules. They execute in order, and all must agree to release:

```yaml
spec:
  rules:
    - name: BinPackOnInflightCapacity
    - name: WaitForImagePrePull
```

In this example, the pod stays gated until both capacity is available AND images are pre-pulled. The gate names for each rule are injected independently, so you can see which rule is still holding a pod:

```bash
kubectl get pod my-pod -o jsonpath='{.spec.schedulingGates[*].name}'
# kompakt.io/awaiting-bin-pack kompakt.io/awaiting-image-prepull
```

As each rule is satisfied, its gate is removed. The pod schedules once all gates are gone.

## Gate naming convention

All Kompakt gates use the `kompakt.io/` prefix:

| Gate name | Rule |
|---|---|
| `kompakt.io/awaiting-bin-pack` | BinPackOnInflightCapacity |
| `kompakt.io/awaiting-scale-up` | WaitForScaleUp |
| `kompakt.io/awaiting-image-prepull` | WaitForImagePrePull |
| `kompakt.io/awaiting-mig-reconfig` | WaitForMIGProfile |
| `kompakt.io/awaiting-colocation` | WaitForCoLocation |

This makes it easy to identify which Kompakt rule is holding each pod.
