# 003: WaitForScaleUp Rule

Status: DONE

## Context

The current `BinPackOnInflightCapacity` rule has a binary decision model: release or hold. This breaks two critical scenarios:

1. **Cold start penalty**: When no capacity exists anywhere (no nodes, no in-flight), the rule returns "hold." The pod stays gated, invisible to the autoscaler. Nothing triggers a scale-up. The pod waits until `reservationTimeout` (3-5 min) expires before the autoscaler even sees it.

2. **Fake hostname affinity**: When a pod fits on an in-flight node, the rule releases the gate with node affinity pointing to a synthetic name like `pool-gpu-pending-0`. The real node arrives with a different hostname (e.g., `cn-jakarta.172.16.x.x`). The pod's affinity never matches. It is stuck forever.

These are the two bugs identified in the original analysis. The `BinPackOnInflightCapacity` rule should only handle existing nodes. A separate `WaitForScaleUp` rule handles the three-state logic needed for scale-up coordination.

## What exists today

- `NodeLedger.FindFit` returns `(nodeName string, err error)` -- no distinction between existing vs in-flight match
- `BinPackOnInflightCapacity.Evaluate` returns `(release bool, nodeName string, err error)` -- binary
- `releaseGatesWithAffinity` has a guard `!strings.HasPrefix(nodeName, "inflight-")` that does not match detector naming convention (`pool-gpu-pending-0`)
- Inflight node enrichment from `NodeGroupTemplates` is implemented (commit `c836cdf`)
- Docs for `WaitForScaleUp` are written, gate name `kompakt.io/awaiting-scale-up` is documented
- Webhook gate mapping for `WaitForScaleUp` already exists in `mutating.go:gateNames`... no, needs to be added

## Changes

### 1. Ledger: FindFit returns isInflight

File: `internal/ledger/ledger.go`

Change `FindFit` signature:

```go
func (l *NodeLedger) FindFit(demand map[string]int64) (name string, isInflight bool, err error)
```

The `check` closure already iterates `l.nodes` and `l.inflight` separately. Track which map the winner came from.

All callers of `FindFit` need updating:
- `internal/rules/binpack.go` -- `BinPackOnInflightCapacity.Evaluate`

### 2. BinPackOnInflightCapacity: only match existing nodes

File: `internal/rules/binpack.go`

After `FindFit` returns, if `isInflight == true`, treat it as no fit. This rule only handles existing nodes.

Alternatively, add a `FindFitExisting` method to the ledger that only checks `l.nodes`. Cleaner separation.

Decision: Add `FindFitExisting` -- avoids changing the `FindFit` signature which would break the WaitForScaleUp rule that needs both.

### 3. New rule: WaitForScaleUp

File: `internal/rules/scaleup.go` (new)

```go
type WaitForScaleUp struct{}

func (r *WaitForScaleUp) Name() string { return "WaitForScaleUp" }

func (r *WaitForScaleUp) Evaluate(...) (release bool, nodeName string, err error) {
    demand := ExtractDemand(pod, profile.Spec.DemandSource)
    if len(demand) == 0 {
        return true, "", nil
    }

    nodeName, isInflight, err := l.FindFit(demand)
    if err != nil {
        // No capacity anywhere -- passthrough to trigger autoscaler
        return true, "", nil
    }

    if isInflight {
        // Incoming node can fit -- hold gate, reserve capacity
        l.Reserve(nodeName, demand)
        return false, "", nil
    }

    // Existing node can fit -- release with real node affinity
    l.Reserve(nodeName, demand)
    return true, nodeName, nil
}
```

Register in `init()`.

### 4. Ledger: add FindFitExisting

File: `internal/ledger/ledger.go`

```go
func (l *NodeLedger) FindFitExisting(demand map[string]int64) (string, error)
```

Same BestFit logic as `FindFit` but only iterates `l.nodes`, not `l.inflight`.

