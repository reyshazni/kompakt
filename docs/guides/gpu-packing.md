# GPU Packing

## When you need this

You run fractional GPU workloads and the cluster autoscaler provisions more GPU nodes than necessary during scale-ups. Common scenarios:

- Multiple inference Deployments sharing GPUs via time-slicing or cGPU
- Burst of GPU workloads from KServe, Ray, or Kubeflow that trigger simultaneous node provisioning
- GPU nodes are expensive and even one extra node is significant cost

Kompakt understands GPU resource requests for bin-packing decisions without replacing device allocation. NVIDIA device plugin, HAMi, KAI, and other GPU sharing systems continue to handle actual device assignment.

## Which rules to use

| Scenario | Rules | Why |
|---|---|---|
| GPU nodes already running, pack more pods onto them | `BinPackOnInflightCapacity` only | Fit pods onto existing GPU capacity |
| Scale-from-zero GPU, prevent double provisioning | `WaitForScaleUp` only | See [Scale-from-zero GPU guide](scale-from-zero-gpu.md) |
| Mixed: some GPU nodes exist, also expect scale-ups | Both rules together | Pack first, coordinate new nodes second |

For the common GPU notebook/inference scenario where you scale from zero, see the dedicated [Scale-from-zero GPU guide](scale-from-zero-gpu.md).

## Supported GPU sharing systems

| System | Demand source | Version |
|---|---|---|
| NVIDIA device-plugin (`nvidia.com/gpu`) | ResourceRequest | v0.1 |
| Alibaba cGPU (`aliyun.com/gpu-mem`) | Annotation | v0.1 |
| HAMi annotations | Annotation | v0.2 |
| KAI annotations | Annotation | v0.2 |

## NVIDIA device-plugin (whole GPU or time-slicing)

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
    - name: BinPackOnInflightCapacity
    - name: WaitForScaleUp
  reservationTimeout: 5m
```

### 2. Label your GPU workloads

```yaml
labels:
  packer.kompakt.io/packing-profile: nvidia-gpu
```

This works for both whole-GPU requests and NVIDIA time-slicing (where `nvidia.com/gpu` is replicated per partition).

## Alibaba cGPU

Alibaba cGPU expresses GPU memory demand via pod annotations and node capacity via node labels. The cluster autoscaler does not understand these annotations, which makes over-provisioning especially severe.

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
    - name: BinPackOnInflightCapacity
    - name: WaitForScaleUp
  reservationTimeout: 5m
```

The `requiredLabels` field in `readinessSignal` ensures that the controller waits for the GPU device plugin labels to appear before considering the node ready. GPU nodes often reach `Ready=True` before the device plugin has registered its labels.

### 2. Label your cGPU workloads

```yaml
labels:
  packer.kompakt.io/packing-profile: alibaba-cgpu
```

## BinPack only (existing GPU nodes)

If your GPU nodes are always running and you just want to pack more pods onto them without scale-up coordination:

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
    - name: BinPackOnInflightCapacity
  reservationTimeout: 1m
```

No `nodeGroupTemplates` needed since there are no in-flight nodes to track.

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

- Does not allocate GPU devices to containers
- Does not manage MIG profiles (planned for v0.3)
- Does not replace NVIDIA device plugin, HAMi, or KAI
- Does not modify GPU-related annotations on pods

Kompakt only uses GPU information for node-level capacity decisions. Actual device allocation remains the responsibility of the GPU sharing system you already have installed.

## Next steps

- [Scale-from-zero GPU](scale-from-zero-gpu.md) for the GPU notebook/inference scenario
- [CPU/Memory packing](cpu-memory-packing.md) for non-GPU workloads
- [Observability](observability.md) for monitoring GPU packing metrics
- [Troubleshooting](troubleshooting.md) for debugging gated GPU pods
