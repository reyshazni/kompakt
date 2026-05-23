# Node Ledger

*How does Kompakt know what capacity exists in the cluster?*

The node ledger is an internal data structure that tracks cluster capacity in real time. It is the controller's source of truth for making bin-packing decisions.

## What the ledger tracks

The ledger maintains three categories of capacity:

### Existing nodes

Nodes already in the cluster with `Ready=True` condition. The ledger tracks their total allocatable resources and current usage (sum of running pod requests).

### In-flight nodes

Nodes requested from the cloud provider but not yet Ready. These are detected via cloud-specific [in-flight detection adapters](inflight-detection.md). The ledger estimates their capacity based on the node group or instance type being provisioned.

### Reservations

Capacity slots reserved for gated pods. When the controller decides to release a gate, it first reserves capacity in the ledger to prevent double-booking. If two pods both need the same slot on an in-flight node, the first one gets the reservation and the second waits for the next available slot.

## How the controller uses the ledger

When a gated pod needs to be evaluated:

1. Read the pod's demand from its matching PackingProfile (e.g., 1 CPU + 2Gi memory)
2. Check existing nodes: is there a node with enough unreserved allocatable capacity?
3. Check in-flight nodes: is a new node coming that will have enough capacity?
4. If yes to either: reserve the capacity, remove the gate, optionally add node affinity
5. If no: keep the gate. The autoscaler will see the pod as unschedulable and provision a node. Once the ledger detects the in-flight node, the pod will be evaluated again.

## Reservation lifecycle

1. **Created**: when the controller decides to release a gate for a pod
2. **Active**: capacity is held for the pod. Other pods cannot use this slot.
3. **Fulfilled**: the pod is scheduled and running on the target node. The reservation is removed, and the pod's resource usage is tracked as part of existing node capacity.
4. **Expired**: the reservation timeout (from the PackingProfile) elapsed before the pod was scheduled. The reservation is released and the capacity is available again.

## Consistency model

The ledger is eventually consistent. Node and pod informers provide updates with typical sub-second latency, but there are edge cases:

- A node can disappear between ledger check and gate release
- An in-flight node can fail to provision
- Pod resource usage can change between reservation and scheduling

The reservation timeout handles all of these. If the expected capacity does not materialize, the reservation expires and the gate is released unconditionally. The pod then schedules via the normal scheduler path without coordination.

## Storage

In v0.x, the ledger is in-memory. If the controller restarts, it rebuilds the ledger from cluster state (nodes, pods, in-flight signals) within seconds. Active reservations are lost on restart, which means some gated pods may need to wait for the next reconcile cycle.

In v0.3, a persistent ledger backed by a Kubernetes resource or etcd will be available for HA deployments that need reservation continuity across controller restarts.

## Performance

| Dimension | Target |
|---|---|
| Ledger update p99 | < 10ms |
| Maximum entries | 100,000 (nodes + pods + reservations) |
| Memory at max scale | ~500 MB |
| Cold start rebuild | < 10s |

The ledger feeds into [rule plugins](rule-plugins.md), which use it to decide when to release each gated pod.
