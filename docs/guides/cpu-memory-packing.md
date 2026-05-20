# CPU/Memory Packing

This guide covers how to use Kompakt to coordinate CPU and memory workloads.

## When you need this

You have multiple Deployments, StatefulSets, or other workloads that scale simultaneously, and your cluster autoscaler provisions more nodes than necessary. Common triggers:

- Traffic spikes that cause multiple services to scale at the same time
- Batch deployments (rollout of multiple services at once)
- Topology spread constraints across Deployments
- Pod affinity rules between different workloads

## Prerequisites

- Managed Kubernetes cluster (GKE, EKS, AKS, ACK, etc.) running **Kubernetes >= 1.30**
- Cluster autoscaler enabled (this is the default on most managed Kubernetes services)
- At least one node pool configured with autoscaling
- Kompakt installed ([Installation guide](../getting-started/installation.md))

## Which rules to use

Kompakt has two rules for CPU/memory workloads. You can use one or both depending on your scenario.

| Scenario | Rules | Why |
|---|---|---|
| Cluster has spare capacity, pods just need packing | `BinPackOnInflightCapacity` only | No scale-up involved, just fit pods onto existing nodes |
| Scale-up expected, want to prevent over-provisioning | `WaitForScaleUp` only | Let first pod trigger scale-up, hold the rest until node arrives |
| Both: pack onto existing, coordinate during scale-up | Both rules together | Most common setup for mixed clusters |

### BinPack only

Use when your cluster already has running nodes with available capacity and you want to minimize node waste. No scale-up coordination. Pods that don't fit stay gated until timeout.

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: binpack-only
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
  rules:
    - name: BinPackOnInflightCapacity
  reservationTimeout: 3m
```

### WaitForScaleUp only

Use when your node pool scales from zero or near-zero and you want to prevent redundant node provisioning. The first pod passes through to trigger the autoscaler, subsequent pods wait for the incoming node.

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: scaleup-only
spec:
  demandSource:
    type: ResourceRequest
    resources: [cpu, memory]
  capacitySource:
    type: NodeAllocatable
    resources: [cpu, memory]
    nodeGroupTemplates:
      - namePrefix: pool-cpu-4xlarge
        allocatable:
          cpu: 16000
          memory: 64000000000
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: WaitForScaleUp
  reservationTimeout: 3m
```

