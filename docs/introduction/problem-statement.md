---
description: Why Kubernetes cluster autoscalers over-provision nodes and how scheduling gates solve the coordination gap across scan cycles and device-plugin resources.
---

# Problem Statement

## The coordination gap in Kubernetes autoscaling

Kubernetes cluster autoscalers (Cluster Autoscaler, Karpenter, GOATScaler, GKE NAP) solve a well-defined problem: provision nodes when pods are pending. They do this by simulating whether pending pods would fit on a hypothetical node, and if no existing node suffices, they trigger a scale-up.

This design works correctly when:

1. All pending pods arrive within a single autoscaler scan cycle (10-30 seconds)
2. The autoscaler's node template accurately declares all resources the real node will have
3. There is a 1:1 relationship between pod demand and node capacity

In production, all three assumptions routinely break. The result is over-provisioning: more nodes than necessary, more cost than necessary, and more idle resources than necessary.

## Failure modes

### Mode 1: Cross-cycle demand fragmentation

The autoscaler evaluates pending pods in discrete scan cycles. Pods that arrive in different cycles are never batched together.

**Concrete example**: Two services scale simultaneously during a traffic spike. Service A's pods arrive at T+0, Service B's pods arrive at T+12s. The autoscaler sees A's pods in cycle 1, triggers node-1. In cycle 2, it sees B's pods. Node-1 is still provisioning (NotReady). The autoscaler's simulation may or may not account for it depending on implementation. If it does not, it triggers node-2. Both services could have fit on node-1.

### Mode 2: Incomplete node template simulation

The autoscaler simulates fit using a **node template**, a static declaration derived from the node group configuration. It only contains resources the cloud provider declares at the infrastructure level (CPU, memory, ephemeral storage), not resources registered dynamically after boot:

- GPU memory partitions from device plugins (cGPU `aliyun.com/gpu-mem`, HAMi annotations)
- Extended resources from NVIDIA device plugin (`nvidia.com/gpu` counts for time-slicing)
- Custom resources from DaemonSets that register after node join

**Concrete example**: A GPU node pool uses Alibaba cGPU with 2-split (each pod gets half a GPU). Pods request `aliyun.com/gpu-mem: 24576`. The node template does not declare `gpu-mem` because it is a device-plugin annotation, not an infrastructure resource. The autoscaler provisions a new node every time, regardless of actual capacity.

### Mode 3: Scale-from-zero information vacuum

When a node pool has zero nodes, the autoscaler relies entirely on the node template. Combined with Mode 2, it cannot determine whether the upcoming node will satisfy custom resource requests.

**Concrete example**: GPU node pool scaled to zero. Notebook A arrives, triggers scale-up. Notebook B arrives 15 seconds later. The autoscaler cannot simulate whether B fits on the upcoming node (no gpu-mem in template). It triggers a second node. Cost doubles for no reason.

For how existing tools compare, see [Prior Art & Alternatives](prior-art.md).

## The insight

The over-provisioning problem is not a scheduling problem. It is a **visibility problem**. The autoscaler over-provisions because it sees pods that should be invisible during the provisioning window.

Kubernetes 1.26 introduced scheduling gates (beta, GA in 1.30). A gated pod exists in etcd but is invisible to both kube-scheduler and the autoscaler. It consumes zero scheduling cycles and triggers zero scale-up decisions.

If a coordinator can:

1. Gate pods at admission time
2. Track in-flight node capacity (including resources the node template misses)
3. Decide which pods should remain invisible until capacity arrives
4. Release pods with node affinity when capacity is confirmed

...then the autoscaler sees exactly the demand it should see: no more, no less. The scheduler places pods on confirmed capacity. No custom scheduler. No autoscaler modification. No privileged access.

This is Kompakt.

## Design constraints

Kompakt operates under a strict set of constraints that preserve the existing Kubernetes scheduling and autoscaling stack:

| Constraint | Rationale |
|---|---|
| Never modify kube-scheduler | Scheduler replacement introduces upgrade friction, breaks multi-tenant clusters, and voids managed K8s support contracts |
| Never modify cluster autoscaler config | Autoscaler configuration is owned by the platform team and often managed by the cloud provider |
| No privileged DaemonSets | Security boundary; no host access required for coordination |
| No cluster-admin RBAC | Minimal blast radius; only Pod patch and Node read |
| Webhook must return in <50ms p99 | Admission webhook latency directly impacts pod creation latency for all workloads |
| Failure mode = passthrough | If Kompakt fails, pods are created normally (ungated). Never block the cluster |

These constraints are non-negotiable. Any feature proposal that violates them is rejected regardless of benefit.
