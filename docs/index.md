---
title: Prevent Kubernetes Autoscaler Over-Provisioning
description: Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscaler over-provisioning using scheduling gates. No custom scheduler, no privileged access.
---

# Kompakt - Kubernetes Admission-Time Coordinator

**Keep your cluster compact.**

You deploy two GPU notebooks. Each needs half a GPU. One node is enough. But the cluster autoscaler provisions two nodes, because it cannot see that the second notebook fits on the node it already requested. You pay double.

This is **autoscaler over-provisioning**. It happens because the autoscaler evaluates pods one [scan cycle](glossary.md#autoscaler-concepts) at a time (every 10-30 seconds), with incomplete information about what resources incoming nodes will have.

Kompakt fixes this. It uses Kubernetes [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (a feature that makes pods invisible to the scheduler until explicitly released) to control which pods the autoscaler can see and when. No custom scheduler. No privileged DaemonSets. No vendor lock-in.

![Without vs With Kompakt](diagrams/07-without-vs-with-kompakt.svg)

## Why does the autoscaler over-provision?

Three situations cause the autoscaler to provision more nodes than necessary:

**Demand arrives across multiple scan cycles.** The autoscaler checks for unschedulable pods every 10-30 seconds. Pods arriving in different cycles are evaluated independently. If two pods arrive 15 seconds apart, the autoscaler may provision two nodes even though both pods fit on one.

**The autoscaler cannot simulate all resources.** When a node is being provisioned, the autoscaler uses a [node template](glossary.md#autoscaler-concepts) (a static model of the incoming node's resources) to predict whether pending pods will fit. But this template only contains infrastructure-level resources like CPU and memory. Resources registered by [device plugins](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/) after boot (GPU memory partitions, accelerator counts) are missing from the template. The autoscaler cannot simulate what it cannot see.

**Scale-from-zero has no information at all.** When a node pool has zero running nodes, there is no existing node to inspect. The autoscaler relies entirely on the node template. Combined with the device-plugin gap, this creates a complete information vacuum for GPU workloads.

For a deeper analysis with concrete examples, read the [problem statement](introduction/problem-statement.md).

## What Kompakt does

Kompakt coordinates pods using two rules, configured per workload class via a `PackingProfile` CRD:

**`WaitForWorkloadPacking`** (bin-packing): When your cluster has running nodes with spare capacity, Kompakt finds the smallest node that fits the pod and releases it with [node affinity](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/) pointing to that node. This packs pods tightly onto existing capacity instead of letting the autoscaler provision new nodes.

**`WaitForNodeReady`** (scale-up coordination): When the cluster needs new nodes, Kompakt lets the first pod through to trigger the autoscaler. Subsequent pods are held invisible until the new node is ready. This prevents the autoscaler from seeing multiple unschedulable pods at once and provisioning redundant nodes.

You can use one or both rules per profile. For most production clusters, both rules together provide the best cost savings.

## How it works

1. A pod is created with a label referencing a `PackingProfile`
2. Kompakt's **webhook** intercepts the pod and injects scheduling gates, making it invisible to the scheduler and autoscaler
3. The **controller** checks cluster capacity: existing nodes, in-flight nodes detected from the autoscaler, and reservations from other gated pods
4. **Rules** evaluate the pod: if existing capacity fits, release with node affinity. If an in-flight node can fit, hold the gate until it arrives. If nothing fits anywhere, release immediately so the autoscaler provisions a new node.
5. **Your existing scheduler and autoscaler** continue working untouched

For the full lifecycle walkthrough, see [How It Works](concepts/how-it-works.md).

## Quick start

Requires Kubernetes >= 1.30:

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace
```

Then create a [PackingProfile](getting-started/first-profile.md) and label your workloads. See the [installation guide](getting-started/installation.md) for full instructions.

## Supported autoscalers and clouds

Kompakt detects in-flight nodes from whichever autoscaler is running via the Kubernetes API, without cloud credentials:

| Autoscaler | Detection method | Supported clouds |
|---|---|---|
| [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) | `cluster-autoscaler-status` ConfigMap | EKS, GKE, AKS, self-managed |
| GOATScaler (Alibaba ACK's autoscaler) | `ProvisionNode` pod events | Alibaba ACK |
| [Karpenter](https://karpenter.sh/docs/) (planned) | `NodeClaim` CRD resources | EKS, AKS |
| NotReady fallback | Node objects with `Ready!=True` | All clouds, all autoscalers |

Detection is automatic. No configuration needed. See [compatibility matrix](reference/compatibility.md) for the full list.

## What Kompakt does not do

Kompakt solves one specific problem: autoscaler over-provisioning. It does not:

- Replace kube-scheduler or the cluster autoscaler
- Allocate GPU devices (use [NVIDIA device-plugin](https://kubernetes.io/docs/tasks/manage-gpus/scheduling-gpus/), [HAMi](https://github.com/Project-HAMi/HAMi), or [KAI Scheduler](https://github.com/NVIDIA/KAI-Scheduler))
- Manage resource quotas (use [Kueue](https://kueue.sigs.k8s.io/) or ResourceQuota)
- Provide gang scheduling (use [Volcano](https://volcano.sh/en/docs/) or Coscheduling)
- Federate across clusters

See [prior art](introduction/prior-art.md) for how Kompakt compares to these tools.

## Where to start

If you want to understand why autoscalers over-provision, start with the [problem statement](introduction/problem-statement.md). If you are ready to try Kompakt, go to [installation](getting-started/installation.md). If you want to see how it works under the hood first, read [how it works](concepts/how-it-works.md).

For terms you do not recognize (cGPU, HAMi, node template, scan cycle, etc.), see the [glossary](glossary.md).
