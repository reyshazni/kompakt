# Prior Art and Alternatives

This page compares Kompakt against existing Kubernetes scheduling and autoscaling tools. The goal is to help platform engineers decide whether Kompakt solves a problem their current stack does not.

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

The Cluster Autoscaler (CA) is the default autoscaling solution on most managed Kubernetes platforms. It watches for pending pods and provisions nodes from pre-configured node groups.

**What CA does well:**

- Reacts to pending pods and provisions appropriate node types
- Scales down underutilized nodes
- Supports priority-based expanders for multi-node-group selection
- Handles basic "upcoming node" simulation within a single scan cycle

**What CA cannot do:**

- Coordinate pods across scan cycles (10-30s boundary is hard)
- Simulate resources not in the node template (device-plugin resources, labels added post-boot)
- Hold a pod invisible until a better placement is available
- Track actual in-flight node capacity for resources it does not know about

**Kompakt's relationship to CA:** Kompakt does not replace CA. It sits in front of it, controlling which pods CA can see. CA continues to make all provisioning decisions. Kompakt just ensures CA sees the right demand at the right time.

### vs. Karpenter

Karpenter is a node provisioning system that replaces the Cluster Autoscaler with a more flexible, pod-aware provisioner. It evaluates all pending pods together and provisions nodes to fit them.

**What Karpenter does well:**

- Evaluates multiple pending pods in a single provisioning pass (better batching than CA)
- Selects instance types dynamically based on pod requirements
- Consolidates workloads onto fewer nodes over time
- Supports custom resource fitting via NodePool constraints

**What Karpenter cannot do:**

- Retroactively include pods that arrive after a provisioning decision is made
- Solve the device-plugin resource gap (same template limitation)
- Coordinate across time (pods must be pending simultaneously)
- Work alongside Cluster Autoscaler (it replaces CA)

**Kompakt's relationship to Karpenter:** Kompakt works alongside Karpenter. By gating pods until in-flight capacity is confirmed, Kompakt ensures Karpenter sees consolidated demand rather than trickle demand. This is especially valuable during scale-from-zero where Karpenter's batching window is too short to capture all related pods.

### vs. Volcano

Volcano is a batch scheduling system for Kubernetes. It provides gang scheduling, fair-share scheduling, and job lifecycle management for HPC and AI/ML workloads.

**What Volcano does well:**

- Gang scheduling: all-or-nothing pod group placement
- Queue-based admission with priority and fair-share
- Job-level lifecycle management (retry, dependency, DAG)
- Integration with training frameworks (TensorFlow, PyTorch, MPI)

**What Volcano does not solve:**

- Autoscaler over-provisioning (Volcano operates at scheduler level, after autoscaler)
- Cross-workload coordination (pods must be in the same PodGroup)
- Dynamic resource awareness (no device-plugin resource tracking)
- Works only with its own scheduler (replaces kube-scheduler for affected pods)

**Kompakt's relationship to Volcano:** Complementary. Volcano handles "schedule these pods together" (gang semantics). Kompakt handles "don't trigger redundant nodes while we wait" (visibility control). A cluster can run both: Kompakt gates pods pre-admission, Volcano schedules them post-admission.

### vs. Kueue

Kueue is a Kubernetes-native job queuing system. It provides workload-level admission control, resource quotas, and fair-sharing across namespaces and teams.

**What Kueue does well:**

- Cluster-level resource quota management
- Workload admission gating (hold workloads until resources are available)
- Fair-share across teams and namespaces
- Integration with Job, RayJob, MPIJob, and other workload APIs

**What Kueue does not solve:**

- Node-level placement decisions (it works at cluster quota level)
- In-flight node tracking (it does not know about upcoming nodes)
- Device-plugin resource awareness (it uses standard resource quotas)
- Pod-level coordination (it gates workloads, not individual pods)
- Autoscaler interaction (it admits workloads, autoscaler provisions for them)

**Kompakt's relationship to Kueue:** Complementary at different layers. Kueue decides "does the cluster have budget for this workload?" Kompakt decides "which specific node should this pod wait for?" A cluster can run both: Kueue admits the workload, Kompakt coordinates the pod-level placement.

### vs. Custom scheduler / scheduler extender

Some teams write custom schedulers or scheduler extenders to solve placement problems.

**What custom schedulers enable:**

- Arbitrary placement logic (topology-aware, data-locality, etc.)
- Integration with external systems for placement decisions
- Fine-grained control over binding

**What custom schedulers cannot solve:**

- Autoscaler over-provisioning (scheduling happens after autoscaler provisioning)
- The visibility problem (scheduler sees all pending pods, autoscaler acts before scheduler)
- Operational complexity (scheduler upgrades, compatibility with managed K8s)
- Multi-tenant friction (changing schedulerName affects all pods)

**Kompakt's relationship to custom schedulers:** Fully compatible. Kompakt does not set `schedulerName`. It gates pods before any scheduler sees them and releases them for whatever scheduler the pod is configured to use. Works with default scheduler, Volcano, KAI, or any custom scheduler.

## When Kompakt is NOT the right tool

Kompakt is not a general-purpose scheduling solution. It solves one specific problem: autoscaler over-provisioning caused by demand arriving faster than nodes. Do not use Kompakt for:

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

Use this to determine if Kompakt helps your specific problem:

1. Are you seeing more nodes provisioned than necessary during scale-up events?
   - No: Kompakt will not help. Your autoscaler is working correctly.
   - Yes: Continue.

2. Is the over-provisioning caused by pods arriving in separate autoscaler scan cycles?
   - Yes: Kompakt's `WaitForNodeReady` rule directly solves this.
   - Not sure: Check your autoscaler logs for multiple scale-up decisions within 1-5 minutes.

3. Is the over-provisioning caused by device-plugin resources missing from node templates?
   - Yes: Kompakt's `nodeGroupTemplates` fills the gap.
   - No: Continue.

4. Do you have a scale-from-zero scenario where the autoscaler cannot simulate upcoming node capacity?
   - Yes: Kompakt's combination of in-flight detection + template enrichment solves this.
   - No: Your problem may be better solved by tuning autoscaler parameters (scan interval, expander strategy).
