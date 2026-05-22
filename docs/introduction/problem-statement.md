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

The autoscaler evaluates pending pods in discrete scan cycles. Pods that arrive in different cycles are not batched together. Each cycle independently decides whether to scale up.

**Concrete example**: Two services scale simultaneously during a traffic spike. Service A's pods arrive at T+0, Service B's pods arrive at T+12s. The autoscaler sees A's pods in cycle 1, triggers node-1. In cycle 2, it sees B's pods. Node-1 is still provisioning (NotReady). The autoscaler's simulation may or may not account for it depending on implementation. If it does not, it triggers node-2. Both services could have fit on node-1.

### Mode 2: Incomplete node template simulation

When the autoscaler simulates whether a pending pod fits on an upcoming node, it uses a **node template** -- a static declaration of what resources the node will have. This template is derived from the node group configuration.

The template is incomplete by definition. It only contains resources the cloud provider declares at the infrastructure level: CPU, memory, ephemeral storage. It does not contain:

- GPU memory partitions added by device plugins after boot (cGPU `aliyun.com/gpu-mem`, HAMi annotations)
- Extended resources registered dynamically (NVIDIA device plugin `nvidia.com/gpu` counts for time-slicing)
- Custom resources from DaemonSets that register after node join
- Labels and annotations added by node initializers

**Concrete example**: A GPU node pool uses Alibaba cGPU with 2-split (each pod gets half a GPU). Pods request `aliyun.com/gpu-mem: 24576`. The node template does not declare `gpu-mem` because it is a device-plugin annotation, not an infrastructure resource. The autoscaler sees a pending pod requesting a resource that zero existing or upcoming nodes have. It provisions a new node every time, regardless of actual capacity.

### Mode 3: Scale-from-zero information vacuum

When a node pool has zero nodes, the autoscaler has no running node to inspect. It relies entirely on the node template. Combined with Mode 2, this creates a complete information vacuum: the autoscaler cannot determine whether the upcoming node will satisfy the pending pod's custom resource requests.

**Concrete example**: GPU node pool scaled to zero. Notebook A arrives, triggers scale-up. Notebook B arrives 15 seconds later. The autoscaler cannot simulate whether B fits on the upcoming node (no gpu-mem in template). It triggers a second node. Cost doubles for no reason.

## Why existing tools do not solve this

### Cluster Autoscaler alone

The Cluster Autoscaler is fundamentally reactive and per-cycle. It has no mechanism to coordinate demand across time. Its "upcoming node" simulation helps within a single cycle but cannot help across cycles, and fails entirely for resources not in the node template.

### Karpenter alone

Karpenter consolidates pending pods before provisioning, which helps within a single evaluation pass. However, it faces the same cross-cycle problem: pods arriving after the provisioning decision cannot be retroactively included. Karpenter also does not solve the device-plugin resource gap.

### Volcano / Scheduling frameworks

Volcano and similar batch schedulers solve a different problem: they coordinate pod groups that must be scheduled atomically (gang scheduling). They replace or extend kube-scheduler. They do not address autoscaler over-provisioning because by the time the scheduler sees the pods, the autoscaler has already made its provisioning decision.

### Kueue

Kueue manages resource quotas and admission control. It gates workloads (not pods) based on cluster-level resource budgets. It does not make node-level placement decisions and does not interact with the autoscaler's provisioning logic. Kueue answers "does the cluster have quota for this workload?" not "which specific node should this pod wait for?"

### Custom scheduler

Replacing or extending kube-scheduler does not help because the scheduling phase happens after the autoscaler's provisioning phase. By the time a custom scheduler evaluates a pod, redundant nodes have already been requested. Additionally, custom schedulers add operational complexity, vendor lock-in, and upgrade friction.

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
