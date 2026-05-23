# Prior Art and Alternatives

This page compares Kompakt against existing Kubernetes scheduling and autoscaling tools to help platform engineers decide whether Kompakt solves a problem their current stack does not.

## Comparison matrix

| Capability | Cluster Autoscaler | Karpenter | Volcano | Kueue | Kompakt |
|---|---|---|---|---|---|
| Cross-cycle demand coordination | No | Partial | No | No | Yes |
| Device-plugin resource awareness | No | No | No | No | Yes |
| Scale-from-zero GPU coordination | No | No | No | No | Yes |
| Works with any scheduler | N/A | N/A | No (replaces) | Yes | Yes |
| Works with any autoscaler | N/A | N/A | Yes | Yes | Yes |
| No privileged access required | Yes | Yes | Varies | Yes | Yes |
| Pod-level (not workload-level) | N/A | N/A | No | No | Yes |
| Admission-time (pre-scheduler) | No | No | No | Yes | Yes |

## Detailed comparisons

### vs. Cluster Autoscaler (standalone)
CA watches for pending pods and provisions nodes from pre-configured node groups.

- Cannot coordinate pods across scan cycles (10-30s boundary is hard)
- Cannot simulate device-plugin resources or labels added post-boot
- Cannot track in-flight node capacity for resources outside the node template

Kompakt does not replace CA. It controls which pods CA can see, ensuring CA receives the right demand at the right time.

### vs. Karpenter
Karpenter replaces CA with a pod-aware provisioner that evaluates all pending pods together.

- Cannot include pods that arrive after a provisioning decision is made
- Has the same device-plugin resource gap as CA
- Requires pods to be pending simultaneously; no coordination across time

Kompakt works alongside Karpenter, gating pods so Karpenter sees consolidated demand rather than trickle demand.

### vs. Volcano
Volcano provides gang scheduling and job lifecycle management for HPC and AI/ML workloads.

- Operates at scheduler level, after the autoscaler has already acted
- Requires pods to be in the same PodGroup; no cross-workload coordination
- Replaces kube-scheduler for affected pods

Complementary: Volcano handles gang semantics, Kompakt handles visibility control. A cluster can run both.

### vs. Kueue
Kueue provides workload-level admission control, resource quotas, and fair-sharing.

- Works at cluster quota level, not node-level placement
- Does not track in-flight nodes or device-plugin resources
- Gates workloads, not individual pods

Complementary: Kueue decides "does the cluster have budget?", Kompakt decides "which node should this pod wait for?"

### vs. Custom scheduler / scheduler extender
Custom schedulers add arbitrary placement logic (topology-aware, data-locality, etc.).

- Scheduling happens after autoscaler provisioning; too late to prevent over-provisioning
- The autoscaler acts on all pending pods before the scheduler can intervene
- Changing `schedulerName` creates operational and multi-tenant friction

Fully compatible. Kompakt does not set `schedulerName` and works with any scheduler.

## When Kompakt is NOT the right tool
Kompakt solves one specific problem: autoscaler over-provisioning caused by demand arriving faster than nodes. Do not use Kompakt for:

| Problem | Better tool |
|---|---|
| Gang scheduling (all-or-nothing placement) | Volcano, Coscheduling plugin |
| Resource quotas across teams | Kueue, ResourceQuota |
| Job queuing and priority | Kueue |
| Pod topology spread | kube-scheduler topology constraints |
| Node selection / instance type choice | Karpenter, CA expanders |
| GPU device allocation | NVIDIA device-plugin, HAMi, KAI |
| Multi-cluster federation | Admiralty, Liqo |

## Decision flowchart

1. Are you seeing more nodes provisioned than necessary during scale-up events? If no, Kompakt will not help.
2. Is the over-provisioning caused by pods arriving in separate autoscaler scan cycles? If yes, Kompakt's `WaitForNodeReady` rule directly solves this. If unsure, check your autoscaler logs for multiple scale-up decisions within 1-5 minutes.
3. Is the over-provisioning caused by device-plugin resources missing from node templates? If yes, Kompakt's `nodeGroupTemplates` fills the gap.
4. Do you have a scale-from-zero scenario where the autoscaler cannot simulate upcoming node capacity? If yes, Kompakt's in-flight detection + template enrichment solves this. If no, try tuning autoscaler parameters (scan interval, expander strategy).

If Kompakt fits your problem, [install it](../getting-started/installation.md) and [create your first profile](../getting-started/first-profile.md).
