# How It Works

## The core idea

Kompakt occupies the gap between admission and scheduling. It holds pods at admission time using Kubernetes [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA since v1.30) and releases them when the controller confirms that capacity is available or incoming. Everything below Kompakt (the scheduler, the autoscaler) is unmodified.

## Components

### Mutating admission webhook

A standard `MutatingAdmissionWebhook` that intercepts Pod CREATE requests. For each incoming pod:

1. Check if the pod has a `packer.kompakt.io/packing-profile` label
2. If no label: allow the pod through untouched
3. If label present: look up the named PackingProfile
4. If profile not found: **reject the pod** with error "PackingProfile not found"
5. If profile found: inject `spec.schedulingGates` with the appropriate gate names

The webhook is stateless and horizontally scalable. Target p99 latency is under 50ms. No external calls happen in the webhook hot path (the profile lookup hits the in-memory informer cache).

Failure policy is set to `Ignore` in v0.x, meaning a webhook outage does not block pod creation. Pods simply bypass Kompakt and schedule normally. In v1.0 with HA, the failure policy upgrades to `Fail`.

### Coordination controller

A controller-runtime reconciler that watches gated pods and nodes. It:

1. Maintains the [node ledger](node-ledger.md) with existing capacity and in-flight nodes from the autoscaler
2. Runs configured [rule plugins](rule-plugins.md) for each gated pod
3. When a rule determines capacity is available, removes the scheduling gate
4. Optionally injects `spec.affinity.nodeAffinity` to steer the pod to the right node

The controller runs as a single-leader with HA replicas via lease-based election.

### PackingProfile CRD

A cluster-scoped custom resource that defines:

- How to extract resource demand from pods (container requests or annotations)
- How to determine node capacity (allocatable resources or node labels)
- Which rule plugins to run
- Timeout before unconditional gate release

Pods reference a profile by name via the `packer.kompakt.io/packing-profile` label. The profile defines HOW to coordinate. The pod declares WHICH profile to use.

See [PackingProfiles](packing-profiles.md) for details.

## Request flow

When a pod is created with label `packer.kompakt.io/packing-profile: general-cpu-coordination`:

1. The API server sends the pod spec to the Kompakt webhook
2. The webhook reads the label and looks up the PackingProfile `general-cpu-coordination`
3. The webhook injects `spec.schedulingGates: [{name: "kompakt.io/awaiting-bin-pack"}]`
4. The pod is persisted in etcd with the gate. The scheduler sees the pod but skips it because it has an active scheduling gate.
5. The controller's reconcile loop picks up the gated pod
6. The controller reads the PackingProfile to determine demand (e.g., 1 CPU + 2Gi memory) and capacity source (e.g., node allocatable)
7. The controller queries the node ledger: is there existing capacity? Is a new node coming from the autoscaler?
8. If capacity is available (or an in-flight node will provide it), the controller reserves the capacity in the ledger, removes the scheduling gate, and optionally adds node affinity
9. The scheduler picks up the now-ungated pod and binds it to a node
10. If the reservation times out (default 3 minutes), the gate is released unconditionally. The pod schedules normally without coordination.

## What the scheduler and autoscaler see

The scheduler sees pods transition from `SchedulingGated` to `Pending` to `Running`. From its perspective, pods simply appeared later than usual. It does not know Kompakt exists.

The autoscaler sees fewer simultaneously-unschedulable pods because Kompakt releases them in coordinated batches. Instead of seeing 6 unschedulable pods from different Deployments and provisioning 6 nodes, the autoscaler sees 2-3 unschedulable pods at a time and provisions 2-3 nodes. The remaining pods are released once those nodes are confirmed.

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
| Deployment (via ReplicaSet) | Each pod gated individually, bin-packed with siblings |
| StatefulSet | Each pod gated, ordered creation respected |
| DaemonSet | Excluded by default (per-node workloads are not coordination candidates) |
| Job, CronJob | Gated, composes with Kueue if present |
| KServe InferenceService | Underlying Deployment pods gated transparently |
| Argo Workflow | Each step pod gated individually |
| Ray RayCluster | Each worker pod gated |
| Kubeflow PyTorchJob / TFJob | Each replica gated |
| Plain Pod | Gated if label is present |
