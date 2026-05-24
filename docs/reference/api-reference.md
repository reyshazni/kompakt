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
  capacitySource:
    type: NodeAllocatable
    resources: [cpu, memory]
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
    requiredLabels: []
  rules:
    - name: WaitForWorkloadPacking
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
| `additionalResources` | []string | No | Extended resource names to track beyond cpu and memory (e.g., `[nvidia.com/gpu]`). CPU and memory are always tracked implicitly. |
| `resources` | []string | No | **Deprecated.** Use `additionalResources` instead. |
| `annotation` | string | When type=Annotation | Pod annotation key holding the demand value |
| `unit` | string | When type=Annotation | Unit of the annotation value (e.g., `MiB`, `cores`) |

### ResourceRequest

Sums container `resources.requests` across all containers in the pod.
CPU and memory are always tracked implicitly. To track extended resources (e.g., `nvidia.com/gpu`), list them in `additionalResources`.

### Annotation

Reads demand from a pod annotation value.
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

Reads from `node.status.allocatable`.

### NodeLabel

Reads capacity from a node label.
`perDeviceCount` tells Kompakt how many devices exist to calculate total capacity.

## NodeGroupTemplate

| Field | Type | Required | Description |
|---|---|---|---|
| `namePrefix` | string | Yes | Node group name prefix to match against in-flight node names |
| `allocatable` | map[string]int64 | Yes | Expected allocatable resources in millivalue |

Used by the `WaitForNodeReady` rule to populate capacity on in-flight nodes detected from the cluster autoscaler. Without templates, in-flight nodes have unknown capacity.

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
| `WaitForWorkloadPacking` | v0.1 | `kompakt.io/wait-for-workload-packing` |
| `WaitForNodeReady` | v0.1 | `kompakt.io/wait-for-node-ready` |

Planned for future releases: `WaitForImagePrePull` (v0.2), `WaitForMIGProfile` (v0.3), `WaitForCoLocation` (v0.3).

## status

| Field | Type | Description |
|---|---|---|
| `activeGates` | int32 | Number of pods currently gated by this profile |
| `inflightNodes` | int32 | Number of in-flight nodes detected |
| `activeDetectors` | []string | Which in-flight detector found nodes (e.g., `["goatscaler"]`) |
| `conditions` | []metav1.Condition | Standard Kubernetes conditions (Ready, ProfileValid, LedgerReady, InflightDetectionActive) |

## Pod labels and annotations

| Key | Type | Description |
|---|---|---|
| `packer.kompakt.io/packing-profile` | Label | References a PackingProfile by name. Required for opt-in. |
| `kompakt.io/exclude` | Label | Set to `true` to skip gating entirely |
| `kompakt.io/priority` | Annotation | Set to `high` to release gate immediately |
| `kompakt.io/trace-id` | Annotation | Set by webhook. 8-char ID for log correlation. |
| `kompakt.io/gate-reason` | Annotation | Set by controller on release. Values: `capacity`, `timeout`, `priority`, `profile_not_found`. |
| `kompakt.io/target-node` | Annotation | Set by controller on release with node affinity. The real node hostname. |
| `kompakt.io/held-by` | Annotation | Set by controller while gated. The rule name currently holding (e.g., `WaitForNodeReady`). Removed on release. |
