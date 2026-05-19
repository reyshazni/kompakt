# Compatibility Matrix

## Kubernetes versions

| Kubernetes version | Scheduling gates | Kompakt support |
|---|---|---|
| < 1.26 | Not available | Not supported |
| 1.26 | Alpha | Not supported |
| 1.27 - 1.29 | Beta | Not recommended (feature gate required) |
| 1.30+ | GA | Supported |

## Managed Kubernetes services

| Cloud | Service | Autoscaler | In-flight detection | Kompakt version |
|---|---|---|---|---|
| AWS | EKS | Cluster Autoscaler | CA ConfigMap | v0.1 |
| AWS | EKS | Karpenter | NodeClaim | v0.1 |
| Alibaba | ACK | Cluster Autoscaler | CA ConfigMap | v0.1 |
| Alibaba | ACK | ack-goatscaler | TBD | v0.1 (verification pending) |
| Google | GKE Standard | CA + NAP | CA ConfigMap, NAP NodePool | v0.2 |
| Azure | AKS | Cluster Autoscaler | CA ConfigMap | v0.2 |
| Azure | AKS | NAP (Karpenter) | NodeClaim | v0.2 |
| Tencent | TKE | Cluster Autoscaler | CA ConfigMap | v0.2 |
| Oracle | OKE | Cluster Autoscaler | CA ConfigMap | v0.3 |
| Cloudeka | Managed K8s | Cluster Autoscaler | CA ConfigMap | v0.3 |

### Not supported

| Cloud | Service | Reason |
|---|---|---|
| AWS | EKS Fargate | No node-level control |
| Google | GKE Autopilot | No node-level control |
| Alibaba | ACK Serverless | No node-level control |

## Scheduler compatibility

Kompakt does not modify or replace the scheduler. It works alongside any scheduler:

| Scheduler | Compatible | Notes |
|---|---|---|
| kube-scheduler (default) | Yes | Primary target |
| KAI Scheduler | Yes | Kompakt gates before KAI schedules |
| Volcano | Yes | Kompakt gates before Volcano schedules |
| Custom schedulers | Yes | As long as they respect `spec.schedulingGates` (standard K8s behavior) |

## GPU sharing compatibility

| System | Compatible | Profile type | Kompakt version |
|---|---|---|---|
| NVIDIA device-plugin | Yes | ResourceRequest | v0.1 |
| NVIDIA time-slicing | Yes | ResourceRequest | v0.1 |
| Alibaba cGPU | Yes | Annotation | v0.1 |
| HAMi | Yes | Annotation | v0.2 |
| KAI GPU sharing | Yes | Annotation | v0.2 |
| NVIDIA MIG | Planned | Planned | v0.3 |

## Workload controller compatibility

| Controller | Compatible | Notes |
|---|---|---|
| Deployment / ReplicaSet | Yes | |
| StatefulSet | Yes | Ordered creation respected |
| DaemonSet | Excluded by default | Per-node workloads are not coordination candidates |
| Job / CronJob | Yes | Composes with Kueue |
| KServe InferenceService | Yes | Underlying pods gated transparently |
| Argo Workflows | Yes | Each step pod gated individually |
| Ray RayCluster | Yes | Each worker pod gated |
| Kubeflow PyTorchJob / TFJob | Yes | Each replica gated |
| Tekton TaskRun | Yes | |
| Spark on K8s | Yes | |

## Tool compatibility

| Tool | Compatible | Relationship |
|---|---|---|
| Kueue | Yes | Complementary. Kueue handles admission/quota, Kompakt handles bin-packing |
| Karpenter | Yes | Complementary. Karpenter provisions nodes, Kompakt coordinates pod release |
| Prometheus | Yes | Kompakt exposes `/metrics` endpoint |
| Grafana | Yes | Reference dashboard provided |
| cert-manager | Yes | Can manage webhook TLS certificates |
| Istio / Linkerd | Yes | Service mesh sidecars are included in resource calculations |
