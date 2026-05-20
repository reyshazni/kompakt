# Kompakt

**Keep your cluster compact.**

Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscalers from over-provisioning nodes. It uses [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA in K8s v1.30) to control when pods become visible to the scheduler and autoscaler, coordinating scale-up events across all workload types: Deployments, StatefulSets, Jobs, KServe, Argo, Ray, and anything else that creates pods.

- No custom scheduler
- No privileged DaemonSets
- No vendor lock-in
- Works on EKS, GKE, AKS, ACK, and TKE

## The problem

When multiple unschedulable pods appear at the same time, the cluster autoscaler groups them independently and frequently provisions one node per pod, even when they could share. With topology spread constraints, pod affinity, or fractional-GPU annotations, the over-provisioning gets severe: **30-60% extra nodes is typical**, with extreme cases hitting 10x.

This affects every major cloud running the upstream cluster autoscaler.

## What Kompakt does

Kompakt coordinates pods in two ways, depending on which rules you configure:

**Pack onto existing capacity** (`BinPackOnInflightCapacity`): When your cluster has running nodes with spare capacity, Kompakt finds the best-fit node and releases the pod with node affinity. Minimizes wasted resources across existing nodes.

**Coordinate scale-ups** (`WaitForScaleUp`): When the cluster needs new nodes, Kompakt lets the first pod through to trigger the autoscaler, then holds subsequent pods until the new node is ready. Prevents the autoscaler from provisioning redundant nodes.

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

- [Install Kompakt](getting-started/installation.md)
- [Create your first PackingProfile](getting-started/first-profile.md)
- [Understand how it works](concepts/how-it-works.md)
