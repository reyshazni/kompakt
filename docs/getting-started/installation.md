# Installation

## Prerequisites

- Kubernetes >= 1.30 (scheduling gates must be GA)
- Helm 3
- `kubectl` configured for your cluster

## Install with Helm

```bash
helm repo add kompakt https://reyshazni.github.io/kompakt
helm repo update
helm install kompakt kompakt/kompakt -n kompakt-system --create-namespace
```

## Verify

Check that the webhook and controller pods are running:

```bash
kubectl get pods -n kompakt-system
```

Expected output:

```
NAME                                  READY   STATUS    RESTARTS   AGE
kompakt-controller-6f8b9d4c5-x7k2p   1/1     Running   0          30s
```

Check that the webhook configuration is registered:

```bash
kubectl get mutatingwebhookconfiguration | grep kompakt
```

## Helm values

Common overrides:

```yaml
# values.yaml
replicaCount: 2

webhook:
  failurePolicy: Ignore  # default for v0.x, set to Fail for v1.0 with HA

resources:
  requests:
    cpu: 100m
    memory: 100Mi
  limits:
    cpu: 200m
    memory: 250Mi
```

Install with custom values:

```bash
helm install kompakt kompakt/kompakt \
  -n kompakt-system \
  --create-namespace \
  -f values.yaml
```

## Install from source

```bash
git clone https://github.com/reyshazni/kompakt.git
cd kompakt
make deploy IMG=ghcr.io/reyshazni/kompakt:latest
```

## Next steps

- [Create your first PackingProfile](first-profile.md)
