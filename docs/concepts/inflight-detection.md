# In-flight Node Detection

Kompakt needs to know about nodes that are being provisioned but not yet Ready. This is how WaitForScaleUp avoids double-provisioning: if the autoscaler is already bringing up a node, Kompakt holds subsequent pods instead of letting the autoscaler provision another one.

## Autoscaler-aware, not cloud-aware

Kompakt does not care which cloud your cluster runs on. It cares which autoscaler is running and how that autoscaler signals scale-up events. All data comes from the Kubernetes API. Kompakt never calls cloud APIs and never requires cloud credentials.

Detection works in two layers that run in parallel:

**Layer 1: Autoscaler-aware** detects scale-up before the Node object exists in Kubernetes. This covers the critical window between the autoscaler's decision and node registration.

| Detector | Autoscaler | Signal | Clouds |
|---|---|---|---|
| ClusterAutoscalerDetector | Upstream CA | `cluster-autoscaler-status` ConfigMap | EKS, GKE, AKS, self-managed |
| GOATScalerDetector | ACK GOATScaler | `ProvisionNode` pod events | Alibaba ACK |
| KarpenterDetector (planned) | Karpenter | `NodeClaim` CRD resources | EKS, AKS (NAP) |

**Layer 2: Node-based** detects nodes that exist in Kubernetes but have never been Ready. This covers the secondary window while the node initializes (GPU driver, device plugin, CNI). Typically 2-5 minutes for GPU nodes. Works on every cloud and every autoscaler, including custom autoscalers that Kompakt does not know about.

| Detector | Signal | Clouds |
|---|---|---|
| NotReadyNodeDetector | Nodes where Ready!=True and never been Ready | All |

Both layers run on every reconcile. Results are deduplicated by node name. Layer 1 provides earlier and richer data. Layer 2 serves as a universal safety net.

One autoscaler can serve multiple clouds (CA runs on EKS, GKE, and AKS). One cloud can have multiple autoscaler options (ACK supports both GOATScaler and upstream CA).

## Auto-discovery

All detectors run in parallel on every reconcile cycle. Each detector probes for its signal source:

- Signal found: parse and return in-flight nodes
- Signal not found: return empty (not an error)

No configuration needed. On ACK, the CA detector finds no ConfigMap and returns nothing; the GOATScaler detector finds `ProvisionNode` events and returns in-flight nodes. On EKS with CA, the CA detector finds the ConfigMap; the GOATScaler detector finds no events. Both work automatically.

## Cluster Autoscaler detector

Reads the `cluster-autoscaler-status` ConfigMap in `kube-system`. This ConfigMap contains the names of node groups being scaled and how many nodes are pending.

The ConfigMap is written by the upstream Cluster Autoscaler and follows a standard format across clouds. If the ConfigMap does not exist (e.g., the cluster uses a different autoscaler), the detector silently returns nothing.

## GOATScaler detector

Watches Kubernetes Events on pods where `source.component` is `GOATScaler` and `reason` is `ProvisionNode`. This is the earliest signal that a scale-up is happening on ACK. The event fires when GOATScaler decides to provision, before the ECS API call, before the Node object exists in Kubernetes.

The event message contains the expected node name, availability zone, and instance type:

```
Provision node asa-xxx in Zone: ap-southeast-5a with InstanceType: ecs.gn8is.4xlarge
```

The instance type is matched against `nodeGroupTemplates` in the PackingProfile to determine expected allocatable resources.

## Template enrichment

Detected in-flight nodes often have unknown allocatable resources (the node does not exist yet, so there is nothing to read). The `nodeGroupTemplates` field in `capacitySource` provides this information:

```yaml
capacitySource:
  type: NodeAllocatable
  resources: [cpu, memory]
  nodeGroupTemplates:
    - instanceType: ecs.gn8is.4xlarge
      allocatable:
        aliyun.com/gpu-mem: 49152
    - namePrefix: pool-cpu-4xlarge
      allocatable:
        cpu: 16000
        memory: 64000000000
```

Matching priority:

1. `instanceType`: matched against the instance type from GOATScaler events
2. `namePrefix`: matched against the node name from CA status

Without templates, in-flight nodes have unknown capacity and WaitForScaleUp cannot hold pods for them.

## Fallback behavior

If neither layer detects in-flight nodes (e.g., the autoscaler is completely unknown and nodes appear instantly as Ready), WaitForScaleUp still works. It uses passthrough: the first pod is released immediately to trigger the autoscaler, subsequent pods are released when the node becomes Ready (detected as an existing node by the regular ledger sync). This is slower but still prevents over-provisioning through coordinated release.
