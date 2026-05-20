# How It Works

## The core idea

Kompakt occupies the gap between admission and scheduling. It holds pods at admission time using Kubernetes [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA since v1.30) and releases them when rules confirm that capacity is available, incoming, or needs to be requested. Everything below Kompakt (the scheduler, the autoscaler) is unmodified.

## Components

### Mutating admission webhook

A standard `MutatingAdmissionWebhook` that intercepts Pod CREATE requests. For each incoming pod:

1. Check if the pod has a `packer.kompakt.io/packing-profile` label
2. If no label: allow the pod through untouched
3. If label present: look up the named PackingProfile
4. If profile not found: **reject the pod** with error "PackingProfile not found"
5. If profile found: inject `spec.schedulingGates` with the appropriate gate names and a `kompakt.io/trace-id` annotation for end-to-end correlation

Each rule in the profile maps to a distinct gate name:

| Rule | Gate |
|---|---|
| BinPackOnInflightCapacity | `kompakt.io/awaiting-bin-pack` |
| WaitForScaleUp | `kompakt.io/awaiting-scale-up` |

If a profile specifies both rules, both gates are injected. Each gate is managed independently by the corresponding rule.

The webhook is stateless and horizontally scalable. Target p99 latency is under 50ms. No external calls happen in the webhook hot path (the profile lookup hits the in-memory informer cache).

Failure policy is set to `Ignore` in v0.x, meaning a webhook outage does not block pod creation. Pods simply bypass Kompakt and schedule normally. In v1.0 with HA, the failure policy upgrades to `Fail`.

### Coordination controller

A controller-runtime reconciler that watches gated pods and nodes. For each gated pod, it:

1. Syncs the [node ledger](node-ledger.md) with current cluster state (existing nodes, pod usage, in-flight nodes from autoscaler)
2. Runs the profile's [rule plugins](rule-plugins.md) in order
3. Based on rule decisions: releases gates, holds gates, or releases with node affinity
4. Emits Kubernetes Events on the pod (`GateReleased`, `GateHeld`) visible in `kubectl describe pod`
5. Annotates the pod with status (`kompakt.io/gate-reason`, `kompakt.io/target-node`, `kompakt.io/held-by`)
6. Updates the PackingProfile status with conditions (`Ready`, `ProfileValid`, `LedgerReady`, `InflightDetectionActive`)

The controller runs as a single-leader with HA replicas via lease-based election.

### PackingProfile CRD

A cluster-scoped custom resource that defines:

- How to extract resource demand from pods (container requests or annotations)
- How to determine node capacity (allocatable resources or node labels)
- Expected capacity for in-flight nodes (`nodeGroupTemplates`)
- Which rule plugins to run
- Timeout before unconditional gate release

Pods reference a profile by name via the `packer.kompakt.io/packing-profile` label. The profile defines HOW to coordinate. The pod declares WHICH profile to use.

See [PackingProfiles](packing-profiles.md) for details.

## Request flow

### Scenario 1: Existing node has capacity (BinPack)

1. Pod A is created with label `packer.kompakt.io/packing-profile: my-profile`
2. Webhook injects `kompakt.io/awaiting-bin-pack` gate
3. Controller syncs ledger: node-1 has 4 CPU free
4. BinPack rule finds node-1 fits Pod A's 1 CPU demand, reserves the slot
5. Controller releases gate, injects `nodeAffinity` pointing to node-1
6. Scheduler places Pod A on node-1
7. Pod A event: `GateReleased: gate released, reason=capacity, targetNode=node-1`

### Scenario 2: No capacity, need scale-up (WaitForScaleUp)

1. Pod A is created, no nodes have capacity, no in-flight nodes exist
2. WaitForScaleUp rule: no capacity anywhere -- **release immediately** (passthrough)
3. Pod A becomes visible to the autoscaler, which triggers a scale-up
4. Node starts provisioning. Kompakt detects this from the `cluster-autoscaler-status` ConfigMap
5. Pod B is created while the node is provisioning
6. WaitForScaleUp rule: in-flight node can fit Pod B -- **hold the gate**, reserve capacity on the incoming node
7. Pod B stays invisible to the autoscaler. No second scale-up triggered.
8. Node becomes Ready, Pod A starts running
9. Controller re-evaluates Pod B: existing node now has capacity -- **release with node affinity**
10. Scheduler places Pod B on the same node

Result: 1 node instead of 2.

### Scenario 3: Timeout safety net

If a pod stays gated longer than `reservationTimeout` (default 3m), the gate is released unconditionally. This handles edge cases: node provisioning fails, autoscaler stalls, resource estimates were wrong. The pod schedules normally without coordination.

## What the scheduler and autoscaler see

The scheduler sees pods transition from `SchedulingGated` to `Pending` to `Running`. From its perspective, pods simply appeared later than usual. It does not know Kompakt exists.

The autoscaler sees fewer simultaneously-unschedulable pods because Kompakt controls when pods become visible. Instead of seeing 6 unschedulable pods and provisioning 6 nodes, the autoscaler sees 1-2 unschedulable pods at a time, provisions 1-2 nodes, and the remaining pods are released once those nodes arrive.

## Observability

Kompakt provides multiple layers of visibility:

**Pod level** (kubectl describe pod):

- Events: `GateHeld` (which rule is holding), `GateReleased` (reason, target node)
- Annotations: `kompakt.io/held-by` (while gated), `kompakt.io/gate-reason` and `kompakt.io/target-node` (after release)
- Trace ID: `kompakt.io/trace-id` for correlating webhook and controller logs

**Profile level** (kubectl get packingprofiles):

```
NAME                 DEMAND            RULES                                          GATES   INFLIGHT   READY   AGE
general-cpu-coord    ResourceRequest   ["BinPackOnInflightCapacity","WaitForScaleUp"]  3       2          True    1h
```

**Profile conditions** (kubectl describe packingprofile):

| Condition | Meaning |
|---|---|
| `Ready` | Profile is valid and ledger is synced |
| `ProfileValid` | Configuration has no errors |
| `LedgerReady` | Last cluster state sync succeeded |
| `InflightDetectionActive` | In-flight nodes detected from autoscaler |

**Prometheus metrics**: Gate hold duration, release counts by reason, rule evaluation latency, ledger node counts. See [Metrics](../reference/metrics.md).

## Zero scheduler integration

This is the project's defining constraint. Kompakt does not:

- Modify kube-scheduler config or flags
- Install as a scheduler-extender
- Replace the default scheduler
- Set `schedulerName` on user pods
- Modify cluster-autoscaler config or flags
- Require privileged DaemonSets
- Require cluster-admin RBAC

Every design decision in Kompakt respects this constraint. The practical benefit: installation is a Helm chart, uninstall is deleting the webhook configuration, and your cluster reverts to pre-Kompakt behavior within seconds.

## Workload universality

Kompakt operates at the Pod level. The webhook intercepts Pod CREATE events regardless of what controller created the pod. Any pod with the `packer.kompakt.io/packing-profile` label is coordinated:

| Workload type | How it works |
|---|---|
| Deployment (via ReplicaSet) | Each pod gated individually, coordinated with siblings |
| StatefulSet | Each pod gated, ordered creation respected |
| DaemonSet | Excluded by default (per-node workloads are not coordination candidates) |
| Job, CronJob | Gated, composes with Kueue if present |
| KServe InferenceService | Underlying Deployment pods gated transparently |
| Argo Workflow | Each step pod gated individually |
| Ray RayCluster | Each worker pod gated |
| Kubeflow PyTorchJob / TFJob | Each replica gated |
| Plain Pod | Gated if label is present |
