# Kompakt

**Keep your cluster compact.**

Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscalers from over-provisioning nodes. It uses [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA in K8s v1.30) to control when pods become visible to the scheduler and autoscaler, coordinating scale-up events across all workload types.

No custom scheduler. No privileged DaemonSets. No vendor lock-in.

**[Read the docs](https://reyshazni.github.io/kompakt/)** | [Install](https://reyshazni.github.io/kompakt/getting-started/installation/) | [Problem statement](https://reyshazni.github.io/kompakt/introduction/problem-statement/)

## The Problem

The cluster autoscaler evaluates pending pods in scan cycles (every 10-30 seconds). Pods that arrive in different cycles are not batched together. When a node is being provisioned but not yet Ready, the autoscaler simulates whether pending pods will fit on it, but this simulation only works for resources declared in the node template.

This causes over-provisioning whenever demand arrives faster than the autoscaler can batch it. Some examples:

**Fractional GPU sharing**: You have 2 notebooks, each needing half a GPU. One node is enough. But the autoscaler's node template does not declare `gpu-mem`, so it cannot simulate that the second notebook fits on the incoming node. It provisions a second GPU node. You pay double.

**Burst deployments**: You deploy 3 services simultaneously, each with topology spread constraints. The autoscaler sees them in separate scan cycles and provisions nodes independently, often 1 node per service instead of packing them together.

**Scale-from-zero**: A node pool scales to zero when idle. Two requests arrive within seconds. The first triggers a node, the second cannot see it yet and triggers another.

These are not bugs in the autoscaler. It makes the best decision it can with the information available at each scan cycle. The problem is that no one coordinates demand across cycles. Kompakt fills that gap.

## How It Works

Kompakt coordinates pods using two rules:

**WaitForWorkloadPacking**: Fits pods onto existing nodes with available capacity. Finds the best-fit node and releases the pod with node affinity.

**WaitForNodeReady**: Coordinates scale-up events. The first pod passes through to trigger the autoscaler. Subsequent pods are held until the new node is ready, preventing redundant provisioning.

The flow:

1. Pod is created with label `packer.kompakt.io/packing-profile: <profile-name>`
2. **Webhook** injects scheduling gates into the pod
3. **Controller** maintains a node ledger tracking existing capacity and in-flight nodes
4. **Rules** evaluate: release immediately, hold for incoming node, or release with node affinity
5. **Your existing scheduler and autoscaler** continue working untouched

## Installation

Requires Kubernetes >= 1.30.

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace
```

## Usage

### 1. Create a PackingProfile

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: gpu-coordination
spec:
  demandSource:
    type: ResourceRequest
    additionalResources: [aliyun.com/gpu-core.percentage]
  capacitySource:
    type: NodeAllocatable
    nodeGroupTemplates:
      - labels:
          project: my-gpu-pool
        taints:
          - key: project
            value: my-gpu-pool
            effect: NoSchedule
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: WaitForWorkloadPacking
    - name: WaitForNodeReady
  reservationTimeout: 5m
```

CPU and memory are always tracked automatically. `additionalResources` adds extended resources like GPU core percentage. Node capacity is read directly from the Kubernetes API, including NotReady nodes that are still provisioning.

```bash
kubectl apply -f packingprofile.yaml
```

### 2. Label your workloads

```yaml
spec:
  template:
    metadata:
      labels:
        packer.kompakt.io/packing-profile: gpu-coordination
```

If the profile does not exist, pod creation is rejected with an error.

### 3. Observe

```bash
kubectl get packingprofiles
# NAME               DEMAND            RULES                                                GATES   INFLIGHT   READY   AGE
# gpu-coordination   ResourceRequest   ["WaitForWorkloadPacking","WaitForNodeReady"]         2       1          True    5m

kubectl describe pod my-pod
# Events:
#   GateHeld: gate held by rule WaitForNodeReady, profile=gpu-coordination
#   GateReleased: gate released, reason=capacity, targetNode=ap-southeast-5.10.199.x.y
```

### 4. Opt out specific pods

- `kompakt.io/exclude=true` label: bypass gating entirely
- `kompakt.io/priority=high` annotation: release gate immediately

### 5. Emergency uninstall

```bash
kubectl delete mutatingwebhookconfiguration kompakt-webhook
```

Restores default cluster behavior within seconds.

## Autoscaler Support

Kompakt is autoscaler-aware, not cloud-aware. It detects in-flight nodes from whichever autoscaler is running, without cloud credentials.

| Autoscaler | Detection | Clouds |
|---|---|---|
| Cluster Autoscaler | ConfigMap status | EKS, GKE, AKS, self-managed |
| GOATScaler | ProvisionNode events | Alibaba ACK |
| NotReady fallback | Node objects | All |

Detection is automatic. No configuration needed.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
