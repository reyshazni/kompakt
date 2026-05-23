# Installation

## What you need

- A Kubernetes cluster (version 1.30 or newer)
- `kubectl` connected to your cluster
- `helm` (version 3 or newer)

Not sure if `kubectl` is working? Run `kubectl version` -- if you see a server version, you're good.

## Quick install (one command)

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace
```

That's it. Kompakt is now running.

## Check that it worked
```bash
# 1. Is the controller running?
kubectl get pods -n kompakt-system
```

Look for `1/1 Running`. If it shows `0/1` or `CrashLoopBackOff`, wait 30 seconds and try again.

```bash
# 2. Is the webhook registered?
kubectl get mutatingwebhookconfiguration kompakt-webhook
```

You should see one entry.

```bash
# 3. Was the TLS certificate created?
kubectl get secret kompakt-webhook-certs -n kompakt-system
```

You should see a secret of type `kubernetes.io/tls`.

## Install in a custom namespace
```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n my-namespace --create-namespace
```

## Use a private image registry
If your cluster can't pull from `ghcr.io`, point to your own registry:

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace \
  --set image.repository=your-registry.example.com/kompakt \
  --set image.tag=v0.1.0
```

| Registry | `--set image.repository` value |
|---|---|
| Alibaba ACR | `registry.ap-southeast-5.aliyuncs.com/my-ns/kompakt` |
| AWS ECR | `123456789.dkr.ecr.us-east-1.amazonaws.com/kompakt` |
| Google Artifact Registry | `us-docker.pkg.dev/my-project/my-repo/kompakt` |

## Customize resources and replicas

Create a file called `values.yaml`:

```yaml
# Run 2 replicas for high availability
replicaCount: 2

# Adjust resource limits for your cluster
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 256Mi
```

Then install with:

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace \
  -f values.yaml
```

## Alternative: install with kustomize

```bash
kubectl apply -k https://github.com/reyshazni/kompakt/config/default
```

For kustomize customization options (replicas, image, resources), see the kustomize overlay in the [GitHub repository](https://github.com/reyshazni/kompakt/tree/main/config/default).

## Troubleshooting

Having trouble? See [Troubleshooting](../guides/troubleshooting.md).

## Upgrade

```bash
helm upgrade kompakt oci://ghcr.io/reyshazni/charts/kompakt -n kompakt-system
```

## Next steps

- [Create your first PackingProfile](first-profile.md)
