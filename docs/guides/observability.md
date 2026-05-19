# Observability

## When you need this

You want to monitor Kompakt's behavior, measure cost savings, set up alerts for anomalies, or debug why pods are gated longer than expected. Common scenarios:

- Measuring ROI: how many nodes did Kompakt save?
- Debugging: why is a pod stuck in `SchedulingGated`?
- Capacity planning: how often are reservations timing out?
- SLA monitoring: is the webhook adding latency to pod creation?

## Prometheus metrics

### Webhook metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_webhook_duration_seconds` | Histogram | | Webhook request latency |
| `kompakt_webhook_errors_total` | Counter | `reason` | Webhook errors by reason |
| `kompakt_gates_injected_total` | Counter | `profile`, `namespace` | Gates injected by profile |

### Controller metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_gates_released_total` | Counter | `profile`, `namespace`, `reason` | Gates released by profile and reason |
| `kompakt_gate_duration_seconds` | Histogram | `profile` | Time a pod spent gated |
| `kompakt_reservations_active` | Gauge | `profile` | Currently active capacity reservations |
| `kompakt_reservations_failed_total` | Counter | `profile`, `reason` | Failed reservations by reason |

### Ledger metrics

| Metric | Type | Labels | Description |
|---|---|---|---|
| `kompakt_inflight_nodes_total` | Gauge | `adapter` | In-flight nodes detected by adapter |
| `kompakt_nodes_avoided_total` | Counter | | Nodes avoided through coordination (headline ROI metric) |

## Scraping

Kompakt exposes metrics on port `8080` at `/metrics`. Add a `ServiceMonitor` if you use the Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: kompakt
  namespace: kompakt-system
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: kompakt
  endpoints:
    - port: metrics
      interval: 15s
```

Or add a scrape config:

```yaml
- job_name: kompakt
  kubernetes_sd_configs:
    - role: pod
      namespaces:
        names: [kompakt-system]
  relabel_configs:
    - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_name]
      regex: kompakt
      action: keep
```

## Grafana dashboard

A reference Grafana dashboard is available at `examples/grafana/kompakt-dashboard.json`. Import it into your Grafana instance.

Key panels:

- **Nodes avoided**: the headline ROI metric. Shows cumulative nodes saved through coordination.
- **Gate duration**: p50/p95/p99 time pods spend gated, by profile. If this is consistently hitting your reservation timeout, your timeout may be too low.
- **Active gates**: current number of gated pods per profile.
- **Webhook latency**: p99 should stay under 50ms. If it spikes, the webhook may be under-resourced.
- **Inflight nodes**: number of nodes detected by each adapter. If this stays at 0, check that the in-flight detection adapter is working.

## Structured logging

Kompakt uses structured JSON logging via `logr` (controller-runtime). Key log fields:

- `profile`: the PackingProfile name
- `pod`: the gated pod name
- `namespace`: the pod namespace
- `gate`: the gate name
- `node`: the target node (when releasing a gate)
- `reason`: why a gate was released or a reservation failed

Adjust log verbosity with the `--zap-log-level` flag:

```bash
# Default (info)
--zap-log-level=0

# Debug (verbose reconcile logging)
--zap-log-level=1

# Trace (every ledger update)
--zap-log-level=2
```

## Alerts

Recommended alert rules:

```yaml
groups:
  - name: kompakt
    rules:
      - alert: KompaktWebhookHighLatency
        expr: histogram_quantile(0.99, rate(kompakt_webhook_duration_seconds_bucket[5m])) > 0.1
        for: 5m
        annotations:
          summary: Kompakt webhook p99 latency exceeds 100ms

      - alert: KompaktGateTimeout
        expr: rate(kompakt_gates_released_total{reason="timeout"}[5m]) > 0
        for: 10m
        annotations:
          summary: Pods are hitting reservation timeout consistently

      - alert: KompaktWebhookErrors
        expr: rate(kompakt_webhook_errors_total[5m]) > 0.1
        for: 5m
        annotations:
          summary: Kompakt webhook is returning errors
```

## Next steps

- [Prometheus metrics reference](../reference/metrics.md) for the full metrics list
- [Troubleshooting](troubleshooting.md) for diagnosing common issues
