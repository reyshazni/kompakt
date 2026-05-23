# Node Group Templates Reference

This page is the single reference for configuring `nodeGroupTemplates` in a PackingProfile. All guides link here rather than repeating the same instructions.

## What nodeGroupTemplates are

`nodeGroupTemplates` declare the expected capacity of nodes that do not yet exist. When the autoscaler is provisioning a new node, Kompakt cannot inspect its real resources (it has not joined the cluster yet). The template tells Kompakt what the node will have when it arrives.

Without a template, Kompakt cannot match pending pods to in-flight nodes. The `WaitForNodeReady` rule requires at least one template to function.

## Schema

```yaml
spec:
  capacitySource:
    nodeGroupTemplates:
      - namePrefix: <string>       # required: prefix matching the node group name
        allocatable:               # required: expected resources as milliValues
          <resource>: <int64>
          <resource>: <int64>
        labels:                    # optional: expected node labels
          <key>: <value>
        taints:                    # optional: expected node taints
          - key: <string>
            value: <string>
            effect: <NoSchedule|NoExecute|PreferNoSchedule>
```

## Fields

### namePrefix

The prefix that matches the node group name in the autoscaler's status. Kompakt matches in-flight nodes to templates by checking if the node group name starts with this prefix.

**How to find it:** Read the cluster autoscaler status ConfigMap:

```bash
kubectl get configmap cluster-autoscaler-status -n kube-system -o yaml
```

Look for `Name:` lines under `NodeGroups:`:

```
NodeGroups:
  Name: pool-gpu-l20-2split
  Health: ready=0, cloudProviderTarget=1
  ...
  Name: pool-cpu-4xlarge
  Health: ready=3, cloudProviderTarget=3
```

Use the full name or a unique prefix: `pool-gpu-l20-2split` or `pool-gpu-l20`.

**For GOATScaler (ACK):** The name prefix matches the scaling group name from `ProvisionNode` events. Check the GOATScaler component logs or the `ess-scalinggroup-id` label on existing nodes.

**For Karpenter (planned):** Will match `NodeClaim` names.

### allocatable

A map of resource names to their milliValues (int64). This represents the total allocatable capacity of the node for Kompakt's bin-packing calculations.

**Unit conversion rules:**

| Resource type | Raw value | milliValue | Example |
|---|---|---|---|
| CPU | 16 cores | 16000 | 16 vCPU node -> `cpu: 16000` |
| Memory | 64 GiB | 64000000000 | 64 GiB node -> `memory: 64000000000` |
| GPU (nvidia.com/gpu) | 8 GPUs | 8000 | 8x A100 node -> `nvidia.com/gpu: 8000` |
| GPU memory (cGPU) | 49152 MiB | 49152 | L20 48GiB -> `aliyun.com/gpu-mem: 49152` |

!!! note
    For annotation-based demand sources (cGPU), the allocatable value uses the same unit as the annotation. cGPU annotations are in MiB, so the template value is also in MiB (not milliValue).

**How to find allocatable values:**

Option 1 -- From an existing node of the same type:

```bash
kubectl get node <node-name> -o jsonpath='{.status.allocatable}' | jq
```

Option 2 -- From cloud provider documentation:

| Instance / GPU | CPU | Memory | GPU resource |
|---|---|---|---|
| ecs.gn7i-c8g1.2xlarge (ACK, L20 1/2) | 8000 | 30000000000 | `aliyun.com/gpu-mem: 24576` |
| ecs.gn7i-c16g1.4xlarge (ACK, L20 full) | 16000 | 60000000000 | `aliyun.com/gpu-mem: 49152` |
| p3.2xlarge (EKS, V100) | 8000 | 61000000000 | `nvidia.com/gpu: 1000` |
| p4d.24xlarge (EKS, A100x8) | 96000 | 1100000000000 | `nvidia.com/gpu: 8000` |
| a2-highgpu-1g (GKE, A100x1) | 12000 | 85000000000 | `nvidia.com/gpu: 1000` |

Option 3 -- From the autoscaler node template (if it includes the resource):

```bash
# CA exposes node templates in its status ConfigMap (some implementations)
kubectl get configmap cluster-autoscaler-status -n kube-system -o yaml | grep -A 20 "Name: pool-gpu"
```

!!! warning
    Always use the **allocatable** value, not the **capacity** value. Allocatable = capacity minus system reserved. Using capacity overestimates what pods can actually use.

### labels (optional)

Expected labels the node will have when it joins the cluster. Used by `FindFit` to match pods with `nodeSelector` requirements against in-flight nodes.

```yaml
nodeGroupTemplates:
  - namePrefix: pool-gpu-l20
    allocatable:
      aliyun.com/gpu-mem: 49152
    labels:
      node.kubernetes.io/instance-type: ecs.gn7i-c16g1.4xlarge
      topology.kubernetes.io/zone: ap-southeast-5a
      gpu-type: l20
```

**How to find expected labels:** Check an existing node from the same node pool:

```bash
kubectl get node <node-name> -o jsonpath='{.metadata.labels}' | jq
```

Only include labels that pods select on (via `nodeSelector` or `nodeAffinity`). Do not include all labels.

### taints (optional)

Expected taints the node will have. Used by `FindFit` to verify that pods tolerate the in-flight node's taints before reserving capacity.

```yaml
nodeGroupTemplates:
  - namePrefix: pool-gpu-l20
    allocatable:
      aliyun.com/gpu-mem: 49152
    taints:
      - key: project
        value: ml-platform
        effect: NoSchedule
      - key: gpu-type
        value: l20
        effect: NoSchedule
      - key: gpu-split
        value: "2"
        effect: NoSchedule
```

**How to find expected taints:** Check the node pool configuration in your cloud console, or inspect an existing node:

```bash
kubectl get node <node-name> -o jsonpath='{.spec.taints}' | jq
```

!!! important
    If your node pool has taints and you do not declare them in the template, Kompakt will reserve capacity on in-flight nodes for pods that cannot tolerate those taints. The pods will be released with node affinity but fail to schedule. Always declare taints that your node pool applies.

## Multiple templates

A single profile can have multiple templates for different node groups:

```yaml
nodeGroupTemplates:
  - namePrefix: pool-gpu-l20-full
    allocatable:
      aliyun.com/gpu-mem: 49152
    taints:
      - key: gpu-type
        value: l20
        effect: NoSchedule
  - namePrefix: pool-gpu-l20-half
    allocatable:
      aliyun.com/gpu-mem: 24576
    taints:
      - key: gpu-type
        value: l20-half
        effect: NoSchedule
```

Kompakt matches in-flight nodes to templates by prefix. The first matching template is used.

## When templates are NOT needed

- **`WaitForWorkloadPacking` only profiles**: BinPack operates on existing nodes (already joined, real capacity visible). No template needed.
- **Static node pools (no autoscaling)**: If your nodes are always running, there are no in-flight nodes to track.

## Common mistakes

| Mistake | Consequence | Fix |
|---|---|---|
| Using capacity instead of allocatable | Over-estimates available resources, pods may not fit when released | Use `kubectl get node -o jsonpath='{.status.allocatable}'` |
| Missing taints in template | Pods reserved on in-flight nodes they cannot tolerate | Add all node pool taints to the template |
| Wrong namePrefix | In-flight nodes not matched to template, treated as unknown capacity | Verify against `cluster-autoscaler-status` ConfigMap |
| Forgetting to update after node pool resize | Template no longer matches reality | Audit templates when changing instance types |
| Declaring labels pods never select on | No harm but unnecessary noise | Only include labels used in `nodeSelector` or `nodeAffinity` |
