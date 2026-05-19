#!/usr/bin/env bash
set -euo pipefail

IMG="${IMG:-kompakt:e2e}"
CLUSTER_NAME="${CLUSTER_NAME:-kompakt-dev}"
NAMESPACE="kompakt-system"
SECRET_NAME="kompakt-webhook-certs"
SERVICE_NAME="kompakt-controller"
WEBHOOK_NAME="kompakt-webhook"
CERT_DIR=$(mktemp -d)

echo "==> Creating kind cluster (if not exists)"
kind get clusters | grep -q "^${CLUSTER_NAME}$" || kind create cluster --name "${CLUSTER_NAME}" --image kindest/node:v1.31.0

echo "==> Building image"
docker build -t "${IMG}" .

echo "==> Loading image into kind"
kind load docker-image "${IMG}" --name "${CLUSTER_NAME}"

echo "==> Installing CRDs"
kubectl apply -f config/crd/bases/

echo "==> Generating self-signed TLS certs"
# CA
openssl genrsa -out "${CERT_DIR}/ca.key" 2048 2>/dev/null
openssl req -x509 -new -nodes -key "${CERT_DIR}/ca.key" -days 1 -out "${CERT_DIR}/ca.crt" \
  -subj "/CN=kompakt-ca" 2>/dev/null

# Server cert for webhook service
cat > "${CERT_DIR}/server.conf" <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_dn
prompt = no
[req_dn]
CN = ${SERVICE_NAME}.${NAMESPACE}.svc
[v3_req]
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = ${SERVICE_NAME}
DNS.2 = ${SERVICE_NAME}.${NAMESPACE}
DNS.3 = ${SERVICE_NAME}.${NAMESPACE}.svc
DNS.4 = ${SERVICE_NAME}.${NAMESPACE}.svc.cluster.local
EOF

openssl genrsa -out "${CERT_DIR}/tls.key" 2048 2>/dev/null
openssl req -new -key "${CERT_DIR}/tls.key" -out "${CERT_DIR}/server.csr" \
  -config "${CERT_DIR}/server.conf" 2>/dev/null
openssl x509 -req -in "${CERT_DIR}/server.csr" -CA "${CERT_DIR}/ca.crt" -CAkey "${CERT_DIR}/ca.key" \
  -CAcreateserial -out "${CERT_DIR}/tls.crt" -days 1 \
  -extensions v3_req -extfile "${CERT_DIR}/server.conf" 2>/dev/null

echo "==> Deploying with kustomize"
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# Create TLS secret before deploying manager
kubectl -n "${NAMESPACE}" create secret tls "${SECRET_NAME}" \
  --cert="${CERT_DIR}/tls.crt" --key="${CERT_DIR}/tls.key" \
  --dry-run=client -o yaml | kubectl apply -f -

# Deploy manager (set image to e2e, then restore kustomization.yaml)
(cd config/manager && kustomize edit set image "ghcr.io/reyshazni/kompakt=${IMG}")
kustomize build config/default | kubectl apply -f -
git checkout config/manager/kustomization.yaml 2>/dev/null || true

echo "==> Patching webhook CA bundle"
CA_BUNDLE=$(base64 < "${CERT_DIR}/ca.crt" | tr -d '\n')
kubectl patch mutatingwebhookconfiguration "${WEBHOOK_NAME}" \
  --type='json' \
  -p "[{\"op\":\"add\",\"path\":\"/webhooks/0/clientConfig/caBundle\",\"value\":\"${CA_BUNDLE}\"}]"

echo "==> Restarting controller to pick up latest image"
kubectl -n "${NAMESPACE}" rollout restart deployment/kompakt-controller

echo "==> Waiting for controller"
kubectl -n "${NAMESPACE}" rollout status deployment/kompakt-controller --timeout=90s || {
  echo "==> Controller failed to start. Logs:"
  kubectl -n "${NAMESPACE}" logs -l app.kubernetes.io/name=kompakt --tail=50 || true
  kubectl -n "${NAMESPACE}" describe pods || true
  exit 1
}

echo "==> Cleanup temp certs"
rm -rf "${CERT_DIR}"

echo "==> E2E setup complete"
