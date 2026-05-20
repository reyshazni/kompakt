# Prometheus Metrics

All metrics use the `kompakt_` prefix and are served on port `8080` at `/metrics`.

In addition to the custom metrics below, Kompakt exposes standard controller-runtime
metrics (`controller_runtime_reconcile_total`, `controller_runtime_webhook_latency_seconds`,
`workqueue_*`, etc.) which are not listed here.

## Webhook

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_webhook_requests_total` | Counter | `operation` | Webhook admission decisions. Operations: `gate`, `reject`, `passthrough` |
| `kompakt_webhook_request_duration_seconds` | Histogram | `operation` | Time to process a webhook request. Buckets tuned for p99 < 50ms target |

## Controller

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_gated_pods` | Gauge | `namespace`, `profile` | Current number of pods held with scheduling gates |
| `kompakt_gate_hold_duration_seconds` | Histogram | `profile`, `reason` | Time between gate injection and release |
| `kompakt_gate_releases_total` | Counter | `profile`, `reason` | Gates released. Reasons: `capacity`, `timeout`, `priority`, `profile_not_found` |

## Ledger

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_ledger_nodes` | Gauge | | Existing nodes tracked by the ledger |
| `kompakt_ledger_inflight_nodes` | Gauge | `source` | In-flight nodes by detection source: `cluster-autoscaler`, `karpenter` |
| `kompakt_ledger_allocatable_millicores` | Gauge | | Total allocatable CPU across tracked nodes (millicores) |
| `kompakt_ledger_allocatable_memory_bytes` | Gauge | | Total allocatable memory across tracked nodes (bytes) |

## Rule Engine

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_rule_evaluations_total` | Counter | `rule`, `result` | Rule evaluations. Results: `release`, `hold`, `error` |
| `kompakt_rule_evaluation_duration_seconds` | Histogram | `rule` | Time per rule evaluation |

## Label Cardinality

All labels are bounded:

- `operation`: 3 values (gate, reject, passthrough)
- `reason`: 4 values (capacity, timeout, priority, profile_not_found)
- `profile`: bounded by PackingProfile count
- `namespace`: bounded by cluster namespace count
- `source`: bounded by detector implementations
- `rule`: bounded by registered rule plugins
- `result`: 3 values (release, hold, error)

Pod name, node name, and UID are never used as labels.

## Key Metrics to Monitor

**`kompakt_gated_pods`**: If this grows unboundedly, pods are stuck. Alert if it exceeds
a threshold for your cluster size.

**`kompakt_gate_hold_duration_seconds`**: If p99 consistently hits your `reservationTimeout`,
either the timeout is too low or capacity is genuinely unavailable. Check
`kompakt_ledger_inflight_nodes` to verify in-flight detection is working.

**`kompakt_webhook_request_duration_seconds`**: Should stay under 50ms p99.

**`kompakt_gate_releases_total{reason="timeout"}`**: Non-zero means the system is failing
to find capacity within the reservation window.
