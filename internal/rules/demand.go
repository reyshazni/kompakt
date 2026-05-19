package rules

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
)

// ExtractDemand reads resource demand from a pod based on the profile's demandSource.
func ExtractDemand(pod *corev1.Pod, source v1alpha1.DemandSource) map[string]int64 {
	switch source.Type {
	case "ResourceRequest":
		return extractFromRequests(pod, source.Resources)
	case "Annotation":
		return extractFromAnnotation(pod, source.Annotation)
	default:
		return nil
	}
}

// extractFromRequests sums container resource requests across all containers.
func extractFromRequests(pod *corev1.Pod, resources []string) map[string]int64 {
	demand := make(map[string]int64)
	for _, container := range pod.Spec.Containers {
		for _, res := range resources {
			qty, ok := container.Resources.Requests[corev1.ResourceName(res)]
			if !ok {
				continue
			}
			demand[res] += qty.MilliValue()
		}
	}
	return demand
}

// extractFromAnnotation reads a single resource demand from a pod annotation.
func extractFromAnnotation(pod *corev1.Pod, annotation string) map[string]int64 {
	val, ok := pod.Annotations[annotation]
	if !ok {
		return nil
	}
	qty, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return nil
	}
	return map[string]int64{annotation: qty}
}
