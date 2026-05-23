# PackingProfiles

A `PackingProfile` is a cluster-scoped custom resource that defines how Kompakt coordinates a class of workloads. It describes the coordination behavior: how to measure demand, how to measure capacity, which rules to run, and how long to hold reservations.

Pods opt in to a profile by setting a label:

```yaml
metadata:
  labels:
    packer.kompakt.io/packing-profile: <profile-name>
```

If the referenced profile does not exist, pod creation is rejected with a clear error.

## Structure

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: my-profile          # pods reference this name via label
spec:
  demandSource: {}           # how to measure what the pod needs
  capacitySource: {}         # how to measure what nodes can provide
  readinessSignal: {}        # when a node is ready to receive pods
  rules: []                  # which rule plugins to run
  reservationTimeout: 3m     # max hold time before unconditional release
```

## Demand source

Defines how Kompakt extracts resource demand from a matched pod.

### ResourceRequest

Reads from the pod's container resource requests. This is the common case for CPU and memory workloads:

```yaml
demandSource:
  type: ResourceRequest
  resources: [cpu, memory]
```

Kompakt sums the requests across all containers in the pod.

### Annotation

Reads from a pod annotation. Used for GPU sharing systems that express demand via annotations rather than resource requests:

```yaml
demandSource:
  type: Annotation
  annotation: aliyun.com/gpu-mem
  unit: MiB
```

## Capacity source

Defines how Kompakt determines what a node can provide.

### NodeAllocatable

Reads from `node.status.allocatable`. The standard approach for CPU and memory:

```yaml
capacitySource:
  type: NodeAllocatable
  resources: [cpu, memory]
```

### NodeLabel

Reads capacity from a node label. Used for GPU sharing systems where device capacity is expressed as labels:

```yaml
capacitySource:
  type: NodeLabel
  label: aliyun.accelerator/gpu-memory-mib
  perDeviceCount:
    label: aliyun.accelerator/gpu-count
```

The `perDeviceCount` field tells Kompakt how many devices exist on the node, so it can calculate total available capacity.

## Readiness signal

Defines when a node is ready to receive gated pods. The controller will not release gates targeting a node until all readiness conditions are met.

```yaml
readinessSignal:
  nodeConditions:
    - type: Ready
      status: "True"
  requiredLabels:
    - aliyun.accelerator/gpu-count
```

`nodeConditions` checks standard Kubernetes node conditions. `requiredLabels` ensures specific labels are present (useful for GPU nodes where device plugin labels appear after the node is Ready).

## Rules

An ordered list of [rule plugins](rule-plugins.md) to run for matched pods:

```yaml
rules:
  - name: WaitForWorkloadPacking
  - name: WaitForNodeReady
```

Rules are executed in order. Each rule decides whether the pod's gate should be released. See [Rule Plugins](rule-plugins.md) for available rules.

## Reservation timeout

Maximum time a pod's capacity reservation is held before the gate is released unconditionally:

```yaml
reservationTimeout: 3m
```

When a reservation times out, the gate is removed and the pod schedules normally without coordination. Recommended values:

- CPU/memory workloads: `3m`
- GPU workloads: `5m` (GPU nodes take longer to provision)

## Multiple profiles

You can create multiple PackingProfiles for different workload classes. Each pod's label determines which profile applies:

```yaml
# Profile for general CPU/memory coordination
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: general-cpu-coordination
spec:
  demandSource:
    type: ResourceRequest
    resources: [cpu, memory]
  # ...
---
# Profile for Alibaba cGPU fractional GPU
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: alibaba-cgpu
spec:
  demandSource:
    type: Annotation
    annotation: aliyun.com/gpu-mem
    unit: MiB
  # ...
```

Pods choose their profile via label:

```yaml
# CPU workload
labels:
  packer.kompakt.io/packing-profile: general-cpu-coordination

# GPU workload
labels:
  packer.kompakt.io/packing-profile: alibaba-cgpu
```

Each pod references exactly one profile. The relationship is explicit and unambiguous.

## Checking profile status

```bash
kubectl get packingprofiles
```

```
NAME                         DEMAND            RULES                        ACTIVE GATES   AGE
general-cpu-coordination     ResourceRequest   WaitForWorkloadPacking    12             1h
alibaba-cgpu                 Annotation        WaitForWorkloadPacking    3              1h
```

The `ACTIVE GATES` column shows how many pods are currently gated by each profile.
