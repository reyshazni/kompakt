---
title: Prevent Kubernetes Autoscaler Over-Provisioning
description: Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscaler over-provisioning using scheduling gates. No custom scheduler, no privileged access.
---

# Kompakt - Kubernetes Admission-Time Coordinator

**Keep your cluster compact.**

Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscalers from over-provisioning nodes. It uses [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA in K8s v1.30) to control when pods become visible to the scheduler and autoscaler, coordinating scale-up events across all workload types: Deployments, StatefulSets, Jobs, KServe, Argo, Ray, and anything else that creates pods.

- No custom scheduler
- No privileged DaemonSets
- No vendor lock-in

![Without vs With Kompakt](diagrams/07-without-vs-with-kompakt.svg)

## The problem

The cluster autoscaler evaluates pending pods in **scan cycles** (every 10-30 seconds). Pods that arrive in different cycles are not batched together. When a node is being provisioned but not yet Ready, the autoscaler simulates whether pending pods fit on it, but this simulation only works for resources declared in the node template.

This causes over-provisioning whenever demand arrives faster than the autoscaler can batch it:

- **Fractional GPU sharing**: Two notebooks each need half a GPU. One node is enough, but the autoscaler's node template does not declare `gpu-mem`. It provisions a second GPU node. You pay double.
- **Burst deployments**: Three services scale simultaneously. The autoscaler sees them in separate scan cycles and provisions nodes independently instead of packing them together.
- **Scale-from-zero**: A node pool has zero nodes. Two requests arrive within seconds. The first triggers a node, the second cannot see it and triggers another.

These are not bugs in the autoscaler. The problem is that no one coordinates demand across scan cycles. See the [problem statement](introduction/problem-statement.md) for a detailed analysis.

## What Kompakt does

Kompakt coordinates pods in two ways, depending on which rules you configure:

- **`WaitForWorkloadPacking`** (bin-packing): When your cluster has running nodes with spare capacity, Kompakt finds the best-fit node and releases the pod with node affinity. This minimizes wasted resources across existing nodes by packing pods onto the smallest sufficient node.
- **`WaitForNodeReady`** (scale-up coordination): When the cluster needs new nodes, Kompakt lets the first pod through to trigger the autoscaler, then holds subsequent pods until the new node is ready. This prevents the autoscaler from seeing multiple unschedulable pods and provisioning redundant nodes.

You can use one or both rules per profile. For most production clusters, both rules together provide the best cost savings.

## How it works

1. **Webhook** intercepts pod creation, matches against a `PackingProfile` CRD, and injects scheduling gates
2. **Controller** maintains a node ledger tracking existing capacity and in-flight nodes detected from the autoscaler
3. **Rules** evaluate each gated pod: release immediately if no capacity exists anywhere (passthrough), hold if an in-flight node can fit the pod, or release with node affinity to a confirmed node
4. **Your existing scheduler and autoscaler** continue working untouched. The scheduler places pods on confirmed capacity. The autoscaler sees only the demand it should see.

## Quick start

Requires Kubernetes >= 1.30:

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace
```

Then create a [PackingProfile](getting-started/first-profile.md) and label your workloads. See the [installation guide](getting-started/installation.md) for details.

## Supported autoscalers and clouds

Kompakt is autoscaler-aware, not cloud-aware. It detects in-flight nodes from whichever autoscaler is running via the Kubernetes API, without cloud credentials.

| Autoscaler | Detection method | Supported clouds |
|---|---|---|
| Cluster Autoscaler | `cluster-autoscaler-status` ConfigMap | EKS, GKE, AKS, self-managed |
| GOATScaler | `ProvisionNode` pod events | Alibaba ACK |
| Karpenter (planned) | `NodeClaim` CRD resources | EKS, AKS |
| NotReady fallback | Node objects with Ready!=True | All clouds, all autoscalers |

Detection is automatic. No configuration needed. See [compatibility matrix](reference/compatibility.md) for the full list of supported Kubernetes versions, managed services, GPU sharing systems, and workload controllers.

## Supported workload types

Kompakt operates at the Pod level. The webhook intercepts Pod CREATE events regardless of which controller created the pod:

| Workload type | How it works |
|---|---|
| Deployment, StatefulSet | Each pod gated individually |
| Job, CronJob | Gated, composes with Kueue if present |
| KServe InferenceService | Underlying pods gated transparently |
| Argo Workflow | Each step pod gated individually |
| Ray RayCluster | Each worker pod gated |
| Kubeflow PyTorchJob / TFJob | Each replica gated |
| DaemonSet | Excluded by default |

## What Kompakt does not do

Kompakt solves one specific problem: autoscaler over-provisioning caused by demand arriving faster than nodes. It does not:

- Replace kube-scheduler or the cluster autoscaler
- Allocate GPU devices (use NVIDIA device-plugin, HAMi, or KAI)
- Manage resource quotas (use Kueue or ResourceQuota)
- Provide gang scheduling (use Volcano or Coscheduling)
- Federate across clusters

See [prior art](introduction/prior-art.md) for how Kompakt compares to these tools and when to use each.

## Next steps

- [Why Kompakt? Read the problem statement](introduction/problem-statement.md)
- [Compare with existing tools](introduction/prior-art.md)
- [Install Kompakt](getting-started/installation.md)
- [Create your first PackingProfile](getting-started/first-profile.md)
- [Understand how it works](concepts/how-it-works.md)
