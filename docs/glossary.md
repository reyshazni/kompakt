---
description: Definitions of Kubernetes, autoscaler, and GPU sharing terms used throughout the Kompakt documentation.
---

# Glossary

## Kubernetes concepts

**[Scheduling gates](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/)**
:   A Kubernetes feature (GA since v1.30) that makes a pod invisible to the scheduler and autoscaler. A gated pod exists in etcd but consumes zero scheduling cycles. Gates are removed one at a time; the pod becomes schedulable only when all gates are gone. Kompakt uses scheduling gates to control when pods become visible to the autoscaler.

**[Device plugin](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/)**
:   A Kubernetes framework for exposing custom hardware (GPUs, FPGAs, etc.) to the kubelet. Runs as a DaemonSet on each node. Registers extended resources (like `nvidia.com/gpu`) after the node joins the cluster. The cluster autoscaler's node template does not include these resources because they are registered dynamically after boot.

**[Allocatable](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/)**
:   The amount of compute resources on a node available for scheduling pods. Equals total node capacity minus resources reserved for system daemons (kubelet, OS kernel). Always use allocatable (not capacity) when configuring Kompakt's `nodeGroupTemplates`.

**[Node conditions](https://kubernetes.io/docs/reference/node/node-status/)**
:   Status fields on a Node object reporting health. The most important is `Ready`: `True` means the node can accept pods, `False` or `Unknown` means it cannot. Kompakt uses `Ready=True` as the default readiness signal for releasing gated pods.

**[Resource requests](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)**
:   CPU and memory amounts a container declares it needs. The scheduler uses requests to decide node placement. CPU uses millicores (1000m = 1 core, written with suffix `m`). Memory uses bytes (suffixes: `Mi` for mebibytes, `Gi` for gibibytes).

**[Extended resources](https://kubernetes.io/docs/tasks/manage-gpus/scheduling-gpus/)**
:   Custom resources beyond CPU and memory, advertised by device plugins. Examples: `nvidia.com/gpu` (GPU count), `aliyun.com/gpu-mem` (GPU memory). Kubernetes tracks them as integer quantities on the node. The cluster autoscaler may not know about these resources if they are registered after boot.

## Autoscaler concepts

**[Cluster Autoscaler (CA)](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)**
:   The upstream Kubernetes component that provisions and removes nodes based on pending pods. Watches for unschedulable pods, simulates placement on node templates, and triggers scale-up if no existing node fits. Configurable via `--scan-interval` (default: 10 seconds).

**Scan cycle**
:   The Cluster Autoscaler's periodic evaluation loop. Default interval: 10 seconds (`--scan-interval` flag). Each cycle checks for unschedulable pods and simulates whether they fit on existing or upcoming nodes. Pods arriving in different cycles are evaluated independently, which is why the autoscaler can over-provision when demand trickles in. See the [CA FAQ](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md) for details.

**Node template**
:   The Cluster Autoscaler's static model of what resources an incoming node will have. Derived from the node group configuration. Contains only infrastructure-level resources (CPU, memory, ephemeral storage). Does NOT contain resources registered by device plugins after boot (GPU memory, accelerator counts). This is why the autoscaler over-provisions for GPU workloads: it cannot simulate resources it does not know about. See the [CA FAQ](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md).

**[Karpenter](https://karpenter.sh/docs/)**
:   Open-source node provisioner (alternative to Cluster Autoscaler). Evaluates all pending pods together and provisions best-fit nodes. Selects instance types dynamically instead of using pre-configured node groups. Still faces cross-cycle coordination problems when pods arrive after a provisioning decision.

## GPU sharing systems

**cGPU**
:   Alibaba Cloud's GPU sharing system for ACK clusters. Partitions a single physical GPU by memory, allowing multiple pods to share it. Pods declare demand via the `aliyun.com/gpu-mem` annotation (in MiB). Nodes expose capacity via the `aliyun.accelerator/gpu-memory-mib` label. These are not standard Kubernetes resources, so the cluster autoscaler cannot simulate them.

**[HAMi](https://github.com/Project-HAMi/HAMi)**
:   Heterogeneous AI Computing Virtualization Middleware. Open-source GPU and accelerator sharing for Kubernetes (CNCF Sandbox project). Supports NVIDIA, Huawei Ascend, Cambricon, Hygon, Iluvatar, and other accelerator vendors.

**[KAI Scheduler](https://github.com/NVIDIA/KAI-Scheduler)**
:   Open-source Kubernetes-native GPU scheduler from NVIDIA. Supports GPU sharing (fractional GPUs), multi-GPU allocation, and gang scheduling for AI workloads.

**`nvidia.com/gpu`**
:   Standard [extended resource](https://kubernetes.io/docs/tasks/manage-gpus/scheduling-gpus/) registered by the NVIDIA device plugin. Represents whole GPU count, or time-sliced GPU slots when time-slicing is enabled.

**`aliyun.com/gpu-mem`**
:   Alibaba cGPU pod annotation declaring GPU memory demand in MiB. Not a standard Kubernetes resource request. The cluster autoscaler cannot simulate this resource because it is not in the node template.

**`gpu-core.percentage`**
:   Alibaba cGPU pod annotation declaring what percentage of GPU compute cores a pod needs (0-100). Also not in the node template and invisible to the autoscaler.

## Scheduling and queuing tools

**[Volcano](https://volcano.sh/en/docs/)**
:   CNCF incubating project for batch scheduling on Kubernetes. Provides gang scheduling (all-or-nothing pod group placement), fair-share queuing, and job lifecycle management. Replaces or extends kube-scheduler for affected pods. Complementary to Kompakt: Volcano handles "schedule these pods together", Kompakt handles "don't trigger redundant nodes while we wait."

**[Kueue](https://kueue.sigs.k8s.io/docs/overview/)**
:   Kubernetes-native job queuing system (SIG-scheduling). Provides workload-level admission control, resource quotas, and fair-sharing across teams. Gates workloads (not individual pods) based on cluster-level resource budgets. Complementary to Kompakt: Kueue decides "does the cluster have budget?", Kompakt decides "which node should this pod wait for?"

## Cloud platforms

**ACK**
:   Alibaba Cloud Container Service for Kubernetes. Alibaba's managed Kubernetes platform.

**EKS**
:   Amazon Elastic Kubernetes Service. AWS managed Kubernetes.

**GKE**
:   Google Kubernetes Engine. Google Cloud managed Kubernetes.

**AKS**
:   Azure Kubernetes Service. Microsoft managed Kubernetes.

**NAP**
:   GKE Node Auto-Provisioning. GKE's built-in autoscaling that creates new node pools dynamically based on pending pod requirements.

**GOATScaler**
:   ACK's built-in cluster autoscaler. Emits `ProvisionNode` Kubernetes Events when scaling up, which Kompakt detects to track in-flight nodes.

## Kompakt-specific terms

**Bin-packing**
:   Scheduling algorithm that places pods onto the smallest sufficient node to minimize wasted resources. Kompakt's `WaitForWorkloadPacking` rule uses best-fit bin-packing: it finds the node with the least free capacity that still fits the pod.

**Node ledger**
:   Kompakt's internal data structure tracking real-time cluster capacity. Maintains three categories: existing nodes with their available resources, in-flight nodes detected from the autoscaler, and capacity reservations for gated pods.

**In-flight node**
:   A node that has been requested by the autoscaler but has not yet joined the cluster as Ready. Kompakt detects these via autoscaler-specific signals (CA ConfigMap, GOATScaler events, or NotReady node objects) to reserve capacity and prevent double-provisioning.

**Leader election**
:   When running multiple Kompakt replicas for high availability, one becomes the active leader via a Kubernetes Lease object. Only the leader reconciles gated pods and makes release decisions.

**Informer cache**
:   controller-runtime's in-memory copy of Kubernetes API objects (pods, nodes, PackingProfiles). The webhook reads profiles from this cache for sub-millisecond latency instead of calling the API server on every pod creation.

**Trace ID**
:   8-character identifier (`kompakt.io/trace-id` annotation) injected by the webhook on pod creation. Correlates webhook and controller log entries for a single pod across its gating lifecycle.

**MilliValue**
:   Kompakt's internal representation for resources, matching Kubernetes conventions. 1 CPU = 1000 millicores. Memory in bytes. For example, `cpu: 16000` in a `nodeGroupTemplate` means 16 cores. See [Resource Management](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/).
