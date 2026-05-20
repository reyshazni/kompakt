# PackingProfile API Reference

## Overview

| | |
|---|---|
| API Group | `packer.kompakt.io` |
| API Version | `v1alpha1` |
| Kind | `PackingProfile` |
| Scope | Cluster |
| Short name | `pp` |

Pods opt in by setting the label `packer.kompakt.io/packing-profile: <profile-name>`.

## Full example

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: general-cpu-coordination
spec:
  demandSource:
    type: ResourceRequest
    resources: [cpu, memory]
  capacitySource:
    type: NodeAllocatable
    resources: [cpu, memory]
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
    requiredLabels: []
  rules:
    - name: BinPackOnInflightCapacity
  reservationTimeout: 3m
```

## spec

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `demandSource` | [DemandSource](#demandsource) | Yes | | How to extract resource demand from pods |
| `capacitySource` | [CapacitySource](#capacitysource) | Yes | | How to determine node capacity |
| `readinessSignal` | [ReadinessSignal](#readinesssignal) | Yes | | When a node is ready for gated pods |
| `rules` | [][RuleRef](#ruleref) | Yes | | Ordered list of rule plugins (min 1) |
| `reservationTimeout` | string | No | `3m` | Max hold before unconditional gate release |

## DemandSource

| Field | Type | Required | Description |
|---|---|---|---|
| `type` | string | Yes | `ResourceRequest` or `Annotation` |
| `resources` | []string | When type=ResourceRequest | Resource names to sum from container requests (e.g., `[cpu, memory]`) |
| `annotation` | string | When type=Annotation | Pod annotation key holding the demand value |
| `unit` | string | When type=Annotation | Unit of the annotation value (e.g., `MiB`, `cores`) |

### ResourceRequest

Sums container `resources.requests` across all containers in the pod:

```yaml
demandSource:
  type: ResourceRequest
  resources: [cpu, memory]
```

Works with any standard Kubernetes resource name, including extended resources like `nvidia.com/gpu`.

### Annotation

Reads demand from a pod annotation value:

```yaml
demandSource:
  type: Annotation
  annotation: aliyun.com/gpu-mem
  unit: MiB
```

Used for GPU sharing systems (cGPU, HAMi, KAI) that express demand via annotations.

## CapacitySource

| Field | Type | Required | Description |
|---|---|---|---|
| `type` | string | Yes | `NodeAllocatable` or `NodeLabel` |
| `resources` | []string | When type=NodeAllocatable | Resource names to read from node allocatable |
| `label` | string | When type=NodeLabel | Node label key holding total capacity |
| `perDeviceCount` | [LabelRef](#labelref) | No | Node label indicating device count (for fractional GPU) |
| `nodeGroupTemplates` | [][NodeGroupTemplate](#nodegrouptemplate) | No | Expected allocatable for in-flight nodes by group name prefix |

### NodeAllocatable

Reads from `node.status.allocatable`:

```yaml
capacitySource:
  type: NodeAllocatable
  resources: [cpu, memory]
```

### NodeLabel

Reads capacity from a node label. `perDeviceCount` tells Kompakt how many devices exist to calculate total capacity:

```yaml
capacitySource:
  type: NodeLabel
  label: aliyun.accelerator/gpu-memory-mib
  perDeviceCount:
    label: aliyun.accelerator/gpu-count
```

## NodeGroupTemplate

| Field | Type | Required | Description |
|---|---|---|---|
| `namePrefix` | string | Yes | Node group name prefix to match against in-flight node names |
| `allocatable` | map[string]int64 | Yes | Expected allocatable resources in millivalue |

Used by the `WaitForScaleUp` rule to populate capacity on in-flight nodes detected from the cluster autoscaler. Without templates, in-flight nodes have unknown capacity.

```yaml
capacitySource:
  type: NodeAllocatable
  resources: [cpu, memory]
  nodeGroupTemplates:
    - namePrefix: pool-gpu
      allocatable:
        cpu: 16000
        memory: 64000000000
```

## LabelRef

| Field | Type | Required | Description |
|---|---|---|---|
| `label` | string | Yes | Node label key |

## ReadinessSignal

| Field | Type | Required | Description |
|---|---|---|---|
| `nodeConditions` | [][NodeConditionRequirement](#nodeconditionrequirement) | No | Node conditions that must be true |
| `requiredLabels` | []string | No | Node labels that must be present |

```yaml
readinessSignal:
  nodeConditions:
    - type: Ready
      status: "True"
  requiredLabels:
    - aliyun.accelerator/gpu-count
```

## NodeConditionRequirement

| Field | Type | Required | Description |
|---|---|---|---|
| `type` | string | Yes | Node condition type (e.g., `Ready`) |
| `status` | string | Yes | Required status (e.g., `True`) |

## RuleRef

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | Yes | Rule plugin name |

Allowed values:

| Name | Version | Gate name |
|---|---|---|
| `BinPackOnInflightCapacity` | v0.1 | `kompakt.io/awaiting-bin-pack` |
| `WaitForScaleUp` | v0.1 | `kompakt.io/awaiting-scale-up` |

Planned for future releases: `WaitForImagePrePull` (v0.2), `WaitForMIGProfile` (v0.3), `WaitForCoLocation` (v0.3).

## status

| Field | Type | Description |
|---|---|---|
| `activeGates` | int32 | Number of pods currently gated by this profile |
| `activeReservations` | int32 | Number of capacity reservations currently held |
| `conditions` | []metav1.Condition | Standard Kubernetes conditions |

## Pod labels and annotations

| Key | Type | Description |
|---|---|---|
| `packer.kompakt.io/packing-profile` | Label | References a PackingProfile by name. Required for opt-in. |
| `kompakt.io/exclude` | Label | Set to `true` to skip gating entirely |
| `kompakt.io/priority` | Annotation | Set to `high` to release gate immediately |

## kubectl examples

```bash
# List all profiles
kubectl get packingprofiles

# Describe a profile
kubectl describe packingprofile general-cpu-coordination

# Get profiles with active gates
kubectl get packingprofiles -o custom-columns=\
NAME:.metadata.name,\
GATES:.status.activeGates,\
RESERVATIONS:.status.activeReservations

# Find all gated pods
kubectl get pods --all-namespaces -o json | \
  jq -r '.items[] | select(.spec.schedulingGates) | "\(.metadata.namespace)/\(.metadata.name)"'
```