`nodeGroupTemplates` tells Kompakt how much capacity the incoming node will have. Without it, Kompakt cannot match pods to in-flight nodes. See [Finding your nodeGroupTemplate values](#finding-your-nodegrouptemplate-values) for how to fill this in.

### Both rules together

The most common setup. Pods first try to fit on existing capacity (BinPack). If nothing fits, WaitForScaleUp takes over: the first pod triggers the autoscaler, the rest wait.

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
    nodeGroupTemplates:
      - namePrefix: pool-cpu-4xlarge
        allocatable:
          cpu: 16000
          memory: 64000000000
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: BinPackOnInflightCapacity
    - name: WaitForScaleUp
  reservationTimeout: 3m
```

Rules execute in order. BinPack runs first. If it finds a fit on an existing node, the gate is released immediately. If not, WaitForScaleUp evaluates next.

## Setup

### 1. Create the PackingProfile

Pick the rule combination from above. Replace `pool-cpu-4xlarge` with your actual node pool name and fill in the allocatable values for your instance type (see [Finding your nodeGroupTemplate values](#finding-your-nodegrouptemplate-values)).

Save it as `packingprofile.yaml` and apply:

```bash
kubectl apply -f packingprofile.yaml
```

The profile defines HOW to coordinate. It does not select pods. Pods opt in by referencing this profile by name.

### 2. Label your workloads

Add the profile label to all workloads you want coordinated:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: service-a
spec:
  replicas: 3
  template:
    metadata:
      labels:
        app: service-a
        packer.kompakt.io/packing-profile: general-cpu-coordination
    spec:
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
      containers:
        - name: app
          resources:
            requests:
              cpu: "1"
              memory: 2Gi
```

Do the same for `service-b`, `service-c`, etc. All pods referencing the same profile are coordinated together.

### 3. Deploy and observe

```bash
# See gated pods
kubectl get pods -o custom-columns=\
NAME:.metadata.name,\
STATUS:.status.phase,\
GATES:.spec.schedulingGates[*].name

# See profile activity
kubectl get packingprofiles

# Watch nodes
kubectl get nodes -w
```

## What to expect

Without Kompakt, deploying `service-a` (3 replicas) and `service-b` (3 replicas) simultaneously with topology spread constraints on a full cluster would provision up to 6 nodes.

With Kompakt, the same deployment provisions 3 nodes. The first pod triggers a scale-up, subsequent pods are held until capacity is confirmed, then released in coordinated batches that share nodes.

## Finding your nodeGroupTemplate values

You need two things: the node pool name (for `namePrefix`) and the instance type's resources (for `allocatable`).

**Node pool name**: check the cluster autoscaler status ConfigMap:

```bash
kubectl get configmap cluster-autoscaler-status -n kube-system -o yaml
```

Look for lines like:

```
Name: pool-cpu-4xlarge
Health: ready=2, cloudProviderTarget=4
```

Use `pool-cpu-4xlarge` as your `namePrefix`.

**Allocatable resources**: check an existing node of the same instance type:

```bash
kubectl get node <node-name> -o jsonpath='{.status.allocatable}' | jq
```

Or check your cloud provider's console for the instance type's CPU and memory. Convert to millivalue: 16 vCPU = `16000`, 64 GiB memory = `64000000000` (in milli-bytes via `resource.Quantity.MilliValue()`).

!!! note
    If your cluster does not have any running nodes of that type yet, check your cloud provider's documentation for the instance type specs.

## Using different profiles for different tiers

You can create separate profiles with different timeout or rule configurations:

```yaml
# Tight timeout for latency-sensitive services
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: latency-sensitive
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
  rules:
    - name: BinPackOnInflightCapacity
  reservationTimeout: 30s
---
# Longer timeout for batch/background services
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: batch-coordination
spec:
  demandSource:
    type: ResourceRequest
    resources: [cpu, memory]
  capacitySource:
    type: NodeAllocatable
    resources: [cpu, memory]
    nodeGroupTemplates:
      - namePrefix: pool-batch
        allocatable:
          cpu: 32000
          memory: 128000000000
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: BinPackOnInflightCapacity
    - name: WaitForScaleUp
  reservationTimeout: 5m
```

The latency-sensitive profile uses BinPack only with a short timeout -- pods either fit on existing capacity or fall through fast. The batch profile uses both rules with a longer timeout to fully coordinate scale-ups.

## Tuning the reservation timeout

The default `3m` timeout works for most CPU/memory workloads. Adjust if:

- Your nodes take longer than 3 minutes to provision: increase the timeout
- You want faster fallback to uncoordinated scheduling: decrease the timeout
- Your workloads are latency-sensitive: decrease to `1m` or `30s`

!!! warning
    Setting the timeout too low reduces coordination effectiveness. Setting it too high means pods stay gated longer if something goes wrong. Start with the default and adjust based on your node provisioning times.

## Excluding specific pods

Critical pods that should never be delayed:

```yaml
metadata:
  labels:
    kompakt.io/exclude: "true"
```

Pods that should be gated but released immediately (tracked in metrics but not delayed):

```yaml
metadata:
  annotations:
    kompakt.io/priority: "high"
```

## Next steps

- [GPU packing](gpu-packing.md) for fractional GPU workloads
- [Scale-from-zero GPU](scale-from-zero-gpu.md) for GPU notebook/inference scenarios
- [Observability](observability.md) for monitoring coordination metrics
- [Troubleshooting](troubleshooting.md) for debugging gated pods
