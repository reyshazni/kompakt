# Decision: Template Matching Strategy

## Context

Kompakt needs to associate detected in-flight nodes with expected allocatable resources. The `NodeGroupTemplate` in PackingProfile provides this mapping. Question: what key to match on?

## Options considered

### 1. namePrefix (current, for CA detector)

```yaml
nodeGroupTemplates:
  - namePrefix: pool-gpu
    allocatable: { aliyun.com/gpu-mem: 49152 }
```

Works for upstream CA where inflight node names are `pool-gpu-pending-0` (synthetic names from the CA status ConfigMap parser).

Does NOT work for:
- ACK: node names are IP-based (`cn-jakarta.172.16.1.10`)
- GOATScaler events: node names are ACK internal IDs (`asa-k1abq7t6148jm1hukffb`)

### 2. Node label matching

```yaml
nodeGroupTemplates:
  - nodeLabel:
      key: node.kubernetes.io/instance-type
      value: ecs.gn8is.4xlarge
    allocatable: { aliyun.com/gpu-mem: 49152 }
```

Works for NotReady node detection (labels are set during registration before Ready). Does NOT work for event-based detection (no Node object exists yet, so no labels to read).

### 3. instanceType (chosen for GOATScaler)

```yaml
nodeGroupTemplates:
  - instanceType: ecs.gn8is.4xlarge
    allocatable: { aliyun.com/gpu-mem: 49152 }
```

Works for GOATScaler event-based detection: instance type is directly in the `ProvisionNode` event message. Human-readable, stable across clusters, deterministic from cloud provider.

## Decision

Support both `namePrefix` and `instanceType` in `NodeGroupTemplate`. Match logic:

1. If node has `InstanceType` and template has `instanceType`: match by instance type (GOATScaler path)
2. If template has `namePrefix`: match by node name prefix (CA path)

This keeps backward compatibility for CA users and adds native support for ACK/GOATScaler.

## Instance types reference (common GPU)

| Cloud | Instance Type | GPU | VRAM |
|---|---|---|---|
| ACK | ecs.gn8is.4xlarge | 1x A30 | 24 GiB |
| ACK | ecs.gn7i-c16g1.4xlarge | 1x A10 | 24 GiB |
| ACK | ecs.gn6v-c8g1.16xlarge | 4x V100 | 64 GiB |
| AWS | p4d.24xlarge | 8x A100 | 320 GiB |
| AWS | g5.xlarge | 1x A10G | 24 GiB |
| GCP | a2-highgpu-1g | 1x A100 | 40 GiB |
