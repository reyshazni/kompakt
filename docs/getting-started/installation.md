# Installation

## Prerequisites

- Kubernetes >= 1.30 (scheduling gates must be GA)
- `kubectl` configured for your cluster

## Option 1: Helm

```bash
helm repo add kompakt https://reyshazni.github.io/kompakt
helm repo update
helm install kompakt kompakt/kompakt -n kompakt-system --create-namespace
```

Install with custom values:

```bash
helm install kompakt kompakt/kompakt \
  -n kompakt-system \
  --create-namespace \
  -f values.yaml
```

Common overrides:

```yaml
# values.yaml
replicaCount: 2

webhook:
  failurePolicy: Ignore  # default for v0.x, set to Fail for v1.0 with HA

resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 256Mi
```

## Option 2: Kustomize

```bash
kubectl apply -k https://github.com/reyshazni/kompakt/config/default
```

Or clone and customize:

```bash
git clone https://github.com/reyshazni/kompakt.git
cd kompakt

# Edit config/manager/kustomization.yaml to set your image tag
kustomize build config/default | kubectl apply -f -
```

## Option 3: Plain kubectl

```bash
git clone https://github.com/reyshazni/kompakt.git
cd kompakt

# Apply all manifests directly
kubectl create namespace kompakt-system
kubectl apply -f config/crd/bases/
kubectl apply -f config/rbac/
kubectl apply -f config/manager/manager.yaml
kubectl apply -f config/manager/service.yaml
kubectl apply -f config/webhook/webhook.yaml
```

## Verify

Check that the controller pod is running:

```bash
kubectl get pods -n kompakt-system
```

Expected output:

```
NAME                                  READY   STATUS    RESTARTS   AGE
kompakt-controller-6f8b9d4c5-x7k2p   1/1     Running   0          30s
```

Check that the webhook is registered:

```bash
kubectl get mutatingwebhookconfiguration | grep kompakt
```

## Next steps

- [Create your first PackingProfile](first-profile.md)
