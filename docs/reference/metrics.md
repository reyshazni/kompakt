# Prometheus Metrics

All metrics use the `kompakt_` prefix.

## Webhook

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_webhook_duration_seconds` | Histogram | | Time to process a webhook request |
| `kompakt_webhook_errors_total` | Counter | `reason` | Webhook errors. Reasons: `profile_not_found`, `internal_error` |
| `kompakt_gates_injected_total` | Counter | `profile`, `namespace` | Scheduling gates injected |

## Controller

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_gates_released_total` | Counter | `profile`, `namespace`, `reason` | Gates released. Reasons: `capacity_available`, `timeout`, `priority_override`, `exclude` |
| `kompakt_gate_duration_seconds` | Histogram | `profile` | Time between gate injection and release |
| `kompakt_reservations_active` | Gauge | `profile` | Currently active capacity reservations |
| `kompakt_reservations_failed_total` | Counter | `profile`, `reason` | Reservations that could not be fulfilled. Reasons: `node_disappeared`, `capacity_exhausted`, `timeout` |

## Ledger

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_inflight_nodes_total` | Gauge | `adapter` | In-flight nodes detected. Adapters: `cluster_autoscaler`, `karpenter`, `ack_goatscaler`, `gke_nap` |
| `kompakt_nodes_avoided_total` | Counter | | Cumulative nodes avoided through coordination. This is the headline ROI metric. |

## Key metrics to monitor

**`kompakt_nodes_avoided_total`**: The primary value metric. If this is not increasing during scale-up events, Kompakt is not providing value. Check that workloads are labeled and the controller is running.

**`kompakt_gate_duration_seconds`**: If p99 consistently hits your `reservationTimeout`, either the timeout is too low or capacity is genuinely unavailable. Check `kompakt_inflight_nodes_total` to verify in-flight detection is working.

**`kompakt_webhook_duration_seconds`**: Should stay under 50ms p99. If it exceeds 100ms, investigate webhook pod resources.

**`kompakt_webhook_errors_total{reason="profile_not_found"}`**: Non-zero means pods are referencing non-existent profiles. These pods are being rejected. Fix the label or create the missing profile.
