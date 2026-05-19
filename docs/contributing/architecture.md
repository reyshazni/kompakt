# Architecture

This document describes the internal code architecture for contributors.

## Package structure

```
api/v1alpha1/          PackingProfile CRD types
cmd/manager/           Single binary entrypoint
internal/
  controller/          Reconciliation loops
  webhook/             Mutating admission webhook
  ledger/              In-flight node capacity tracker
  packing/             Bin-packing algorithms
  matcher/             PackingProfile name resolver
  rules/               Rule plugin interface and implementations
  inflight/            Cloud-specific in-flight node detection
```

## Request path

### Webhook (admission time)

The webhook is the fast path. It runs on every pod creation and must complete within 50ms p99.

1. Read `packer.kompakt.io/packing-profile` label from the pod
2. If absent: allow, return
3. Check `kompakt.io/exclude` label: if present, allow, return
4. Look up PackingProfile by name from the informer cache (in-memory, no API call)
5. If not found: reject with error
6. Inject `spec.schedulingGates` based on the profile's rules
7. Return

The webhook never makes external calls. The profile lookup hits the controller-runtime client cache, which is populated by informers. This is why latency stays under 50ms.

### Controller (reconciliation)

The controller is the slow path. It runs on a 1-second reconcile interval and handles gate release decisions.

1. Watch for pods with `kompakt.io/` scheduling gates
2. For each gated pod, read the matching PackingProfile
3. Extract demand from the pod (ResourceRequest or Annotation)
4. Query the ledger for available capacity
5. Run the profile's rule plugins in order
6. If all rules agree to release: reserve capacity, remove gate, optionally add node affinity
7. If any rule says wait: keep the gate, reconcile again next cycle

## Key interfaces

### Rule plugin

```go
// internal/rules/rule.go
type Rule interface {
    Name() string
}
```

Each rule evaluates a gated pod against cluster state and decides whether to release the gate. Rules are registered by name and referenced in PackingProfile specs.

### In-flight detector

```go
// internal/inflight/detector.go
type Detector interface {
    Name() string
}
```

Each detector watches for cloud-specific signals indicating nodes are being provisioned. Detected in-flight nodes are reported to the ledger.

## Ledger

The ledger (`internal/ledger/`) is the controller's source of truth for capacity decisions. It tracks:

- **Existing nodes**: allocatable resources minus running pod requests
- **In-flight nodes**: expected capacity from autoscaler scale-up events
- **Reservations**: capacity slots held for gated pods awaiting scheduling

The ledger is in-memory in v0.x. All state is derived from cluster objects (nodes, pods, in-flight signals), so it can be rebuilt from scratch on controller restart.

## Packing algorithms

The packing module (`internal/packing/`) implements bin-packing strategies:

- **BestFit**: find the node with the least available capacity that still fits the pod. Minimizes wasted capacity.
- **FirstFit**: find the first node that fits. Faster but may leave more gaps.

The strategy is configured per PackingProfile (planned, currently BestFit is the default).

## Data flow summary

1. Pod created with profile label
2. Webhook injects gate (fast path, < 50ms)
3. Controller detects gated pod via informer
4. Controller reads profile, extracts demand
5. Ledger queried for capacity (existing + in-flight)
6. Packing algorithm finds a fit
7. Reservation created in ledger
8. Gate removed, optional node affinity added
9. Scheduler binds pod (unmodified)
10. Reservation fulfilled when pod is running

## Adding a new rule plugin

1. Create a new file in `internal/rules/`
2. Implement the `Rule` interface
3. Register the rule name in the rule registry
4. Add the name to the `RuleRef` enum in `api/v1alpha1/packingprofile_types.go`
5. Write tests following TDD (RED, GREEN, REFACTOR)
6. Document in `docs/concepts/rule-plugins.md`

## Adding a new in-flight detector

1. Create a new file in `internal/inflight/`
2. Implement the `Detector` interface
3. Register the detector in the adapter auto-detection logic
4. Write tests
5. Document in `docs/concepts/inflight-detection.md`
6. Add to the compatibility matrix in `docs/reference/compatibility.md`
