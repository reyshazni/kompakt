# Alert Runbooks

This page provides a runbook for each recommended Kompakt alert. Each runbook follows the same structure: what the alert means, what to check, and what to do.

## KompaktGatedPodsGrowing

**Alert definition:**

```yaml
alert: KompaktGatedPodsGrowing
expr: kompakt_gated_pods > 100
for: 10m
```

**What it means:** More than 100 pods have been in the gated state for over 10 minutes. Either capacity is genuinely unavailable, the controller is stuck, or pods are accumulating faster than they can be released.

**Severity:** Warning. Pods are not lost (they exist in etcd) but they are not scheduling.

### Diagnosis

```bash
# 1. Is the controller running?
kubectl get pods -n kompakt-system -l app.kubernetes.io/name=kompakt

# 2. Is the controller the leader?
kubectl get lease kompakt-leader -n kompakt-system -o yaml

# 3. What profiles are the gated pods using?
kubectl get pods --all-namespaces -o json | \
  jq -r '.items[] | select(.spec.schedulingGates != null) | .metadata.labels["packer.kompakt.io/packing-profile"]' | \
  sort | uniq -c | sort -rn

# 4. Are there in-flight nodes?
kubectl get configmap cluster-autoscaler-status -n kube-system -o yaml | grep -A5 "cloudProviderTarget"

# 5. Check controller logs for errors
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt --tail=200 | grep -i error
```

### Common causes and fixes

| Cause | Evidence | Fix |
|---|---|---|
| Controller crashed / OOMKilled | Pod in CrashLoopBackOff | Increase memory limit, check logs for panic |
| Leader election lost | Lease holder differs from running pod | Wait for re-election (~15s) or restart pod |
| No capacity anywhere | Ledger has 0 nodes, 0 inflight | Autoscaler may be at max. Check node pool limits |
| In-flight detection broken | Autoscaler provisioning but `kompakt_ledger_inflight_nodes` = 0 | Check adapter (CA ConfigMap exists? GOATScaler events firing?) |
| Reservation timeout too high | Pods gated for longer than node provisioning time | Lower `reservationTimeout` in the profile |
| Burst of demand | Legitimate burst, controller working through queue | Wait. Check if `kompakt_gated_pods` is decreasing |

### Resolution

If the controller is healthy and working through the queue, no action needed -- the alert will self-resolve as pods are released.

If the controller is down or stuck, restart it:

```bash
kubectl rollout restart deployment kompakt-controller-manager -n kompakt-system
```

If you need immediate relief, release all gated pods manually:

```bash
kubectl get pods --all-namespaces -o json | \
  jq -r '.items[] | select(.spec.schedulingGates[]?.name | startswith("kompakt.io/")) | "\(.metadata.namespace) \(.metadata.name)"' | \
  while read ns name; do
    kubectl patch pod "$name" -n "$ns" --type=json \
      -p '[{"op":"remove","path":"/spec/schedulingGates"}]'
  done
```

---

## KompaktWebhookHighLatency

**Alert definition:**

```yaml
alert: KompaktWebhookHighLatency
expr: histogram_quantile(0.99, rate(kompakt_webhook_request_duration_seconds_bucket[5m])) > 0.05
for: 5m
```

**What it means:** The webhook's p99 latency exceeds 50ms for 5 minutes. This adds latency to every pod creation in namespaces covered by the webhook.

**Severity:** Warning. Pod creation still succeeds but is slower. If latency exceeds the API server's webhook timeout (default 10s), pod creation will fail or bypass the webhook (depending on failure policy).

### Diagnosis

```bash
# 1. Check webhook pod resource usage
kubectl top pod -n kompakt-system -l app.kubernetes.io/name=kompakt

# 2. Check if informer cache is synced (logs after restart)
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt --tail=50 | grep -i "cache"

# 3. Check number of PackingProfiles
kubectl get packingprofiles --no-headers | wc -l

# 4. Check if pod was recently restarted
kubectl get pods -n kompakt-system -l app.kubernetes.io/name=kompakt -o wide
```

### Common causes and fixes

| Cause | Evidence | Fix |
|---|---|---|
| Informer cache warming after restart | High latency for 10-30s after pod start, then normalizes | No action needed, transient |
| CPU throttling | `kubectl top` shows CPU at limit | Increase CPU limit |
| Large number of PackingProfiles (>50) | Profile count is high, lookup time scales | Unlikely in practice; consolidate profiles if possible |
| Network latency to API server | Latency correlates with API server load | Check API server health, consider dedicated node for Kompakt |
| Go GC pressure | Memory near limit, periodic latency spikes | Increase memory limit |

### Resolution

If transient (after restart), wait 30 seconds for cache sync.

If persistent, increase resources:

```bash
kubectl patch deployment kompakt-controller-manager -n kompakt-system \
  --type=json -p '[{"op":"replace","path":"/spec/template/spec/containers/0/resources/limits/cpu","value":"1"}]'
```

---

## KompaktGateTimeouts

**Alert definition:**

```yaml
alert: KompaktGateTimeouts
expr: rate(kompakt_gate_releases_total{reason="timeout"}[5m]) > 0
for: 10m
```

**What it means:** Pods are consistently hitting their `reservationTimeout` and being released without confirmed capacity. The system is failing to find a placement within the reservation window.

