# In-flight Node Detection

Kompakt needs to know about nodes that are being provisioned but are not yet Ready. This is how the controller avoids double-provisioning: if the autoscaler is already bringing up a node, Kompakt can reserve capacity on that incoming node instead of letting the autoscaler provision yet another one.

## How it works

The controller runs cloud-specific adapters that watch for signals indicating a node is being provisioned. Each adapter reads publicly available Kubernetes resources. Kompakt never calls cloud APIs directly and never requires cloud-specific credentials.

When an adapter detects an in-flight node, it reports the expected capacity (instance type, resource allocatable) to the [node ledger](node-ledger.md). The ledger then includes this capacity in bin-packing decisions.

## Adapters

### Cluster Autoscaler (CA)

**Supported clouds**: All (EKS, GKE, AKS, ACK, and TKE)

The upstream cluster autoscaler writes a `cluster-autoscaler-status` ConfigMap in `kube-system`. This ConfigMap contains information about pending scale-up events, including:

- Which node groups are scaling
- How many nodes are being added
- The instance type and expected capacity

Kompakt reads this ConfigMap (read-only) to detect in-flight nodes.

```yaml
# What Kompakt reads (not what you configure)
# kube-system/cluster-autoscaler-status ConfigMap
data:
  status: |
    ScaleUp:
      - nodeGroup: pool-cpu-4xlarge
        newSize: 5
        oldSize: 3
```

### Karpenter

**Supported clouds**: AWS (EKS), Azure (AKS with NAP)

Karpenter creates `NodeClaim` custom resources when provisioning nodes. The controller watches these resources to detect in-flight nodes.

```yaml
# What Kompakt watches (not what you configure)
apiVersion: karpenter.sh/v1
kind: NodeClaim
metadata:
  name: nc-xxxxx
status:
  conditions:
    - type: Initialized
      status: "False"  # not yet ready
  allocatable:
    cpu: "16"
    memory: 64Gi
```

### ack-goatscaler

**Supported clouds**: Alibaba ACK

Alibaba's custom autoscaler. Adapter behavior is under research. Documented support matrix will be published before v0.1 release.

**Fallback**: If ack-goatscaler signals are unavailable, the controller falls back to watching new Node objects that appear without the `Ready` condition.

### GKE Node Auto-Provisioning (NAP)

**Supported clouds**: GKE Standard

GKE NAP creates new node pools when existing pools cannot satisfy demand. The adapter watches for new NodePool resources in pending state.

## Fallback behavior

If no adapter detects in-flight nodes (e.g., unsupported autoscaler, cloud-specific signal unavailable), the controller still works. It will:

1. Gate matched pods
2. Wait for the autoscaler to provision a node
3. Detect the new node when it appears with `Ready=True`
4. Release the gate at that point

This fallback is slower (the gate is held until the node is fully ready rather than reserved during provisioning) but still prevents over-provisioning by coordinating which pods are released together.

## Configuration

In-flight detection adapters are auto-detected based on what resources exist in the cluster. No manual configuration is needed.

If you want to disable a specific adapter (e.g., for testing), use the controller flag:

```bash
--disable-inflight-adapter=karpenter
```

Or in the Helm chart:

```yaml
controller:
  args:
    - --disable-inflight-adapter=karpenter
```
