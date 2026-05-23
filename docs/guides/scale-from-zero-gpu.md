---
description: Prevent double GPU node provisioning when scaling from zero in Kubernetes. Kompakt coordinates pods during scale-up to avoid redundant autoscaler decisions.
---

# Scale-from-zero GPU

This guide walks through the most common Kompakt use case: preventing the cluster autoscaler from provisioning redundant GPU nodes when multiple pods arrive during a scale-up from zero.

## When you need this

Your GPU node pool scales to zero when idle to save cost. When users request GPU workloads (notebooks, inference, training), the autoscaler provisions new GPU nodes. If multiple requests arrive within the same autoscaler scan cycle (~30s), each one triggers a separate node, even when they could share.

## Prerequisites

- Managed Kubernetes cluster (GKE, EKS, AKS, ACK, etc.) running **Kubernetes >= 1.30**
- Cluster autoscaler enabled with a GPU node pool configured for scale-to-zero
- A GPU sharing system installed if using fractional GPU (cGPU, NVIDIA time-slicing, HAMi). See the [GPU packing guide](gpu-packing.md#supported-gpu-sharing-systems) for supported systems.
- Kompakt installed ([Installation guide](../getting-started/installation.md))

## The problem

You have a GPU node pool (e.g., NVIDIA L20 with cGPU 2-split). Each notebook takes half a GPU. One node fits 2 notebooks.

Without Kompakt:

1. Notebook A created, no GPU node exists, pod is Pending
2. Autoscaler sees A, triggers scale-up of 1 node
3. Node is provisioning (~3 min for GPU)
4. Notebook B created, also Pending
5. Autoscaler sees B, checks if the upcoming node can fit it. But the node template is missing gpu-mem (only declares gpu-core.percentage). Simulation says B does not fit.
6. Autoscaler triggers scale-up of a second node

Result: 2 nodes for 2 half-GPU notebooks. 1 node was enough. You pay double.

The autoscaler cannot solve this alone. Its "upcoming node" simulation depends on knowing all resources the new node will have. For cGPU, pods request `aliyun.com/gpu-mem` but the node template may not declare it. The autoscaler assumes 0, concludes B does not fit, and provisions another node.

## What Kompakt does

Kompakt sits between pod creation and the scheduler using scheduling gates (K8s 1.30+). A gated pod is invisible to both kube-scheduler and the autoscaler. Kompakt controls when each pod becomes visible.

The `WaitForNodeReady` rule makes three decisions:

1. **No capacity anywhere** (no nodes, no in-flight nodes): release the gate immediately. The pod becomes visible to the autoscaler and triggers a scale-up.
2. **In-flight node can fit**: hold the gate. The pod stays invisible, preventing a redundant scale-up.
3. **Node arrived and has capacity**: release the gate with node affinity to the real node.

Step by step:

1. Notebook A created. Kompakt checks the ledger. Nothing exists. Release the gate. A becomes visible, autoscaler triggers scale-up.
2. Node starts provisioning. Kompakt detects this from the `cluster-autoscaler-status` ConfigMap and knows the incoming node will have 48 GiB gpu-mem (from the profile's `nodeGroupTemplates`).
3. Notebook B created. Kompakt checks the ledger. Sees an in-flight node with 48 GiB, A already claimed 24 GiB, 24 GiB remaining. B fits. Hold the gate. B stays invisible. No second scale-up.
4. Node becomes Ready. Kompakt sees real capacity. 24 GiB available. Release B's gate with node affinity to the real hostname.
5. kube-scheduler places B on the same node.

Result: 1 node, 2 notebooks, no waste.

## Setup

### 1. Create the PackingProfile

This example uses Alibaba cGPU on ACK. For NVIDIA device-plugin, see the [NVIDIA variant](#nvidia-device-plugin-variant) below.

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: cgpu-notebook
spec:
  demandSource:
    type: Annotation
    annotation: aliyun.com/gpu-mem
    unit: MiB
  capacitySource:
    type: NodeLabel
    label: aliyun.accelerator/gpu-memory-mib
    perDeviceCount:
      label: aliyun.accelerator/gpu-count
    nodeGroupTemplates:
      - namePrefix: pool-l20
        allocatable:
          aliyun.com/gpu-mem: 49152
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
    requiredLabels:
      - aliyun.accelerator/gpu-count
  rules:
    - name: WaitForNodeReady
  reservationTimeout: 5m
```

Before applying, replace these values with your own:

| Field | What to put | How to find it |
|---|---|---|
| `namePrefix: pool-l20` | Your GPU node pool name | See [Finding your node pool name](#finding-your-node-pool-name) |
| `aliyun.com/gpu-mem: 49152` | Total GPU memory in MiB for your GPU type | L20 = 49152, A100-40G = 40960, V100 = 16384, T4 = 15360 |
| `reservationTimeout: 5m` | Longer than your GPU node provisioning time | GPU nodes typically take 2-5 minutes |

Key fields explained:

- **`nodeGroupTemplates`**: declares expected resources for incoming nodes. This is how Kompakt knows the node's capacity before it arrives.
- **`WaitForNodeReady`**: the only rule needed for scale-from-zero. No BinPack rule because there are no existing nodes to pack onto.
- **`requiredLabels`**: waits for the cGPU device plugin labels before considering the node ready. GPU nodes often reach `Ready=True` before the device plugin registers.

Apply it:

```bash
kubectl apply -f packingprofile.yaml
```

### 2. Label your workloads

Add the profile label to your notebook Deployment, StatefulSet, or any workload that creates pods. The `aliyun.com/gpu-mem` annotation is set by your platform (JupyterHub, KubeFlow, etc.). Kompakt reads it but does not create it.

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: jupyter-notebook
spec:
  replicas: 4
  selector:
    matchLabels:
      app: jupyter-notebook
  template:
    metadata:
      labels:
        app: jupyter-notebook
        packer.kompakt.io/packing-profile: cgpu-notebook
      annotations:
        aliyun.com/gpu-mem: "24576"
    spec:
      containers:
        - name: notebook
          image: jupyter/tensorflow-notebook:latest
          resources:
            requests:
              cpu: "2"
              memory: 8Gi
```

### 3. Apply and observe

```bash
kubectl apply -f packingprofile.yaml
kubectl apply -f notebook.yaml
```

Watch the gates:

```bash
kubectl get pods -w -o custom-columns=\
NAME:.metadata.name,\
PHASE:.status.phase,\
GATES:.spec.schedulingGates[*].name,\
NODE:.spec.nodeName
```

Expected output during scale-up:

```
NAME                  PHASE     GATES                            NODE
jupyter-notebook-0    Pending   <none>                           <none>     # released, triggering scale-up
jupyter-notebook-1    Pending   kompakt.io/wait-for-node-ready     <none>     # held, waiting for node
jupyter-notebook-2    Pending   kompakt.io/wait-for-node-ready     <none>     # held
jupyter-notebook-3    Pending   kompakt.io/wait-for-node-ready     <none>     # held
```

After nodes arrive:

```
NAME                  PHASE     GATES    NODE
jupyter-notebook-0    Running   <none>   cn-jakarta.172.16.1.10
jupyter-notebook-1    Running   <none>   cn-jakarta.172.16.1.10
jupyter-notebook-2    Running   <none>   cn-jakarta.172.16.1.11
jupyter-notebook-3    Running   <none>   cn-jakarta.172.16.1.11
```

2 nodes for 4 half-GPU notebooks instead of 4 nodes.

## Finding your node pool name and template values

See the [Node Group Templates Reference](../reference/node-group-templates.md) for how to find `namePrefix`, `allocatable` values, and how to configure labels and taints on templates. The reference covers CA ConfigMap, GOATScaler, and Karpenter detection sources.

## Adding BinPack for mixed scenarios

If your GPU nodes sometimes have spare capacity (not always scale-from-zero), add `WaitForWorkloadPacking` before `WaitForNodeReady`:

```yaml
rules:
  - name: WaitForWorkloadPacking
  - name: WaitForNodeReady
```

BinPack runs first. If a pod fits on an existing node, it is released immediately with node affinity. If not, WaitForNodeReady takes over and coordinates the scale-up.

## NVIDIA device-plugin variant

Same pattern, different demand source. This assumes the [NVIDIA device-plugin](https://github.com/NVIDIA/k8s-device-plugin) is installed on your cluster (default on most managed Kubernetes GPU node pools).

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: nvidia-notebook
spec:
  demandSource:
    type: ResourceRequest
    resources: [nvidia.com/gpu, memory]
  capacitySource:
    type: NodeAllocatable
    resources: [nvidia.com/gpu, memory]
    nodeGroupTemplates:
      - namePrefix: pool-a100
        allocatable:
          nvidia.com/gpu: 8000
          memory: 512000000000
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: WaitForNodeReady
  reservationTimeout: 5m
```

Replace `pool-a100` with your GPU node pool name and update allocatable to match your instance type.

## Next steps

- [GPU packing](gpu-packing.md) for general GPU bin-packing
- [CPU/Memory packing](cpu-memory-packing.md) for non-GPU workloads
- [Observability](observability.md) for monitoring coordination metrics
- [Troubleshooting](troubleshooting.md) for debugging gated GPU pods
