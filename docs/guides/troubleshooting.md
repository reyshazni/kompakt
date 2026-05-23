# Troubleshooting

## When you need this

Something is not working as expected. Pods are stuck, the webhook is not intercepting, or nodes are still over-provisioning. This guide covers the most common issues and how to diagnose them.

## Pod stuck in SchedulingGated

**Symptom**: Pod stays in `SchedulingGated` phase and never schedules.

**Diagnosis**:

```bash
# Check which gates are active
kubectl get pod <pod-name> -o jsonpath='{.spec.schedulingGates}'

# Check the PackingProfile status
kubectl get packingprofiles

# Check controller logs
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt --tail=100
```

**Common causes**:

1. **Reservation timeout too high**: The controller is waiting for capacity that is slow to arrive. Lower `reservationTimeout` for faster fallback.
2. **No in-flight node detection**: The autoscaler is provisioning nodes but the controller does not detect them. Check that the correct adapter is active (see [In-flight Detection](../concepts/inflight-detection.md)).
3. **Controller is down**: If the controller pod is not running, gates are never released. Check `kubectl get pods -n kompakt-system`.

**Immediate fix**: Remove the gate manually:

```bash
kubectl patch pod <pod-name> --type=json \
  -p '[{"op":"remove","path":"/spec/schedulingGates"}]'
```

## Pod rejected with "PackingProfile not found"

**Symptom**: Pod creation fails with error message referencing a PackingProfile that does not exist.

**Cause**: The pod has label `packer.kompakt.io/packing-profile: <name>` but no PackingProfile with that name exists in the cluster.

**Fix**: Either create the PackingProfile or remove the label from the pod spec.

```bash
# Check existing profiles
kubectl get packingprofiles

# Check the label on the pod template
kubectl get deployment <name> -o jsonpath='{.spec.template.metadata.labels}'
```

## Webhook not intercepting pods

**Symptom**: Pods with the profile label are created without scheduling gates.

**Diagnosis**:

```bash
# Check webhook configuration exists
kubectl get mutatingwebhookconfiguration | grep kompakt

# Check webhook pod is running
kubectl get pods -n kompakt-system

# Check webhook logs
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt --tail=100
```

**Common causes**:

1. **Webhook not installed**: The `MutatingWebhookConfiguration` is missing. Reinstall with Helm.
2. **Webhook pod is down**: The webhook pod crashed or is not scheduled. Check pod events.
3. **Failure policy is Ignore**: In v0.x, webhook outages cause pods to bypass gating silently. This is by design.
4. **Missing label**: The pod does not have the `packer.kompakt.io/packing-profile` label. Only pods with this label are gated.

## High webhook latency

**Symptom**: `kompakt_webhook_duration_seconds` p99 exceeds 50ms.

**Common causes**:

1. **Informer cache not synced**: After controller restart, the cache needs a few seconds to populate. Latency should normalize within 10-30 seconds.
2. **Too many PackingProfiles**: The webhook looks up profiles from the cache. This should be fast, but an unusually large number of profiles could add latency.
3. **Resource pressure**: The webhook pod does not have enough CPU or memory. Increase resource requests.

## Nodes still over-provisioning

**Symptom**: Kompakt is installed and gating pods, but the cluster autoscaler still provisions more nodes than expected.

**Common causes**:

1. **Not all workloads labeled**: Only pods with the `packer.kompakt.io/packing-profile` label are gated. Unlabeled pods that scale simultaneously will still cause independent scale-ups.
2. **Reservation timeout too short**: If the timeout expires before capacity is confirmed, pods are released uncoordinated.
3. **In-flight detection not working**: If the controller cannot detect incoming nodes, it cannot reserve capacity on them. Check `kompakt_inflight_nodes_total` metric.

## Emergency recovery

If Kompakt is causing problems and you need to remove it immediately:

```bash
# Step 1: Remove webhook (stops all gating within seconds)
kubectl delete mutatingwebhookconfiguration kompakt-webhook

# Step 2: Release all gated pods
kubectl get pods --all-namespaces -o json | \
  jq -r '.items[] | select(.spec.schedulingGates[]?.name | startswith("kompakt.io/")) | "\(.metadata.namespace) \(.metadata.name)"' | \
  while read ns name; do
    kubectl patch pod "$name" -n "$ns" --type=json \
      -p '[{"op":"remove","path":"/spec/schedulingGates"}]'
  done

# Step 3: Full cleanup (optional)
helm uninstall kompakt -n kompakt-system
```
