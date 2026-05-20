# Kompakt Wiki

Accumulated knowledge from development, research, and live cluster investigation. Not derivable from code alone.

## Cloud Providers

- [GOATScaler (ACK)](goatscaler-ack.md) -- Alibaba ACK managed autoscaler behavior, event formats, ConfigMap structure
- [Cluster Autoscaler (upstream)](cluster-autoscaler-upstream.md) -- CA status ConfigMap format, event format, scan cycle behavior

## Kubernetes Internals

- [Node Ready Condition](node-ready-condition.md) -- kubelet registration sequence, lastTransitionTime semantics, how to detect newly provisioning nodes
- [Scheduling Gates](scheduling-gates.md) -- GA timeline, interaction with autoscaler, gated pod visibility

## Design Decisions

- [Event vs Node Detection](decision-event-vs-node-detection.md) -- why event-based detection beats NotReady node detection for ACK
- [Template Matching](decision-template-matching.md) -- namePrefix vs instanceType vs label matching, why instanceType is best for ACK