Update `BinPackOnInflightCapacity` to call `FindFitExisting` instead of `FindFit`.

### 5. Ledger: update FindFit to return isInflight

File: `internal/ledger/ledger.go`

```go
func (l *NodeLedger) FindFit(demand map[string]int64) (name string, isInflight bool, err error)
```

Track whether the best match came from `l.nodes` or `l.inflight`. Used by `WaitForScaleUp`.

### 6. Webhook: add gate mapping

File: `internal/webhook/mutating.go`

Add to `gateNames` map:

```go
"WaitForScaleUp": "kompakt.io/awaiting-scale-up",
```

### 7. CRD: add WaitForScaleUp to RuleRef enum

File: `api/v1alpha1/packingprofile_types.go`

The `RuleRef.Name` kubebuilder enum already has `WaitForScaleUp`... needs to be verified. If not, add it.

### 8. Controller: remove inflight- prefix guard

File: `internal/controller/packingprofile_controller.go`

The guard at line 214 `!strings.HasPrefix(nodeName, "inflight-")` is no longer needed. With the new rule separation:
- `BinPackOnInflightCapacity` only returns existing node names (always real)
- `WaitForScaleUp` returns existing node names on release, empty string on hold

The guard can be simplified to just `nodeName != ""`.

## Test plan (RED phase)

### Ledger tests (`internal/ledger/ledger_test.go`)

- [ ] `TestFindFit_ReturnsIsInflight_True` -- inflight node fits, isInflight=true
- [ ] `TestFindFit_ReturnsIsInflight_False` -- existing node fits, isInflight=false
- [ ] `TestFindFit_PrefersExistingOverInflight` -- both fit, existing node has less slack, picks existing
- [ ] `TestFindFitExisting_IgnoresInflight` -- inflight node exists but FindFitExisting returns errNoFit
- [ ] `TestFindFitExisting_FindsExisting` -- existing node fits, returns it

### Rule tests (`internal/rules/scaleup_test.go`, new)

- [ ] `TestWaitForScaleUp_NoCapacity_Passthrough` -- no nodes, no inflight -> release=true, nodeName=""
- [ ] `TestWaitForScaleUp_InflightFits_Hold` -- inflight node fits -> release=false, capacity reserved
- [ ] `TestWaitForScaleUp_ExistingFits_Release` -- existing node fits -> release=true, nodeName=real
- [ ] `TestWaitForScaleUp_EmptyDemand_Release` -- no demand -> release=true
- [ ] `TestWaitForScaleUp_ReservationOnInflight` -- after hold, subsequent FindFit sees reduced capacity
- [ ] `TestWaitForScaleUp_Registered` -- rule is in global Registry

### BinPack rule tests (`internal/rules/rule_test.go`)

- [ ] `TestBinPack_IgnoresInflightNodes` -- inflight node exists with capacity, BinPack returns hold (does not use it)
- [ ] Update existing `TestBinPack_InflightCapacity` -- should now return release=false

### Controller tests (`internal/controller/packingprofile_controller_test.go`)

- [ ] `TestReconcile_WaitForScaleUp_Passthrough` -- no nodes, gate released, no affinity
- [ ] `TestReconcile_WaitForScaleUp_HoldOnInflight` -- inflight node fits, gate stays
- [ ] `TestReconcile_WaitForScaleUp_ReleaseWithAffinity` -- existing node fits, gate released with affinity

### Webhook tests (`internal/webhook/mutating_test.go`)

- [ ] `TestHandle_WaitForScaleUp_GateInjected` -- profile with WaitForScaleUp rule injects correct gate

## Verification

```bash
make generate manifests
make fmt vet lint test
```

## Order of operations

1. RED: write all failing tests
2. GREEN: implement in order: ledger -> rule -> webhook mapping -> CRD enum -> controller cleanup
3. REFACTOR: remove dead code (inflight- prefix guard), run full suite
