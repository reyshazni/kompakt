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

  capacitySource:
    type: NodeAllocatable
    resources: [cpu, memory]

  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"

  rules:
    - name: WaitForWorkloadPacking
    - name: WaitForNodeReady

  reservationTimeout: 3m
```

Apply it:

```bash
kubectl apply -f packingprofile.yaml
```

This profile tells Kompakt: measure what each pod needs by reading its CPU and memory [resource requests](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/) (`demandSource`), measure what nodes can provide by reading their [allocatable](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/) resources (`capacitySource`), consider a node ready when its `Ready` [condition](https://kubernetes.io/docs/reference/node/node-status/) is True (`readinessSignal`), and use both bin-packing and scale-up coordination rules.

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
general-cpu-coordination     ResourceRequest   WaitForWorkloadPacking    0              10s
```

Deploy a workload and watch for gated pods:

```bash
kubectl get pods -o custom-columns=\
NAME:.metadata.name,\
STATUS:.status.phase,\
GATES:.spec.schedulingGates[*].name
```

Pods with the profile label will show `kompakt.io/wait-for-workload-packing` in the GATES column while the controller evaluates capacity. Once capacity is confirmed, the gate is removed and the pod schedules normally.

For a detailed explanation of the gating and release flow, see [How It Works](../concepts/how-it-works.md).

## Next steps

Your cluster is now coordinating pod placement. For production tuning, see the [CPU/Memory packing guide](../guides/cpu-memory-packing.md). For GPU workloads, see the [GPU packing guide](../guides/gpu-packing.md). To understand the full gating lifecycle, read [How It Works](../concepts/how-it-works.md).
