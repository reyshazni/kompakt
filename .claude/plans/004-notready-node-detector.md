# 004: NotReady Node Detector + Label-based Template Matching

Status: PLANNED

## Context

The `ClusterAutoscalerDetector` reads the `cluster-autoscaler-status` ConfigMap in kube-system. This ConfigMap does not exist on Alibaba ACK because ACK uses Goatscaler (a managed autoscaler) which stores state differently:

- `autoscaler-meta` ConfigMap: scaler config only, no scale-up state
- `np<id>-inventory-status` ConfigMaps: ECS instance stock availability per nodepool
- Pod events: `TriggeredScaleUp` / `NotTriggerScaleUp` written directly on pods

Kompakt's detector returns `nil, nil` (no inflight nodes), the ledger stays empty, and WaitForScaleUp always passthroughs. Kompakt becomes a no-op on ACK.

Additionally, `nodeGroupTemplates` matches by `namePrefix` against node names. On ACK, node names are IP-based (`cn-jakarta.172.16.1.10`), not nodepool names. Template matching fails.

## Research: How to detect newly provisioning nodes

### The "never been Ready" signal

When a node first registers with the API server:

1. Kubelet creates the Node object and sets initial conditions
2. The `Ready` condition starts as `False` with `lastTransitionTime` set to the current time
3. The node goes through initialization: network plugin, device plugins, etc.
4. When everything is ready, kubelet updates `Ready` to `True`, updating `lastTransitionTime`

For a node that was previously Ready but crashed:

1. The `Ready` condition transitions from `True` to `False` (or `Unknown`)
2. `lastTransitionTime` updates to the time of the transition
3. Critically: `lastTransitionTime` is AFTER `creationTimestamp` (because the node was Ready at some point between creation and the crash)

The detection rule: **a node is newly provisioning if `Ready != True` AND the Ready condition's `lastTransitionTime` is within a few seconds of `node.creationTimestamp`**.

This means the node has never been Ready since it was created. It was born NotReady and has stayed NotReady. This is fundamentally different from a node that was Ready and then crashed.

Source: [kubernetes/pkg/kubelet/nodestatus/setters.go](https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/nodestatus/setters.go) -- when readyConditionUpdated is false (first time), lastTransitionTime is set to currentTime. When Ready status changes from True to False, lastTransitionTime updates again.

### Why "within a few seconds" not exact equality

`creationTimestamp` is set by the API server when the Node object is created. `lastTransitionTime` of the Ready condition is set by kubelet when it first reports status. There can be a small delta (typically < 5 seconds) because:

- API server creates the Node object (sets creationTimestamp)
- Kubelet's first status update arrives moments later (sets lastTransitionTime)

We use a threshold of 10 seconds: if `lastTransitionTime - creationTimestamp < 10s`, the node has never been Ready.

### What about kubelet restart?

