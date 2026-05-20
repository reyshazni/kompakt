# Decision: Event-based vs Node-based Detection

## Context

Kompakt needs to detect in-flight nodes on ACK (GOATScaler). Two approaches were considered.

## Option 1: NotReady Node Detection

Watch for nodes where `Ready != True` and the node has never been Ready (see [node-ready-condition](node-ready-condition.md)).

Pros:
- Cloud-agnostic (works on any K8s cluster)
- Simple implementation (list nodes, filter by condition)

Cons:
- **Misses the critical window.** The Node object is only created after the ECS instance is provisioned and kubelet registers. This takes 30-60 seconds after the autoscaler decision. During this window, Pod B arrives, WaitForScaleUp sees no inflight nodes, and passthroughs. Pod B triggers a second node. The exact problem Kompakt exists to prevent.
- Label matching is needed for template association (nodepool IDs are opaque hashes)

## Option 2: GOATScaler Event Detection (chosen)

Watch for `ProvisionNode` events from `GOATScaler` component. Parse node name and instance type from the event message.

Pros:
- **Earliest possible signal.** Event fires when GOATScaler decides to provision, before the ECS API call, before the Node object exists. Covers the full window.
- Instance type is directly in the event message. No label matching needed.
- Node name is in the event message. Can be used for affinity once the node arrives.

Cons:
- ACK-specific (other clouds need their own detector)
- Events are ephemeral (default 1h TTL). Must filter by recency.
- Message format is undocumented and could change in a GOATScaler update.

## Decision

Event-based. The window between autoscaler decision and node registration is precisely where Kompakt provides value. Missing it makes Kompakt a no-op for scale-from-zero. The message format risk is acceptable because:
1. GOATScaler is based on upstream CA which has had stable event format for years
2. Format change would be a breaking change for ACK users' monitoring
3. We can add a fallback to NotReady detection as a safety net

## Verified on

Cluster: `main-al-dads-id-s-03`, 2026-05-20. See [goatscaler-ack](goatscaler-ack.md) for full event details.
