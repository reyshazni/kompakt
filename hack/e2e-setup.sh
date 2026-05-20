#!/usr/bin/env bash
set -euo pipefail

IMG="${IMG:-kompakt:e2e}"
CLUSTER_NAME="${CLUSTER_NAME:-kompakt-dev}"
NAMESPACE="kompakt-system"

echo "==> Creating kind cluster (if not exists)"
kind get clusters | grep -q "^${CLUSTER_NAME}$" || kind create cluster --name "${CLUSTER_NAME}" --image kindest/node:v1.31.0

echo "==> Building image"
docker build -t "${IMG}" .

echo "==> Loading image into kind"
kind load docker-image "${IMG}" --name "${CLUSTER_NAME}"

echo "==> Installing CRDs"
kubectl apply -f config/crd/bases/

echo "==> Deploying with kustomize"
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# TLS certs are provisioned in-process by the controller at startup.
# No external cert generation, Secret creation, or caBundle patching needed.

# Deploy manager (set image to e2e, then restore kustomization.yaml)
(cd config/manager && kustomize edit set image "ghcr.io/reyshazni/kompakt=${IMG}")
kustomize build config/default | kubectl apply -f -
git checkout config/manager/kustomization.yaml 2>/dev/null || true

echo "==> Restarting controller to pick up latest image"
kubectl -n "${NAMESPACE}" rollout restart deployment/kompakt-controller

echo "==> Waiting for controller"
kubectl -n "${NAMESPACE}" rollout status deployment/kompakt-controller --timeout=90s || {
  echo "==> Controller failed to start. Logs:"
  kubectl -n "${NAMESPACE}" logs -l app.kubernetes.io/name=kompakt --tail=50 || true
  kubectl -n "${NAMESPACE}" describe pods || true
  exit 1
}

echo "==> E2E setup complete"
