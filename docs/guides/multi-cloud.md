# Multi-cloud Setup

## When you need this

You run Kubernetes clusters across multiple cloud providers and want consistent autoscaler coordination everywhere. Common scenarios:

- Multiple clouds for redundancy or regulatory reasons (e.g., ACK in Asia, EKS in US)
- Migrating workloads between clouds
- Teams on different clouds that want the same tooling

## Supported clouds

| Cloud | Autoscaler | In-flight detection | Status |
|---|---|---|---|
| Amazon EKS | CA or Karpenter | CA ConfigMap, Karpenter NodeClaim | v0.1 |
| Alibaba ACK (Alibaba Cloud Container Service for Kubernetes) | CA or ack-goatscaler | CA ConfigMap, goatscaler TBD | v0.1 |
| Google GKE Standard | CA + NAP (Node Auto-Provisioning) | CA ConfigMap, NAP NodePool | v0.2 |
| Azure AKS | CA or NAP (Karpenter) | CA ConfigMap, Karpenter NodeClaim | v0.2 |
| Tencent TKE | CA | CA ConfigMap | v0.2 |
| Oracle OKE | CA | CA ConfigMap | v0.3 |
| Cloudeka | CA | CA ConfigMap | v0.3 |

!!! note
    GKE Autopilot, EKS Fargate, and ACK Serverless are not supported. These environments lack node-level control, which Kompakt requires to coordinate capacity.

## Setup

Install Kompakt the same way on every cloud:

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt -n kompakt-system --create-namespace
```

No cloud-specific configuration needed. In-flight detection adapters are auto-detected based on what resources exist in the cluster.

## Cloud-specific notes

### Amazon EKS

**With Cluster Autoscaler**: Standard installation. The CA `cluster-autoscaler-status` ConfigMap is detected automatically.

**With Karpenter**: Kompakt detects `NodeClaim` resources automatically. Both Kompakt and Karpenter can run simultaneously. Karpenter already handles some multi-pod bin-packing. Kompakt adds coordination across different workload types and topology constraints that Karpenter does not optimize for.

### Alibaba ACK

**With Cluster Autoscaler**: Standard installation. Works the same as EKS with CA.

**With ack-goatscaler**: Support for scheduling gates is under verification. If goatscaler does not respect `spec.schedulingGates`, Kompakt falls back to a `nodeSelector` workaround. Check the [compatibility matrix](../reference/compatibility.md) for the latest status.

**cGPU workloads**: Alibaba cGPU is a primary use case. See the [GPU packing guide](gpu-packing.md) for the `alibaba-cgpu` PackingProfile.

### Google GKE Standard

Standard installation. GKE's Node Auto-Provisioning (NAP) creates new node pools when existing pools cannot satisfy demand. Kompakt detects this via the NAP adapter.

GKE 1.34 introduced post-hoc node consolidation. Kompakt and GKE consolidation are complementary: Kompakt prevents over-provisioning at scale-up time, GKE consolidation removes underutilized nodes after the fact.

### Azure AKS

**With Cluster Autoscaler**: Standard installation.

**With NAP (Karpenter-based)**: AKS NAP uses Karpenter under the hood. Kompakt detects `NodeClaim` resources automatically, same as EKS with Karpenter.

### Tencent TKE

Standard installation. TKE's Most-Pods and Least-Waste expanders help with single-deployment scale-ups but do not solve the cross-deployment equivalence group problem. Kompakt addresses that gap.

## What to expect

The same PackingProfile works on every cloud. No cloud-specific fields, no cloud-specific configuration. The only difference is which in-flight detection adapter is active, and that is auto-detected.

If you manage clusters across multiple clouds, you can use the same set of PackingProfile manifests everywhere:

```bash
kubectl apply -f packingprofiles/ --context=eks-prod
kubectl apply -f packingprofiles/ --context=ack-prod
kubectl apply -f packingprofiles/ --context=gke-prod
```

## Next steps

- [Compatibility matrix](../reference/compatibility.md) for the full support table
- [In-flight detection](../concepts/inflight-detection.md) for how each adapter works