**Severity:** Warning. Pods are released (not stuck), but they are released uncoordinated -- the autoscaler will provision for them individually, which is exactly the over-provisioning Kompakt is supposed to prevent.

### Diagnosis

```bash
# 1. Which profiles are timing out?
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt --tail=500 | \
  grep "timeout" | jq -r '.profile' | sort | uniq -c

# 2. What is the reservation timeout for those profiles?
kubectl get packingprofile <profile-name> -o jsonpath='{.spec.reservationTimeout}'

# 3. How long are nodes actually taking to provision?
# Check node age vs creation timestamp
kubectl get nodes --sort-by=.metadata.creationTimestamp -o custom-columns=\
NAME:.metadata.name,AGE:.metadata.creationTimestamp,READY:.status.conditions[-1].lastTransitionTime

# 4. Are in-flight nodes being detected?
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt --tail=200 | grep "inflight"
```

### Common causes and fixes

| Cause | Evidence | Fix |
|---|---|---|
| Timeout too short for node provisioning time | Nodes take 4min, timeout is 3m | Increase `reservationTimeout` to exceed provisioning time + buffer |
| No stock (cloud capacity exhausted) | Autoscaler reports "failed to increase size" | Nothing Kompakt can do. Consider multi-AZ or different instance types |
| In-flight detection not working | Nodes provisioning but `kompakt_ledger_inflight_nodes` = 0 | Verify adapter: CA ConfigMap exists? GOATScaler events present? |
| Node stuck in NotReady | Node joined but never becomes Ready | Investigate node health (kubelet, CNI, device-plugin) |
| Template mismatch | In-flight detected but capacity does not match pod demand | Verify `nodeGroupTemplates.allocatable` matches real node capacity |

### Resolution

Most commonly: increase `reservationTimeout` to match your actual node provisioning time:

```bash
kubectl patch packingprofile <name> --type=merge \
  -p '{"spec":{"reservationTimeout":"5m"}}'
```

If nodes are genuinely unavailable (no stock), the timeout fallback is correct behavior -- pods should be released to give the autoscaler a chance with different node groups.

---

## KompaktGateHoldDurationHigh

**Alert definition:**

```yaml
alert: KompaktGateHoldDurationHigh
expr: histogram_quantile(0.99, rate(kompakt_gate_hold_duration_seconds_bucket[5m])) > 120
for: 5m
```

**What it means:** The p99 gate hold duration exceeds 2 minutes. Pods are waiting a long time before being released. This may or may not be a problem depending on your node provisioning time.

**Severity:** Info/Warning. If your GPU nodes take 3-5 minutes to provision, a 2-minute hold is expected. If your CPU nodes take 60 seconds, a 2-minute hold indicates a problem.

### Diagnosis

```bash
# 1. What is the actual node provisioning time?
# Compare node creation to Ready condition
kubectl get nodes -o json | jq -r '.items[] | select(.metadata.creationTimestamp > "2024-01-01") |
  "\(.metadata.name) created=\(.metadata.creationTimestamp) ready=\(.status.conditions[] | select(.type=="Ready") | .lastTransitionTime)"'

# 2. Which profiles have high hold duration?
# Check the profile label on metrics
# PromQL: histogram_quantile(0.99, rate(kompakt_gate_hold_duration_seconds_bucket[5m])) by (profile)

# 3. Are pods being held on in-flight nodes that never arrive?
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt --tail=200 | grep "held"
```

### Common causes and fixes

| Cause | Evidence | Fix |
|---|---|---|
| Normal GPU node provisioning | GPU nodes take 3-5 min, hold is 2-3 min | Adjust alert threshold for GPU profiles, or suppress for GPU |
| Node stuck provisioning | In-flight node never transitions to Ready | Check cloud provider console for stuck instances |
| Slow device plugin registration | Node is Ready but `requiredLabels` not yet present | Check device plugin DaemonSet health on new nodes |
| Inflight node not transitioning | Node arrived but ledger still shows inflight | Check transition tracking (provision-task-id label matching) |

### Resolution

If the hold duration matches your expected node provisioning time, adjust the alert threshold:

```yaml
expr: histogram_quantile(0.99, rate(kompakt_gate_hold_duration_seconds_bucket[5m])) > 300
```

If nodes are stuck, investigate at the infrastructure level (cloud console, kubelet logs on the node).

---

## Custom alerts to consider

Beyond the four recommended alerts, consider adding:

### KompaktLeaderElectionLost

```yaml
alert: KompaktLeaderElectionLost
expr: kompakt_leader_election_master_status == 0
for: 30s
annotations:
  summary: Kompakt lost leader election, gated pods will not be released until re-election
```

### KompaktNoInflightDetection

```yaml
alert: KompaktNoInflightDetection
expr: kompakt_ledger_inflight_nodes == 0 and kompakt_gated_pods > 0
for: 5m
annotations:
  summary: Pods are gated but no in-flight nodes detected -- WaitForNodeReady cannot make decisions
```

### KompaktWebhookDown

```yaml
alert: KompaktWebhookDown
expr: up{job="kompakt"} == 0
for: 1m
annotations:
  summary: Kompakt is down. Webhook failure policy is Ignore, pods bypass gating silently
```
