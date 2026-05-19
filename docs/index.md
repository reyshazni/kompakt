# Kompakt

**Keep your cluster compact.**

Kompakt is a Kubernetes admission-time coordinator that prevents cluster autoscalers from over-provisioning nodes. It gates pods via [scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/) (GA in K8s v1.30) and bin-packs scale-up events across all workload types: Deployments, StatefulSets, Jobs, KServe, Argo, Ray, and anything else that creates pods.

- No custom scheduler
- No privileged DaemonSets
- No vendor lock-in
- Works on EKS, GKE, AKS, ACK, and TKE

## The problem

When multiple unschedulable pods appear at the same time, the cluster autoscaler groups them independently and frequently provisions one node per pod, even when they could share. With topology spread constraints, pod affinity, or fractional-GPU annotations, the over-provisioning gets severe: **30-60% extra nodes is typical**, with extreme cases hitting 10x.

This affects every major cloud running the upstream cluster autoscaler.

## How Kompakt solves it

1. **Webhook** intercepts pod creation, matches against `PackingProfile` CRDs, injects `spec.schedulingGates`
2. **Controller** maintains an in-flight node ledger tracking existing capacity and pending autoscaler nodes
3. **Gate released** with node affinity when capacity is available
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
