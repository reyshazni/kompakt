# Your First PackingProfile

This guide walks you through creating a PackingProfile that coordinates CPU and memory workloads across your cluster.

## 1. Create the PackingProfile

Save the following as `packingprofile.yaml`:

```yaml
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: general-cpu-coordination
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

  reservationTimeout: 3m
```

Apply it:

```bash
kubectl apply -f packingprofile.yaml
```

The profile defines HOW to coordinate. It does not select pods. Pods opt in by referencing this profile by name.

## 2. Label your workloads

Add the `packer.kompakt.io/packing-profile` label to pods you want coordinated. The label value must match the name of an existing PackingProfile.

For example, a Deployment:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-service
spec:
  replicas: 3
  selector:
    matchLabels:
      app: my-service
  template:
    metadata:
      labels:
        app: my-service
        packer.kompakt.io/packing-profile: general-cpu-coordination
    spec:
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
      containers:
        - name: app
          image: my-service:latest
          resources:
            requests:
              cpu: 1
              memory: 2Gi
```

If the referenced PackingProfile does not exist, pod creation is rejected with a clear error. No silent failures.

## 3. Verify it works

Check the profile is active:

```bash
kubectl get packingprofiles
```

```
NAME                         DEMAND            RULES                        ACTIVE GATES   AGE
general-cpu-coordination     ResourceRequest   BinPackOnInflightCapacity    0              10s
```

Deploy a workload and watch for gated pods:

```bash
kubectl get pods -o custom-columns=\
NAME:.metadata.name,\
STATUS:.status.phase,\
GATES:.spec.schedulingGates[*].name
```

Pods with the profile label will show `kompakt.io/awaiting-bin-pack` in the GATES column while the controller evaluates capacity. Once capacity is confirmed, the gate is removed and the pod schedules normally.

## 4. Understand what happened

When your labeled pods are created:

1. The Kompakt webhook intercepts the pod creation request
2. It reads the `packer.kompakt.io/packing-profile` label
3. It looks up the PackingProfile `general-cpu-coordination`
4. It injects `spec.schedulingGates: [{name: "kompakt.io/awaiting-bin-pack"}]`
5. The pod is stored in etcd as gated. The scheduler ignores it.
6. The controller checks the in-flight node ledger for available capacity
7. When capacity is confirmed, the controller removes the gate and optionally adds node affinity
8. The scheduler picks up the pod and binds it as usual

## Opting out specific pods

If a specific pod should bypass Kompakt gating entirely, add the exclude label (even if it has a profile label, it will be skipped):

```yaml
metadata:
  labels:
    kompakt.io/exclude: "true"
```

For critical workloads that should be gated but released immediately:

```yaml
metadata:
  annotations:
    kompakt.io/priority: "high"
```

## Next steps

- [CPU/Memory packing guide](../guides/cpu-memory-packing.md) for advanced configuration
- [GPU packing guide](../guides/gpu-packing.md) for fractional GPU workloads
- [How it works](../concepts/how-it-works.md) for a deeper understanding of the architecture
