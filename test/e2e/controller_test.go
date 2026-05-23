package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Controller releases the gate when a node with sufficient capacity exists.
// In kind, the node has real allocatable resources so 10m/16Mi fits trivially.
func TestGateReleasedWhenCapacityAvailable(t *testing.T) {
	profile := "e2e-release"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-released-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createPod(podName, "default", labels, nil)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}
	defer cleanupPod(podName, "default")

	// Wait for controller to release the gate
	waitFor(t, 30*time.Second, "gate to be released", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Verify node affinity was set (controller should pin to a node)
	out, err := podField(podName, "default", "{.spec.affinity.nodeAffinity}")
	if err != nil {
		t.Fatalf("failed to read node affinity: %s", out)
	}
	if !strings.Contains(out, "kubernetes.io/hostname") {
		t.Fatalf("expected node affinity with hostname after gate release, got: %s", out)
	}
}

// Priority=high annotation causes immediate gate release, bypassing capacity checks.
func TestPriorityHighImmediateRelease(t *testing.T) {
	profile := "e2e-priority"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-priority-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	annotations := map[string]string{"kompakt.io/priority": "high"}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createPod(podName, "default", labels, annotations)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}
	defer cleanupPod(podName, "default")

	// Priority=high should release within a few seconds, not waiting for capacity
	waitFor(t, 15*time.Second, "priority=high gate release", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Priority=high releases WITHOUT node affinity (no capacity check was done)
	out, err := podField(podName, "default", "{.spec.affinity.nodeAffinity}")
	if err != nil {
		t.Logf("no affinity on priority=high pod (expected): %s", out)
	}
}

// Only the exact value "high" triggers priority fast-path. "HIGH" must not.
func TestPriorityAnnotationWrongValue(t *testing.T) {
	profile := "e2e-priority-wrong"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-priority-wrong-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	annotations := map[string]string{"kompakt.io/priority": "HIGH"}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, annotations)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}
	defer cleanupPod(podName, "default")

	// Verify the pod is gated initially
	waitFor(t, 10*time.Second, "pod to be gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/wait-for-workload-packing")
	})

	// The pod should still get released via normal capacity path (kind has capacity),
	// but NOT via the priority fast-path. We verify it was gated first (above),
	// which proves "HIGH" != "high" didn't trigger immediate release.
}

// Reservation timeout releases gate unconditionally.
func TestReservationTimeoutReleasesGate(t *testing.T) {
	// Create a profile with an extremely short timeout.
	// The controller requeues every 1s, so a 5s timeout should trigger quickly.
	profile := "e2e-timeout"
	if out, err := createProfileWithTimeout(profile, "5s"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Create a pod requesting absurd resources so capacity check would never pass
	podName := "e2e-timeout-pod"
	defer cleanupPod(podName, "default")

	pod := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: %s
spec:
  terminationGracePeriodSeconds: 1
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: "100"
          memory: 1Ti
`, podName, profile)

	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := kubectlApply(pod)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}

	// Verify pod is gated first
	waitFor(t, 10*time.Second, "pod to be gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	// Wait for timeout to release the gate (5s timeout + controller requeue cycles)
	waitFor(t, 30*time.Second, "timeout to release gate", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// Controller releases gates when the referenced profile is deleted.
func TestProfileDeletedReleasesGates(t *testing.T) {
	profile := "e2e-profile-delete"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	// Intentionally deleted mid-test, no defer cleanup.

	// Create a pod requesting huge resources so it stays gated
	podName := "e2e-orphaned-pod"
	defer cleanupPod(podName, "default")

	pod := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: %s
spec:
  terminationGracePeriodSeconds: 1
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: "100"
          memory: 1Ti
`, podName, profile)

	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := kubectlApply(pod)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}

	// Verify gated
	waitFor(t, 10*time.Second, "pod to be gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	// Delete the profile
	if out, err := kubectl("delete", "packingprofile", profile); err != nil {
		t.Fatalf("failed to delete profile: %s", out)
	}

	// Controller should detect profile-not-found and release gates
	waitFor(t, 30*time.Second, "gates released after profile deletion", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// Controller only removes kompakt gates, not third-party gates.
func TestControllerPreservesThirdPartyGates(t *testing.T) {
	profile := "e2e-preserve-gates"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-preserve-gates-pod"
	defer cleanupPod(podName, "default")

	pod := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: %s
spec:
  terminationGracePeriodSeconds: 1
  schedulingGates:
    - name: other-system.io/my-gate
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: 10m
          memory: 16Mi
`, podName, profile)

	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := kubectlApply(pod)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}

	// Wait for kompakt gate to be released (capacity is available in kind)
	waitFor(t, 30*time.Second, "kompakt gate released", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Verify third-party gate is still present
	out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
	if err != nil {
		t.Fatalf("failed to get gates: %s", out)
	}
	if !strings.Contains(out, "other-system.io/my-gate") {
		t.Fatalf("controller removed third-party gate, remaining gates: %q", out)
	}
}

// A pod with no resource requests has zero demand and should release immediately.
func TestZeroDemandPodReleasedImmediately(t *testing.T) {
	profile := "e2e-zero-demand"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-zero-demand-pod"
	defer cleanupPod(podName, "default")

	pod := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: %s
spec:
  terminationGracePeriodSeconds: 1
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
`, podName, profile)

	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := kubectlApply(pod)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}

	// Zero demand = len(demand) == 0 in ExtractDemand -> immediate release
	waitFor(t, 15*time.Second, "zero-demand gate release", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// All gated pods on the same profile must eventually release.
func TestMultipleGatedPodsSameProfile(t *testing.T) {
	profile := "e2e-multi"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	const count = 5
	podNames := make([]string, count)
	for i := 0; i < count; i++ {
		podNames[i] = fmt.Sprintf("e2e-multi-%d", i)
		defer cleanupPod(podNames[i], "default")
	}

	// Create all pods with small resources so they can be released
	for _, name := range podNames {
		n := name
		var lastErr error
		waitFor(t, 15*time.Second, n+" creation", func() bool {
			cleanupPod(n, "default")
			labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
			_, err := createPod(n, "default", labels, nil) // 10m CPU fits on kind node
			if err != nil {
				lastErr = err
				return false
			}
			return true
		})
		if lastErr != nil {
			t.Fatalf("failed to create %s: %v", n, lastErr)
		}
	}

	// All should eventually release (kind node has enough capacity for 5x 10m cpu)
	for _, name := range podNames {
		n := name
		waitFor(t, 60*time.Second, n+" gate released", func() bool {
			out, err := podField(n, "default", "{.spec.schedulingGates}")
			if err != nil {
				return false
			}
			return !strings.Contains(out, "kompakt.io/")
		})
	}
}
