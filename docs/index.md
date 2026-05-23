---
description: Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscaler over-provisioning using scheduling gates. No custom scheduler, no privileged access.
---

# Kompakt

**Keep your cluster compact.**

Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscalers from over-provisioning nodes. It uses [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA in K8s v1.30) to control when pods become visible to the scheduler and autoscaler, coordinating scale-up events across all workload types: Deployments, StatefulSets, Jobs, KServe, Argo, Ray, and anything else that creates pods.

- No custom scheduler
- No privileged DaemonSets
- No vendor lock-in

![Without vs With Kompakt](diagrams/07-without-vs-with-kompakt.svg)

## The problem

The cluster autoscaler evaluates pending pods in scan cycles (every 10-30 seconds). Pods that arrive in different cycles are not batched together, and the autoscaler cannot always simulate whether pending pods fit on nodes it has already requested. This causes over-provisioning whenever demand arrives faster than the autoscaler can batch it: GPU sharing, burst deployments, scale-from-zero, and many other scenarios. See the [problem statement](introduction/problem-statement.md) for detailed examples.

## What Kompakt does

Kompakt coordinates pods in two ways, depending on which rules you configure:

- **`WaitForWorkloadPacking`**: Bin-packs pods onto existing nodes with spare capacity, releasing each pod with node affinity for the best-fit node.
- **`WaitForNodeReady`**: Lets the first pod trigger the autoscaler, then holds subsequent pods until the new node is ready.

You can use one or both rules per profile. For most production clusters, both rules together provide the best results.

## How it works

1. **Webhook** intercepts pod creation, matches against a `PackingProfile` CRD, injects scheduling gates
2. **Controller** maintains a node ledger tracking existing capacity and pending autoscaler nodes
3. **Rules** evaluate each gated pod: release immediately, hold for incoming node, or release with node affinity
4. **Your existing scheduler and autoscaler** continue working untouched

## What Kompakt does not do

- Replace kube-scheduler or the cluster autoscaler
- Allocate GPU devices
- Manage quota or admission control
- Federate across clusters

## Next steps

- [Why Kompakt? Read the problem statement](introduction/problem-statement.md)
- [Compare with existing tools](introduction/prior-art.md)
- [Install Kompakt](getting-started/installation.md)
- [Create your first PackingProfile](getting-started/first-profile.md)
- [Understand how it works](concepts/how-it-works.md)
