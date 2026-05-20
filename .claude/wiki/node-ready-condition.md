# Node Ready Condition

How Kubernetes tracks node readiness, and how to distinguish new nodes from crashed nodes.

## Registration sequence

1. API server creates Node object, sets `creationTimestamp`
2. Kubelet sends first status update, sets `Ready=False` with `lastTransitionTime` = current time
3. Node initializes: CNI, device plugins, etc.
4. Kubelet updates `Ready=True`, updates `lastTransitionTime`

Delta between `creationTimestamp` and first `lastTransitionTime` is typically < 5 seconds.

## Detecting "never been Ready"

A node is newly provisioning if:
- `Ready` condition status is NOT `True`
- `lastTransitionTime` of Ready condition is within ~10 seconds of `creationTimestamp`

This means the node was born NotReady and has stayed NotReady. Never transitioned to True and back.

A crashed node has:
- `Ready` condition was `True`, then transitioned to `False` or `Unknown`
- `lastTransitionTime` is much later than `creationTimestamp` (hours/days)

## Why 10 seconds threshold (not exact equality)

`creationTimestamp` is set by API server. `lastTransitionTime` is set by kubelet on first status report. Different processes, small network delay. 10s is safe margin.

## Edge case: kubelet restart

Kubernetes issue #124397: after kubelet restart, node briefly shows NotReady. But:
- `creationTimestamp` is old (node existed before)
- `lastTransitionTime` is recent (the restart)
- Delta is large, so the "never been Ready" check correctly ignores it

## When allocatable is populated

Kubelet often reports `node.Status.Allocatable` for CPU/memory before the node reaches Ready. This is because kubelet can calculate allocatable from instance resources immediately. However, extended resources from device plugins (e.g., `nvidia.com/gpu`, `aliyun.com/gpu-mem`) are only populated after the device plugin registers, which typically happens around or after Ready.

## Source

- [kubernetes/pkg/kubelet/nodestatus/setters.go](https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/nodestatus/setters.go)
- [kubelet restart issue #124397](https://github.com/kubernetes/kubernetes/issues/124397)
- [Node Status (official docs)](https://kubernetes.io/docs/reference/node/node-status/)
