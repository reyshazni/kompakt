# Prometheus Metrics

All metrics use the `kompakt_` prefix and are served on port `8080` at `/metrics`. Kompakt also exposes standard controller-runtime metrics (`controller_runtime_reconcile_total`, `controller_runtime_webhook_latency_seconds`, `workqueue_*`, etc.) which are not listed here.

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

## PromQL Query Examples

### Webhook performance

```promql
# Webhook p99 latency
histogram_quantile(0.99, rate(kompakt_webhook_request_duration_seconds_bucket[5m]))
# Webhook request rate by operation
sum(rate(kompakt_webhook_requests_total[5m])) by (operation)
# Webhook reject ratio
sum(rate(kompakt_webhook_requests_total{operation="reject"}[5m]))
/
sum(rate(kompakt_webhook_requests_total[5m]))
```

### Gate lifecycle

```promql
# Current gated pods by profile
sum(kompakt_gated_pods) by (profile)
# Gate hold duration p50 / p90 / p99
histogram_quantile(0.50, rate(kompakt_gate_hold_duration_seconds_bucket[5m])) by (profile)
histogram_quantile(0.90, rate(kompakt_gate_hold_duration_seconds_bucket[5m])) by (profile)
histogram_quantile(0.99, rate(kompakt_gate_hold_duration_seconds_bucket[5m])) by (profile)
# Gate release rate by reason
sum(rate(kompakt_gate_releases_total[5m])) by (reason)
# Timeout ratio
sum(rate(kompakt_gate_releases_total{reason="timeout"}[5m]))
/
sum(rate(kompakt_gate_releases_total[5m]))
```

### Ledger health

```promql
# Node count (existing + inflight)
kompakt_ledger_nodes + on() sum(kompakt_ledger_inflight_nodes)
# In-flight nodes by source
kompakt_ledger_inflight_nodes
# Allocatable CPU (cores)
kompakt_ledger_allocatable_millicores / 1000
# Allocatable memory (GiB)
kompakt_ledger_allocatable_memory_bytes / 1073741824
```

### Rule engine

```promql
# Rule evaluation rate by rule and result
sum(rate(kompakt_rule_evaluations_total[5m])) by (rule, result)
# Rule hold ratio
sum(rate(kompakt_rule_evaluations_total{result="hold"}[5m])) by (rule)
/
sum(rate(kompakt_rule_evaluations_total[5m])) by (rule)
# Rule evaluation p99 latency
histogram_quantile(0.99, rate(kompakt_rule_evaluation_duration_seconds_bucket[5m])) by (rule)
```

### Cost
```promql
# Pods released with existing capacity (24h)
sum(increase(kompakt_gate_releases_total{reason="capacity"}[24h]))
# Timeout rate (24h)
sum(increase(kompakt_gate_releases_total{reason="timeout"}[24h]))
/
sum(increase(kompakt_gate_releases_total[24h]))
```

## Grafana Dashboard

### Recommended dashboard layout

| Row | Panel | Query | Visualization |
|---|---|---|---|
| 1 | Webhook p99 latency | `histogram_quantile(0.99, ...)` | Time series, threshold at 50ms |
| 1 | Webhook request rate | `sum(rate(...)) by (operation)` | Time series, stacked |
| 2 | Gated pods | `sum(kompakt_gated_pods) by (profile)` | Time series |
| 2 | Gate hold duration p99 | `histogram_quantile(0.99, ...)` | Time series |
| 3 | Gate releases by reason | `sum(rate(...)) by (reason)` | Time series, stacked |
| 3 | Timeout ratio | `sum(rate({reason="timeout"}...)) / sum(rate(...))` | Stat, thresholds: green <0.05, yellow <0.2, red >=0.2 |
| 4 | Ledger nodes | `kompakt_ledger_nodes` | Stat |
| 4 | Inflight nodes | `kompakt_ledger_inflight_nodes` | Time series |
| 5 | Rule evaluations | `sum(rate(...)) by (rule, result)` | Time series, stacked |
| 5 | Cost: pods saved | `sum(increase({reason="capacity"}[24h]))` | Stat, unit: short |

See the [alert runbooks](../operations/runbooks.md) for how to respond when these metrics cross thresholds.
