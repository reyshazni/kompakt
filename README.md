# Kompakt

**Keep your cluster compact.**

Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscalers from over-provisioning nodes. It gates pods via [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA in K8s v1.30) and bin-packs scale-up events across all workload types: Deployments, StatefulSets, Jobs, KServe, Argo, Ray, and anything else that creates pods.

No custom scheduler. No privileged DaemonSets. No vendor lock-in.

Kompakt does **not**:
- Replace kube-scheduler or the cluster autoscaler
- Allocate GPU devices
- Manage quota or admission control
- Federate across clusters

## The Problem

The cluster autoscaler evaluates pending pods in scan cycles (every 10-30s). Pods that arrive in different cycles are not batched together. When a node is provisioning, the autoscaler simulates whether pending pods will fit -- but only for resources declared in the node template.

This causes over-provisioning: 2 half-GPU notebooks trigger 2 GPU nodes when 1 was enough (the node template does not declare `gpu-mem`). 3 services deployed simultaneously get 3 nodes instead of sharing. These are not autoscaler bugs -- no one coordinates demand across scan cycles.

## How It Works

1. Pod is created with label `packer.kompakt.io/packing-profile: <profile-name>`
2. **Webhook** looks up the referenced PackingProfile. If it does not exist, the pod is rejected with a clear error.
3. **Webhook** injects `spec.schedulingGates` into the pod
4. **Controller** maintains an in-flight node ledger tracking existing capacity and pending autoscaler nodes
5. **Gate released** with node affinity when capacity is available
6. **Your existing scheduler and autoscaler** continue working untouched

## Comparison

| | KAI | HAMi | Kueue | Volcano | Karpenter | **Kompakt** |
|---|:-:|:-:|:-:|:-:|:-:|:-:|
| Managed K8s native | ~ | ~ | yes | ~ | yes | **yes** |
| No custom scheduler | no | no | yes | no | yes | **yes** |
| Any workload type | yes | yes | Jobs only | yes | yes | **yes** |
| Multi-cloud | yes | yes | yes | ~ | AWS only | **yes** |
| Plug-and-play | no | no | ~ | no | yes | **yes** |
| CPU bin-packing | no | no | ~ | no | yes | **yes** |
| GPU bin-packing | ~ | ~ | no | ~ | ~ | **yes** |
| Pluggable rule engine | no | no | no | no | no | **yes** |

## Installation

Requires Kubernetes >= 1.30 and Helm 3.

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace
```

Verify the installation:

```bash
kubectl get pods -n kompakt-system
```

## Usage

### 1. Create a PackingProfile

Define how Kompakt should coordinate your workloads:

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
    - name: WaitForScaleUp
  reservationTimeout: 3m
```

```bash
kubectl apply -f packingprofile.yaml
```

### 2. Label your workloads

Add the profile label to pods you want Kompakt to coordinate:

```yaml
# In your Deployment, StatefulSet, Job, etc.
spec:
  template:
    metadata:
      labels:
        packer.kompakt.io/packing-profile: general-cpu-coordination
```

That's it. Pods with this label are gated and coordinated by the referenced profile. If the profile does not exist, pod creation is rejected with an error.

### 3. Verify pods are being gated

```bash
kubectl get packingprofiles
kubectl get pods -o jsonpath='{range .items[?(@.spec.schedulingGates)]}{.metadata.name}{"\n"}{end}'
```

### 4. Opt out specific pods

Add the label `kompakt.io/exclude=true` to any pod that should bypass gating. Add the annotation `kompakt.io/priority=high` to release the gate immediately.

### 5. Emergency uninstall

If something goes wrong, remove Kompakt from the request path instantly:

```bash
kubectl delete mutatingwebhookconfiguration kompakt-webhook
```

This restores default cluster behavior within seconds. No rollback needed.

## Configuration

| Field | Description | Default |
|---|---|---|
| `spec.demandSource` | How to extract resource demand from pods | required |
| `spec.capacitySource` | How to determine node capacity | required |
| `spec.readinessSignal` | When a node is ready for gated pods | Node Ready=True |
| `spec.rules` | Ordered list of rule plugins to execute | required |
| `spec.reservationTimeout` | Max hold before unconditional gate release | `3m` |

See the [full documentation](https://reyshazni.github.io/kompakt) for the PackingProfile API reference, GPU packing, multi-cloud setup, and more.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
