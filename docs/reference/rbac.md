# RBAC Requirements

Kompakt is designed with minimal RBAC. No cluster-admin, no wildcard permissions, no write access to kube-system.

## Required permissions

| Resource | API Group | Verbs | Scope | Purpose |
|---|---|---|---|---|
| `pods` | core | get, list, watch, patch | Cluster-wide | Read gated pods, remove scheduling gates, add node affinity |
| `nodes` | core | get, list, watch | Cluster-wide | Track node capacity and readiness |
| `packingprofiles` | packer.kompakt.io | get, list, watch | Cluster-wide | Read PackingProfile CRDs |
| `packingprofiles/status` | packer.kompakt.io | update, patch | Cluster-wide | Update active gate counts |
| `leases` | coordination.k8s.io | get, list, watch, create, update | kompakt-system only | Leader election |
| `events` | core | create, patch | Cluster-wide | Emit Kubernetes events for gate/release actions |
| `configmaps` | core | get, list, watch | kube-system (read-only) | Read `cluster-autoscaler-status` for in-flight detection |

## What Kompakt does NOT require

| Permission | Why not needed |
|---|---|
| cluster-admin | No wildcard permissions needed |
| Write to kube-system | Only reads the CA status ConfigMap |
| Write to scheduler config | Does not modify the scheduler |
| Write to autoscaler config | Does not modify the autoscaler |
| Privileged containers | No host-level access needed |
| hostPath volumes | No host filesystem access |
| hostNetwork | No host networking |
| hostPID | No host process visibility |

## ClusterRole manifest

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kompakt-manager
rules:
  # Core: manage gated pods
  - apiGroups: [""]
    resources: [pods]
    verbs: [get, list, watch, patch]

  # Core: track node capacity
  - apiGroups: [""]
    resources: [nodes]
    verbs: [get, list, watch]

  # Core: read PackingProfile CRDs
  - apiGroups: [packer.kompakt.io]
    resources: [packingprofiles]
    verbs: [get, list, watch]

  # Core: update PackingProfile status
  - apiGroups: [packer.kompakt.io]
    resources: [packingprofiles/status]
    verbs: [update, patch]

  # Observability: emit events
  - apiGroups: [""]
    resources: [events]
    verbs: [create, patch]

  # In-flight detection: read CA status
  - apiGroups: [""]
    resources: [configmaps]
    verbs: [get, list, watch]
    resourceNames: [cluster-autoscaler-status]
```

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kompakt-leader-election
  namespace: kompakt-system
rules:
  - apiGroups: [coordination.k8s.io]
    resources: [leases]
    verbs: [get, list, watch, create, update]
```

## Security review checklist

Hand this to your security team:

1. Does it require cluster-admin? **No**
2. Does it modify kube-scheduler? **No**
3. Does it modify cluster-autoscaler? **No**
4. Does it run privileged containers? **No**
5. Does it use hostPath, hostNetwork, or hostPID? **No**
6. Does it write to kube-system? **No** (read-only access to one ConfigMap)
7. What does it write to pods? **Only `spec.schedulingGates` and `spec.affinity`**
8. Can it be removed instantly? **Yes** (`kubectl delete mutatingwebhookconfiguration kompakt-webhook`)
