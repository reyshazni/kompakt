# CPU/Memory Packing

This guide covers how to use Kompakt to coordinate CPU and memory workloads during scale-up events.

## When you need this

You have multiple Deployments, StatefulSets, or other workloads that scale simultaneously, and your cluster autoscaler provisions more nodes than necessary. Common triggers:

- Traffic spikes that cause multiple services to scale at the same time
- Batch deployments (rollout of multiple services at once)
- Topology spread constraints across Deployments
- Pod affinity rules between different workloads

## Setup

### 1. Create the PackingProfile

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
  rules:
    - name: BinPackOnInflightCapacity
  reservationTimeout: 3m
```

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

With Kompakt, the same deployment provisions 3 nodes. The controller holds all 6 pods, waits for the autoscaler to start provisioning, and releases them in coordinated batches that share nodes.

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
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: BinPackOnInflightCapacity
  reservationTimeout: 5m
```

Then label pods accordingly:

```yaml
# Frontend service - short timeout
labels:
  packer.kompakt.io/packing-profile: latency-sensitive

# Background worker - longer timeout
labels:
  packer.kompakt.io/packing-profile: batch-coordination
```

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
- [Observability](observability.md) for monitoring coordination metrics
- [Troubleshooting](troubleshooting.md) for debugging gated pods
