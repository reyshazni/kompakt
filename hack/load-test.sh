#!/usr/bin/env bash
# load-test.sh - Measure kompakt resource consumption under varying pod counts.
#
# Usage: hack/load-test.sh [POD_COUNTS]
# Example: hack/load-test.sh "10 50 100 200"
#
# Prerequisites:
#   - kind cluster with kompakt deployed (hack/e2e-setup.sh)
#   - metrics-server installed
#
# Output: prints a table of pod count, CPU, and memory usage.

set -euo pipefail

POD_COUNTS="${1:-10 50 100 200}"
PROFILE_NAME="load-test-profile"
NAMESPACE="default"

cleanup() {
  echo "==> Cleaning up load test resources"
  kubectl delete pods -l load-test=kompakt -n "${NAMESPACE}" --ignore-not-found --wait=false 2>/dev/null || true
  kubectl delete packingprofile "${PROFILE_NAME}" --ignore-not-found 2>/dev/null || true
}
trap cleanup EXIT

echo "==> Creating load test profile"
cat <<EOF | kubectl apply -f -
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: ${PROFILE_NAME}
spec:
  demandSource:
    type: ResourceRequest
    resources: [cpu, memory]
  capacitySource:
    type: NodeAllocatable
    resources: [cpu, memory]
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: BinPackOnInflightCapacity
  reservationTimeout: 10m
EOF

echo ""
echo "==> Measuring baseline (0 gated pods)"
sleep 2
BASELINE=$(kubectl top pods -n kompakt-system -l app.kubernetes.io/name=kompakt --no-headers 2>/dev/null | awk '{print $2, $3}')
echo "   baseline: ${BASELINE}"
echo ""

printf "%-12s %-12s %-14s %-10s\n" "PODS" "CPU" "MEMORY" "STATUS"
printf "%-12s %-12s %-14s %-10s\n" "----" "---" "------" "------"
printf "%-12s %-12s %-14s %-10s\n" "0 (idle)" "${BASELINE}" "" "baseline"

for COUNT in ${POD_COUNTS}; do
  # Clean previous batch
  kubectl delete pods -l load-test=kompakt -n "${NAMESPACE}" --ignore-not-found --wait=false 2>/dev/null || true
  sleep 2

  # Create pods in parallel (huge resources so they stay gated)
  echo "==> Creating ${COUNT} gated pods..."
  for i in $(seq 1 "${COUNT}"); do
    cat <<EOF | kubectl apply -f - &
apiVersion: v1
kind: Pod
metadata:
  name: load-test-pod-${i}
  namespace: ${NAMESPACE}
  labels:
    packer.kompakt.io/packing-profile: ${PROFILE_NAME}
    load-test: kompakt
spec:
  terminationGracePeriodSeconds: 1
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: "100"
          memory: 1Ti
EOF
  done
  wait

  # Let controller reconcile all pods (requeues every 1s)
  SETTLE=$((COUNT / 10))
  if [ "${SETTLE}" -lt 5 ]; then SETTLE=5; fi
  if [ "${SETTLE}" -gt 30 ]; then SETTLE=30; fi
  echo "   waiting ${SETTLE}s for reconciliation to settle..."
  sleep "${SETTLE}"

  # Measure
  METRICS=$(kubectl top pods -n kompakt-system -l app.kubernetes.io/name=kompakt --no-headers 2>/dev/null | awk '{print $2, $3}')
  CPU=$(echo "${METRICS}" | awk '{print $1}')
  MEM=$(echo "${METRICS}" | awk '{print $2}')

  # Count how many are actually gated
  GATED=$(kubectl get pods -n "${NAMESPACE}" -l load-test=kompakt -o jsonpath='{.items[*].spec.schedulingGates[*].name}' 2>/dev/null | tr ' ' '\n' | grep -c "kompakt.io/" || echo 0)

  printf "%-12s %-12s %-14s %-10s\n" "${COUNT}" "${CPU}" "${MEM}" "${GATED} gated"
done

echo ""
echo "==> Summary"
echo "Use these measurements to set resource requests/limits:"
echo "  requests: set to p50 observed usage + 20% headroom"
echo "  limits: set to peak observed usage + 50% headroom"
echo ""
echo "The controller CPU scales linearly with gated pod count"
echo "because each pod triggers a reconcile loop every 1s."
echo "Memory scales with total tracked pods + nodes in the ledger."
