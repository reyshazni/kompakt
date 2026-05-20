# Resource Sizing Guide

This guide explains how Kompakt's resource consumption works, provides
data-backed defaults, and shows you how to run your own measurements.

## How Kompakt uses resources

Kompakt runs as a single binary with two workloads:

**Webhook (admission handler):** Stateless. Receives pod CREATE requests,
looks up the profile, injects scheduling gates. Runs on all replicas.
Per-request cost is sub-millisecond with no allocations beyond the JSON
patch. Resource usage is negligible.

**Controller (reconciler):** Watches gated pods. For each gated pod, runs
a reconcile loop every 1 second that:

1. Lists all nodes (API server cache hit)
2. Lists all pods (API server cache hit)
3. Rebuilds the in-memory ledger
4. Evaluates bin-packing rules
5. Patches the pod if capacity is available

CPU scales linearly with the number of concurrently gated pods because
each one triggers its own reconcile loop. Memory stays flat because the
ledger is rebuilt from scratch on each reconcile.

## Measured data

Tested on a kind cluster (single node, K8s 1.31) with `metrics-server`.
All pods request 100 CPU / 1Ti memory to stay permanently gated (worst
case for the controller, since it requeues every 1s without releasing).

| Gated Pods | CPU (cores) | Memory |
|------------|-------------|--------|
| 0 (idle)   | 2m          | 30Mi   |
| 10         | 2m          | 34Mi   |
| 50         | 2m          | 34Mi   |
| 100        | 176m        | 25Mi   |
| 200        | 498m        | 32Mi   |

**Observations:**

- CPU is ~2.5m per concurrently gated pod at steady state
- Memory is flat at ~30-34Mi regardless of pod count
- The jump between 50 and 100 pods reflects the controller-runtime work
  queue processing more items per second
- In practice, pods are gated for seconds to minutes, not permanently,
  so the concurrent gated count is much lower than total throughput

## Default resource values

Based on the measurements above, the defaults in `values.yaml` target a
cluster with up to ~100 concurrently gated pods:

```yaml
resources:
  requests:
    cpu: 50m      # idle + headroom for 10-20 concurrent gates
    memory: 64Mi  # observed peak ~34Mi + 80% headroom
  limits:
    cpu: 500m     # supports ~200 concurrent gated pods
    memory: 128Mi # 4x observed peak, allows for GC spikes
```

## How to size for your cluster

The only variable that matters is **peak concurrent gated pods**. Not
total pods, not total nodes, not throughput. If your cluster creates 1000
pods/hour but each is gated for 5 seconds, the concurrent count is
~1.4 (1000 * 5 / 3600).

**Step 1:** Estimate your peak concurrent gated pods.

```
concurrent = (pods_per_hour * average_gate_duration_seconds) / 3600
```

For scale-up events (the primary use case), gate duration is typically
the time for a new node to join the cluster (30s-5min depending on
cloud provider).

**Step 2:** Calculate CPU request.

```
cpu_request = 5m + (concurrent_pods * 2.5m)
```

Add 20% headroom and round up to the nearest 10m.

**Step 3:** Set memory request to 64Mi.

Memory does not scale with pod count. 64Mi is sufficient for any
reasonable workload. Only increase if you have 1000+ nodes (larger
informer cache).

**Step 4:** Set limits.

```
cpu_limit    = cpu_request * 3   # allow burst during scale-up storms
memory_limit = 128Mi             # 2x request, covers GC pressure
```

### Examples

| Scenario | Concurrent gates | CPU request | CPU limit | Memory request | Memory limit |
|----------|-----------------|-------------|-----------|----------------|--------------|
| Small (dev/staging) | 5-10 | 30m | 100m | 64Mi | 128Mi |
| Medium (50-node prod) | 20-50 | 130m | 400m | 64Mi | 128Mi |
| Large (200-node prod) | 100-200 | 500m | 1500m | 64Mi | 128Mi |
| Burst (500+ concurrent) | 500+ | 1300m | 4000m | 64Mi | 256Mi |

## Run your own measurements

The load test script lets you measure on your actual cluster:

```bash
# Prerequisites: metrics-server installed, kompakt deployed
hack/load-test.sh "10 50 100 200 500"
```

The script:

1. Creates a PackingProfile with 10m reservation timeout
2. For each count: creates N pods with huge resource requests (stays gated)
3. Waits for the controller to reach steady state
4. Reads `kubectl top` for the controller pod
5. Prints a table of results
6. Cleans up all test resources

Run this on a cluster that matches your production topology (same node
count, same number of existing pods) for the most accurate results. The
controller's CPU usage depends on the total number of objects it lists
during each reconcile, which varies with cluster size.

## What NOT to do

- Do not set memory limits below 64Mi. The Go runtime needs headroom for
  garbage collection.
- Do not set CPU requests to 0. The controller needs guaranteed CPU to
  maintain its leader election lease. If it gets starved, the lease
  expires and all gated pods stall until a new leader is elected (~15s).
- Do not copy resource values from other operators. Kompakt's profile is
  unique: CPU-bound on reconcile frequency, not on informer sync or
  deep object processing.
