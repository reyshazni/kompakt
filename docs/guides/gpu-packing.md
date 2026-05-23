# GPU Packing

This guide covers how to use Kompakt to coordinate fractional GPU workloads and prevent over-provisioning of GPU nodes.

## When you need this

Multiple GPU workloads arrive at the same time (or within the same autoscaler scan cycle), and the cluster autoscaler provisions a separate GPU node for each one -- even when they could share. GPU nodes are expensive; even one extra node is significant cost.

Common scenarios:

- Multiple inference Deployments sharing GPUs via time-slicing or cGPU
- Burst of GPU workloads from notebook platforms, KServe, Ray, or Kubeflow
- Scale-from-zero GPU node pools where the autoscaler does not know incoming node capacity

## Prerequisites

- Managed Kubernetes cluster (GKE, EKS, AKS, ACK, etc.) running **Kubernetes >= 1.30**
- Cluster autoscaler enabled with at least one GPU node pool configured for autoscaling
- A GPU sharing system installed on your cluster (see [Supported GPU sharing systems](#supported-gpu-sharing-systems))
- Kompakt installed ([Installation guide](../getting-started/installation.md))

Kompakt does not install or manage GPU device plugins. It reads the resource requests and annotations that your GPU sharing system produces, and uses them for capacity decisions. The GPU sharing system must be installed and working before you configure Kompakt.

## Supported GPU sharing systems

| System | How pods request GPU | Kompakt demand source | Version |
|---|---|---|---|
| [NVIDIA device-plugin](https://github.com/NVIDIA/k8s-device-plugin) | `resources.requests: nvidia.com/gpu` | ResourceRequest | v0.1 |
| [Alibaba cGPU](https://www.alibabacloud.com/help/en/ack/ack-managed-and-ack-dedicated/user-guide/install-and-use-the-shared-gpu-component) | Pod annotation `aliyun.com/gpu-mem` | Annotation | v0.1 |
| [HAMi](https://github.com/Project-HAMi/HAMi) | Pod annotations | Annotation | v0.2 |
| [KAI](https://github.com/AliyunContainerService/gpushare-scheduler-extender) | Pod annotations | Annotation | v0.2 |

## Which rules to use

| Scenario | Rules | Why |
|---|---|---|
| GPU nodes already running, pack more pods onto them | `WaitForWorkloadPacking` only | Fit pods onto existing GPU capacity, no scale-up involved |
| Scale-from-zero GPU, prevent double provisioning | `WaitForNodeReady` only | See [Scale-from-zero GPU guide](scale-from-zero-gpu.md) |
| Mixed: some GPU nodes exist, also expect scale-ups | Both rules together | Pack onto existing nodes first, coordinate new nodes second |

For the common GPU notebook/inference scenario where your GPU node pool scales from zero, see the dedicated [Scale-from-zero GPU guide](scale-from-zero-gpu.md).

## NVIDIA device-plugin (whole GPU or time-slicing)

This section assumes you have the [NVIDIA device-plugin](https://github.com/NVIDIA/k8s-device-plugin) installed on your cluster. On managed Kubernetes, this is typically enabled by selecting a GPU node pool in your cloud provider's console.

### 1. Create the profile

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: nvidia-gpu
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
    - name: WaitForWorkloadPacking
    - name: WaitForNodeReady
  reservationTimeout: 5m
```

Replace `pool-a100` with your GPU node pool name and update the `allocatable` values to match your instance type. See [Finding your nodeGroupTemplate values](#finding-your-nodegrouptemplate-values).

### 2. Label your GPU workloads

Add the label to your Deployment, StatefulSet, Job, or any workload that creates pods:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: inference-server
spec:
  template:
    metadata:
      labels:
        app: inference-server
        packer.kompakt.io/packing-profile: nvidia-gpu
    spec:
      containers:
        - name: model
          image: my-model:latest
          resources:
            requests:
              nvidia.com/gpu: 1
              memory: 16Gi
```

This works for both whole-GPU requests and NVIDIA time-slicing (where `nvidia.com/gpu` is replicated per partition).

## Alibaba cGPU

This section assumes you have Alibaba cGPU installed on your ACK cluster. cGPU is installed via the ACK console under "Manage System Components" or via the cGPU Helm chart. Once installed, it provides the `aliyun.com/gpu-mem` annotation for GPU memory sharing and adds `aliyun.accelerator/gpu-memory-mib` and `aliyun.accelerator/gpu-count` labels to GPU nodes.

cGPU expresses GPU memory demand via pod annotations and node capacity via node labels. The cluster autoscaler does not understand these annotations, which makes over-provisioning especially severe -- this is the primary use case for Kompakt.

### 1. Create the profile

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: alibaba-cgpu
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
    - name: WaitForWorkloadPacking
    - name: WaitForNodeReady
  reservationTimeout: 5m
```

Replace `pool-l20` with your GPU node pool name and `49152` with the total GPU memory in MiB for your GPU type (e.g., L20 = 48 GiB = 49152 MiB). See [Finding your nodeGroupTemplate values](#finding-your-nodegrouptemplate-values).

The `requiredLabels` field ensures that Kompakt waits for the cGPU device plugin labels to appear on the node before considering it ready. GPU nodes often reach `Ready=True` before the device plugin has registered its labels.

### 2. Label your cGPU workloads

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: notebook
spec:
  template:
    metadata:
      labels:
        app: notebook
        packer.kompakt.io/packing-profile: alibaba-cgpu
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

The `aliyun.com/gpu-mem` annotation is set by your application or platform (e.g., JupyterHub, KubeFlow). Kompakt reads it but does not create or modify it.

## BinPack only (existing GPU nodes)

If your GPU nodes are always running (no autoscaling) and you just want to pack more pods onto available GPU capacity:

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: gpu-binpack-only
spec:
  demandSource:
    type: ResourceRequest
    resources: [nvidia.com/gpu]
  capacitySource:
    type: NodeAllocatable
    resources: [nvidia.com/gpu]
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: WaitForWorkloadPacking
  reservationTimeout: 1m
```

No `nodeGroupTemplates` needed since there are no in-flight nodes to track. No `WaitForNodeReady` rule needed since no scale-up is expected.

## Finding your nodeGroupTemplate values

See the [Node Group Templates Reference](../reference/node-group-templates.md) for how to find your `namePrefix`, `allocatable` values (including GPU-specific resources), and how to configure labels and taints on templates.

## GPU timeout considerations

GPU nodes typically take longer to provision than CPU nodes (2-5 minutes vs 1-2 minutes) due to driver initialization and device plugin startup. Set `reservationTimeout` to `5m` or higher for GPU profiles.

## Combining CPU and GPU profiles

Create separate profiles for each workload class. Each pod references exactly one profile:

```yaml
# CPU workload
labels:
  packer.kompakt.io/packing-profile: general-cpu-coordination

# GPU workload
labels:
  packer.kompakt.io/packing-profile: alibaba-cgpu
```

## What Kompakt does NOT do with GPUs

- Does not install GPU drivers or device plugins
- Does not allocate GPU devices to containers
- Does not manage MIG profiles (planned for v0.3)
- Does not replace NVIDIA device plugin, HAMi, or KAI
- Does not modify GPU-related annotations on pods

Kompakt only reads GPU resource information for node-level capacity decisions. Actual GPU device allocation is handled entirely by your GPU sharing system.

## Next steps

- [Scale-from-zero GPU](scale-from-zero-gpu.md) for the GPU notebook/inference scenario
- [CPU/Memory packing](cpu-memory-packing.md) for non-GPU workloads
- [Observability](observability.md) for monitoring GPU packing metrics
- [Troubleshooting](troubleshooting.md) for debugging gated GPU pods
