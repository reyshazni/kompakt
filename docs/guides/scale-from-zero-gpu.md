# Scale-from-zero GPU

This guide walks through the most common Kompakt use case: preventing the cluster autoscaler from provisioning redundant GPU nodes when multiple pods arrive during a scale-up.

## The problem

You have a GPU node pool (e.g., L20 with cGPU 2-split). Each notebook takes half a GPU. One node fits 2 notebooks.

Without Kompakt:

1. Notebook A created, no GPU node exists, pod is Pending
2. Autoscaler sees A, triggers scale-up of 1 node
3. Node is provisioning (~3 min for GPU)
4. Notebook B created, also Pending
5. Autoscaler sees B, checks if the upcoming node can fit it. But the node template is missing gpu-mem (only declares gpu-core.percentage). Simulation says B does not fit.
6. Autoscaler triggers scale-up of a second node

Result: 2 nodes for 2 half-GPU notebooks. 1 node was enough. You pay double.

The autoscaler cannot solve this alone because its "upcoming node" simulation depends on knowing all resources the new node will have. For cGPU, pods request `aliyun.com/gpu-mem` but the node template may not declare it. The autoscaler assumes 0, so it thinks B does not fit.

## What Kompakt does

Kompakt sits between pod creation and the scheduler using scheduling gates. A gated pod is invisible to both kube-scheduler and the autoscaler.

The `WaitForScaleUp` rule controls when each pod becomes visible:

1. Notebook A created. Kompakt checks the ledger. Nothing exists, no in-flight nodes. Release the gate immediately. A becomes visible, autoscaler triggers scale-up.
2. Node starts provisioning. Kompakt detects this from the `cluster-autoscaler-status` ConfigMap and knows the incoming node will have 48GB gpu-mem (from `nodeGroupTemplates`).
3. Notebook B created. Kompakt checks the ledger. Sees an in-flight node with 48GB, A already claimed 24GB, 24GB remaining. B fits. Hold the gate. B stays invisible to the autoscaler. No second scale-up.
4. Node becomes Ready, A starts running, 24GB used. Kompakt sees real capacity. 24GB available. Release B's gate with node affinity to the real hostname.
5. kube-scheduler places B on the same node. Done.

Result: 1 node, 2 notebooks, no waste.

## Setup

### 1. Create the PackingProfile

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
    - name: WaitForScaleUp
  reservationTimeout: 5m
```

Key fields:

- **`nodeGroupTemplates`**: declares that nodes from the `pool-l20` group will have 49152 MiB of gpu-mem. This is how Kompakt knows the incoming node's capacity before it arrives. Match the `namePrefix` to whatever your autoscaler calls the node group.
- **`WaitForScaleUp`**: the only rule needed for scale-from-zero. No BinPack rule because there are no existing nodes to pack onto.
- **`requiredLabels`**: waits for the GPU device plugin label before considering the node ready. GPU nodes often reach `Ready=True` before device plugin registration.
- **`reservationTimeout: 5m`**: GPU nodes take 2-5 minutes to provision. 5m gives enough buffer.

### 2. Label your notebooks

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: jupyter-notebook
spec:
  replicas: 4
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

# Watch gates
kubectl get pods -w -o custom-columns=\
NAME:.metadata.name,\
PHASE:.status.phase,\
GATES:.spec.schedulingGates[*].name,\
NODE:.spec.nodeName

# Watch profile
kubectl get packingprofiles
```

Expected output during scale-up:

```
NAME                  PHASE     GATES                            NODE
jupyter-notebook-0    Pending   <none>                           <none>     # released, triggering scale-up
jupyter-notebook-1    Pending   kompakt.io/awaiting-scale-up     <none>     # held, waiting for node
jupyter-notebook-2    Pending   kompakt.io/awaiting-scale-up     <none>     # held
jupyter-notebook-3    Pending   kompakt.io/awaiting-scale-up     <none>     # held
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

## Finding your nodeGroupTemplate values

The `namePrefix` must match the node group name that appears in the `cluster-autoscaler-status` ConfigMap:

```bash
kubectl get configmap cluster-autoscaler-status -n kube-system -o yaml
```

Look for lines like:

```
Name: pool-l20
Health: ready=0, cloudProviderTarget=2
```

The `namePrefix` should be `pool-l20`.

For `allocatable`, check an existing node of the same type:

```bash
kubectl get node <gpu-node-name> -o jsonpath='{.status.allocatable}' | jq
```

Or check your cloud provider's node pool configuration for the expected resources.

## Adding BinPack for mixed scenarios

If your GPU nodes sometimes have spare capacity (not always scale-from-zero), add `BinPackOnInflightCapacity` before `WaitForScaleUp`:

```yaml
rules:
  - name: BinPackOnInflightCapacity
  - name: WaitForScaleUp
```

BinPack runs first. If a pod fits on an existing node, it is released immediately. If not, WaitForScaleUp takes over and coordinates the scale-up.

## NVIDIA device-plugin variant

Same pattern, different demand source:

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
    - name: WaitForScaleUp
  reservationTimeout: 5m
```

## Next steps

- [GPU packing](gpu-packing.md) for general GPU bin-packing
- [CPU/Memory packing](cpu-memory-packing.md) for non-GPU workloads
- [Observability](observability.md) for monitoring coordination metrics
- [Troubleshooting](troubleshooting.md) for debugging gated GPU pods