Known Kubernetes issue (#124397): after kubelet restart, the node briefly shows NotReady in the first update period. However, in this case:

- `creationTimestamp` is old (the node existed before restart)
- `lastTransitionTime` is recent (the restart)
- Delta is large (hours/days), so our detector correctly ignores it

### ACK-specific node labels

ACK nodes have these labels set during provisioning (before Ready):

- `alibabacloud.com/nodepool-id`: opaque hash like `npa7a3c05295974b56ab23537d94e74d08`
- `node.kubernetes.io/instance-type`: ECS instance type like `ecs.gn7i-c16g1.4xlarge`
- `topology.kubernetes.io/zone`: availability zone

These labels are available on NotReady nodes because they are set by the cloud controller during node registration, before kubelet marks the node Ready.

## Design

### 1. NotReadyNodeDetector

New detector in `internal/inflight/`. Lists all nodes via the client reader, filters for nodes that:

1. Do not have `Ready=True` condition
2. Have `lastTransitionTime` of the Ready condition within 10 seconds of `creationTimestamp` (never been Ready)

Returns each qualifying node as an `InflightNode` with:
- `Name`: actual Kubernetes node name
- `Allocatable`: from `node.Status.Allocatable` (kubelet often reports this before Ready)
- `Labels`: from `node.Labels` (set by cloud controller during registration)

### 2. InflightNode struct: add Labels

```go
type InflightNode struct {
    Name        string
    Allocatable map[string]int64
    Labels      map[string]string
}
```

All existing code that creates InflightNode needs updating (ClusterAutoscalerDetector returns empty labels).

### 3. NodeGroupTemplate: add label matching

```go
type NodeGroupTemplate struct {
    NamePrefix  string            `json:"namePrefix,omitempty"`
    NodeLabel   *LabelSelector    `json:"nodeLabel,omitempty"`
    Allocatable map[string]int64  `json:"allocatable"`
}

type LabelSelector struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}
```

CRD validation: at least one of `namePrefix` or `nodeLabel` must be set.

Matching priority: `nodeLabel` first (if set), then `namePrefix` fallback. This way existing configs using namePrefix keep working.

### 4. Controller: matchNodeGroupTemplate update

Update `matchNodeGroupTemplate` to accept node labels and check `nodeLabel` matcher before `namePrefix`.

### 5. Example usage on ACK

```yaml
capacitySource:
  type: NodeLabel
  label: aliyun.accelerator/gpu-memory-mib
  perDeviceCount:
    label: aliyun.accelerator/gpu-count
  nodeGroupTemplates:
    - nodeLabel:
        key: node.kubernetes.io/instance-type
        value: ecs.gn7i-c16g1.4xlarge
      allocatable:
        aliyun.com/gpu-mem: 49152
```

User matches by instance type (stable, human-readable, same across clusters) instead of nodepool ID (opaque hash, varies per cluster).

## Files to modify

1. `internal/inflight/detector.go` -- add InflightNode.Labels, add NotReadyNodeDetector
2. `internal/inflight/detector_test.go` -- tests for NotReadyNodeDetector
3. `api/v1alpha1/packingprofile_types.go` -- add LabelSelector, update NodeGroupTemplate
4. `api/v1alpha1/zz_generated.deepcopy.go` -- regenerate
5. `internal/controller/packingprofile_controller.go` -- update matchNodeGroupTemplate
6. `internal/controller/packingprofile_controller_test.go` -- tests
7. `cmd/manager/main.go` -- register NotReadyNodeDetector
8. `config/crd/` -- regenerated manifests

## Test plan (RED phase)

### NotReadyNodeDetector tests

- `TestNotReadyDetector_NewNode_Detected`: node with Ready=False, lastTransitionTime near creationTimestamp
- `TestNotReadyDetector_CrashedNode_Ignored`: node with Ready=False, lastTransitionTime >> creationTimestamp
- `TestNotReadyDetector_ReadyNode_Ignored`: node with Ready=True
- `TestNotReadyDetector_NoReadyCondition_Detected`: node with no Ready condition at all (just registered)
- `TestNotReadyDetector_AllocatablePopulated`: NotReady node with allocatable already set by kubelet
- `TestNotReadyDetector_LabelsPreserved`: node labels passed through to InflightNode
- `TestNotReadyDetector_Name`: returns "not-ready-nodes"

### Label matching tests

- `TestMatchTemplate_LabelMatch`: template with nodeLabel matches node labels
- `TestMatchTemplate_LabelNoMatch`: template with nodeLabel, node has different value
- `TestMatchTemplate_LabelMissing`: template with nodeLabel, node missing the label
- `TestMatchTemplate_NamePrefixFallback`: template with namePrefix only, still works
- `TestMatchTemplate_LabelTakesPriority`: template with both, label match wins

### Controller integration tests

- `TestReconcile_NotReadyDetector_LabelMatch`: NotReady node detected, template matched by label, WaitForScaleUp holds

## Verification

```bash
make generate manifests
make fmt vet lint test
# Then e2e on kind (NotReady nodes are hard to simulate in kind, may need to skip)
```

## Sources

- [Kubernetes Node Status (official docs)](https://kubernetes.io/docs/reference/node/node-status/)
- [Kubernetes Nodes (official docs)](https://kubernetes.io/docs/concepts/architecture/nodes/)
- [kubelet nodestatus setters.go](https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/nodestatus/setters.go)
- [kubelet restart NotReady issue #124397](https://github.com/kubernetes/kubernetes/issues/124397)
- [ACK Node Pool Labels](https://www.alibabacloud.com/help/en/ack/ack-managed-and-ack-dedicated/user-guide/schedule-an-application-pod-to-a-specific-node-pool)
- [Cluster Autoscaler FAQ](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md)
