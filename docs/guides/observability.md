# Observability

## When you need this

You want to monitor Kompakt's behavior, measure cost savings, set up alerts for anomalies, or debug why pods are gated longer than expected. Common scenarios:

- Debugging: why is a pod stuck in `SchedulingGated`?
- Capacity planning: how often are reservations timing out?
- SLA monitoring: is the webhook adding latency to pod creation?

## Structured Logging

Kompakt uses structured JSON logging via `logr` + zap (controller-runtime). Logs are
designed around the principle **log events, not states**: each pod produces exactly 2 log
lines at default verbosity, regardless of how long it stays gated.

### Trace Correlation

Every gated pod receives a trace ID (an 8-character identifier injected by the webhook as the `kompakt.io/trace-id` pod annotation, used to correlate logs across the webhook and controller). This trace ID appears in every log line for that pod:

```
# Webhook log (pod creation)
{"level":"info","msg":"Pod gated","pod":"my-pod","namespace":"default","profile":"cpu-packing","traceID":"a1b2c3d4","gates":1}

# Controller log (gate release, could be minutes later)
{"level":"info","msg":"Gate released","traceID":"a1b2c3d4","profile":"cpu-packing","podUID":"...","reason":"capacity","node":"node-3"}
```

To trace a pod's full lifecycle:

```bash
# From logs
kubectl logs -l app.kubernetes.io/name=kompakt -n kompakt-system | grep a1b2c3d4

# From the pod itself
kubectl get pod my-pod -o jsonpath='{.metadata.annotations.kompakt\.io/trace-id}'
```

### Log Fields

| Field | Description | Present in |
|---|---|---|
| `traceID` | 8-char UUID from `kompakt.io/trace-id` annotation | Webhook + Controller |
| `profile` | PackingProfile name from pod label | Webhook + Controller |
| `podUID` | Pod UID for unambiguous identification | Controller |
| `namespace` | Pod namespace | Both |
| `name` | Pod name | Both |
| `reconcileID` | Auto-injected by controller-runtime, unique per reconcile | Controller |
| `reason` | Gate release reason: `capacity`, `timeout`, `priority`, `profile_not_found` | Controller |
| `node` | Target node for affinity (on release) | Controller |
| `operation` | Webhook decision: `gate`, `reject`, `passthrough` | Webhook |
| `gates` | Number of scheduling gates injected | Webhook |

### Log Verbosity

Adjust with the `--zap-log-level` flag on the manager binary:

| Flag | Level | What you see |
|---|---|---|
| `--zap-log-level=0` | Info (default) | Gate injected, gate released, errors |
| `--zap-log-level=1` | V(1) | + in-flight detection failures, unknown rule names |
| `--zap-log-level=3` | V(3) | + rule evaluation details, ledger sync stats |
| `--zap-log-level=4` | V(4) | + bin-packing algorithm steps (very verbose) |

At default verbosity, a pod's lifecycle produces exactly 2 log lines (webhook gate +
controller release). Per-reconcile-cycle data (rule holds, ledger state) is captured in
metrics, not logs, to avoid noise from the 1-second requeue loop.

## Prometheus Metrics

Kompakt exposes custom metrics on port `8080` at `/metrics`. See the
[metrics reference](../reference/metrics.md) for the full list.

### Scraping

Add a `ServiceMonitor` if you use the Prometheus Operator:

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

## Alerts

Recommended alert rules:

```yaml
groups:
  - name: kompakt
    rules:
      - alert: KompaktGatedPodsGrowing
        expr: kompakt_gated_pods > 100
        for: 10m
        annotations:
          summary: Large number of pods stuck in gated state

      - alert: KompaktWebhookHighLatency
        expr: histogram_quantile(0.99, rate(kompakt_webhook_request_duration_seconds_bucket[5m])) > 0.05
        for: 5m
        annotations:
          summary: Kompakt webhook p99 latency exceeds 50ms

      - alert: KompaktGateTimeouts
        expr: rate(kompakt_gate_releases_total{reason="timeout"}[5m]) > 0
        for: 10m
        annotations:
          summary: Pods are hitting reservation timeout consistently

      - alert: KompaktGateHoldDurationHigh
        expr: histogram_quantile(0.99, rate(kompakt_gate_hold_duration_seconds_bucket[5m])) > 120
        for: 5m
        annotations:
          summary: Gate hold duration p99 exceeds 2 minutes
```

## Next steps

- [Prometheus metrics reference](../reference/metrics.md) for the full metrics list and PromQL query examples
- [Alert runbooks](../operations/runbooks.md) for per-alert diagnosis and resolution steps
- [Troubleshooting](troubleshooting.md) for diagnosing common issues
