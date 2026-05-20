# Uninstall

## Emergency removal

If something goes wrong and you need to remove Kompakt from the request path immediately:

```bash
kubectl delete mutatingwebhookconfiguration kompakt-webhook
```

This restores default cluster behavior within seconds. Any currently-gated pods will remain gated but can be force-released:

```bash
kubectl get pods --all-namespaces -o json | \
  jq -r '.items[] | select(.spec.schedulingGates[]?.name | startswith("kompakt.io/")) | "\(.metadata.namespace) \(.metadata.name)"' | \
  while read ns name; do
    kubectl patch pod "$name" -n "$ns" --type=json \
      -p '[{"op":"remove","path":"/spec/schedulingGates"}]'
  done
```

Because Kompakt never modifies the scheduler or autoscaler, there is no rollback procedure beyond removing the webhook. Your cluster reverts to pre-Kompakt behavior instantly.

## Clean uninstall

### Helm

```bash
helm uninstall kompakt -n kompakt-system
kubectl delete namespace kompakt-system
```

### Kustomize

```bash
kubectl delete -k https://github.com/reyshazni/kompakt/config/default
```

### Plain kubectl

```bash
kubectl delete mutatingwebhookconfiguration kompakt-webhook
kubectl delete -f https://raw.githubusercontent.com/reyshazni/kompakt/main/config/manager/manager.yaml
kubectl delete -f https://raw.githubusercontent.com/reyshazni/kompakt/main/config/manager/service.yaml
kubectl delete -f https://raw.githubusercontent.com/reyshazni/kompakt/main/config/rbac/role_binding.yaml
kubectl delete -f https://raw.githubusercontent.com/reyshazni/kompakt/main/config/rbac/role.yaml
kubectl delete -f https://raw.githubusercontent.com/reyshazni/kompakt/main/config/rbac/leader_election_role_binding.yaml
kubectl delete -f https://raw.githubusercontent.com/reyshazni/kompakt/main/config/rbac/leader_election_role.yaml
kubectl delete -f https://raw.githubusercontent.com/reyshazni/kompakt/main/config/rbac/service_account.yaml
kubectl delete namespace kompakt-system
```

### Remove CRDs

Helm does not delete CRDs on uninstall. Remove them manually if you want a complete cleanup:

```bash
kubectl delete crd packingprofiles.packer.kompakt.io
```

!!! warning
    Deleting the CRD also deletes all PackingProfile resources in the cluster. Make sure you have backups if you plan to reinstall later.

## Next steps

- [Installation](installation.md) to reinstall Kompakt
- [Troubleshooting](../guides/troubleshooting.md) if you are uninstalling due to issues
