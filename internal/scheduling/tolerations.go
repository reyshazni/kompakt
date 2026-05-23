package scheduling

import corev1 "k8s.io/api/core/v1"

// TolerateTaint checks if a taint is tolerated by any of the given tolerations.
func TolerateTaint(tolerations []corev1.Toleration, taint corev1.Taint) bool {
	for _, t := range tolerations {
		if t.Operator == corev1.TolerationOpExists && (t.Key == "" || t.Key == taint.Key) {
			if t.Effect == "" || t.Effect == taint.Effect {
				return true
			}
		}
		if t.Key == taint.Key && t.Value == taint.Value {
			if t.Effect == "" || t.Effect == taint.Effect {
				return true
			}
		}
	}
	return false
}
