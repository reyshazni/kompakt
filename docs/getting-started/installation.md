# Installation

## What you need

- A Kubernetes cluster (version 1.30 or newer)
- `kubectl` connected to your cluster
- `helm` (version 3 or newer)

Not sure if `kubectl` is working? Run this:

```bash
kubectl version
```

If you see a server version, you're good.

## Quick install (one command)

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace
```

That's it. Kompakt is now running.

## Check that it worked

Run these three commands:

```bash
# 1. Is the controller running?
kubectl get pods -n kompakt-system
```

You should see something like:

```
NAME                                  READY   STATUS    RESTARTS   AGE
kompakt-controller-6f8b9d4c5-x7k2p   1/1     Running   0          30s
```

`1/1 Running` means it's healthy. If it shows `0/1` or `CrashLoopBackOff`, wait
30 seconds and check again -- the controller needs a moment to start.

```bash
# 2. Is the webhook registered?
kubectl get mutatingwebhookconfiguration kompakt-webhook
```

You should see one entry. If not, check the controller logs (see troubleshooting below).

```bash
# 3. Was the TLS certificate created?
kubectl get secret kompakt-webhook-certs -n kompakt-system
```

You should see a secret of type `kubernetes.io/tls`.

## Install in a custom namespace

By default, Kompakt installs into `kompakt-system`. To use a different namespace:

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n my-namespace --create-namespace
```

Everything (controller, secrets, RBAC) will be created in `my-namespace` instead.

## Use a private image registry

If your cluster can't pull from `ghcr.io` (e.g. air-gapped or behind a firewall),
point Kompakt to your own registry:

```bash
helm install kompakt oci://ghcr.io/reyshazni/charts/kompakt \
  -n kompakt-system --create-namespace \
  --set image.repository=your-registry.example.com/kompakt \
  --set image.tag=v0.1.0
```

Examples for common registries:

```bash
# Alibaba ACR
--set image.repository=registry.ap-southeast-5.aliyuncs.com/my-ns/kompakt

# AWS ECR
--set image.repository=123456789.dkr.ecr.us-east-1.amazonaws.com/kompakt

# Google Artifact Registry
--set image.repository=us-docker.pkg.dev/my-project/my-repo/kompakt
```

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

If you prefer kustomize over Helm:

```bash
kubectl apply -k https://github.com/reyshazni/kompakt/config/default
```

To customize, create a `kustomization.yaml` that references the default overlay:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: kompakt-system
resources:
  - https://github.com/reyshazni/kompakt/config/default
```

To customize, create a patch file alongside your `kustomization.yaml`:

```yaml
# kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: my-namespace
resources:
  - https://github.com/reyshazni/kompakt/config/default
images:
  - name: ghcr.io/reyshazni/kompakt
    newName: your-registry.example.com/kompakt
    newTag: v0.1.0
patches:
  - path: patch-manager.yaml
```

```yaml
# patch-manager.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kompakt-controller
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: manager
          args:
            - --leader-elect
            - --zap-log-level=1
          resources:
            requests:
              cpu: 100m
              memory: 64Mi
            limits:
              cpu: "1"
              memory: 256Mi
```

**What you can customize:**

| Field | Default | Notes |
|---|---|---|
| `namespace` | `kompakt-system` | Change in kustomization.yaml |
| `images` | `ghcr.io/reyshazni/kompakt:latest` | For private registry |
| `replicas` | `1` | Increase for HA (leader election built-in) |
| `resources.requests.cpu` | `50m` | See [Resource Sizing](../guides/resource-sizing.md) |
| `resources.requests.memory` | `64Mi` | Flat, does not scale with pod count |
| `resources.limits.cpu` | `500m` | ~200 concurrent gated pods |
| `resources.limits.memory` | `128Mi` | 2x request, covers GC spikes |
| `--zap-log-level` | `0` (info) | `1`=debug, `3`=verbose, `4`=trace |

Then apply:

```bash
kubectl create namespace my-namespace
kubectl apply -k .
```

## Troubleshooting

### Controller pod is not starting

Check the logs:

```bash
kubectl logs -n kompakt-system -l app.kubernetes.io/name=kompakt
```

Look for `"certs provisioned successfully"`. If you see that, the controller started
correctly and the issue is elsewhere.

### Pod shows `CrashLoopBackOff`

This usually means the controller crashed during startup. Common causes:

- **"read-only file system" in logs**: Your deployment is missing the `emptyDir` volume
  mount. This is included in the default Helm chart and kustomize manifests. If you wrote
  custom manifests, make sure the deployment has a writable volume at
  `/tmp/k8s-webhook-server/serving-certs`.

- **RBAC errors in logs**: The controller needs permission to create Secrets and patch
  the webhook configuration. Re-apply the manifests to ensure RBAC is up to date.

### Pods are not being gated

If you created a PackingProfile and labeled your pods but they are not getting
scheduling gates:

1. Check that the webhook is registered: `kubectl get mutatingwebhookconfiguration`
2. Check that your pod's namespace is not excluded (kube-system, kube-public, and
   the controller namespace are excluded by default)
3. Check controller logs for errors

### Webhook returns "connection refused"

The controller needs a few seconds after startup to generate certs and start the TLS
server. During this window, the webhook has `failurePolicy: Ignore`, meaning pods pass
through ungated. This is normal and resolves itself within seconds.

## Upgrade

```bash
helm upgrade kompakt oci://ghcr.io/reyshazni/charts/kompakt -n kompakt-system
```

## Next steps

- [Create your first PackingProfile](first-profile.md)
