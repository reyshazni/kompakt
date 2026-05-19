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

The core rule. Evaluates whether the gated pod can be placed on existing or in-flight node capacity using bin-packing.

The algorithm:

1. Read the pod's demand from the PackingProfile's `demandSource`
2. Query the [node ledger](node-ledger.md) for nodes with available capacity
3. Try BestFit (smallest sufficient node) or FirstFit (first sufficient node), depending on the profile configuration
4. If a fit is found, reserve the capacity and release the gate
5. If no fit exists, keep the gate. The autoscaler will provision a new node, and the pod will be evaluated again once the ledger detects the in-flight node.

This rule handles both CPU/memory and fractional GPU workloads. The demand and capacity sources determine what resources are considered.

**Gate name**: `kompakt.io/awaiting-bin-pack`

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
| `kompakt.io/awaiting-image-prepull` | WaitForImagePrePull |
| `kompakt.io/awaiting-mig-reconfig` | WaitForMIGProfile |
| `kompakt.io/awaiting-colocation` | WaitForCoLocation |

This makes it easy to identify which Kompakt rule is holding each pod.
