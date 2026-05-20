package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Deleting a gated pod must not crash the controller or leak state.
func TestDeleteGatedPodNoLeak(t *testing.T) {
	profile := "e2e-delete-gated"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-delete-gated-pod"

	// Create pod requesting huge resources so it stays gated
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
	waitFor(t, 10*time.Second, "pod gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	// Delete the gated pod
	if out, err := kubectl("delete", "pod", podName, "-n", "default", "--wait=false"); err != nil {
		t.Fatalf("failed to delete gated pod: %s", out)
	}

	// Verify pod is gone
	waitFor(t, 15*time.Second, "pod deletion", func() bool {
		_, err := kubectl("get", "pod", podName, "-n", "default")
		return err != nil
	})

	// Controller must still be healthy after reconciling a deleted pod
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "deployment", "kompakt-controller",
		"-o", "jsonpath={.status.availableReplicas}",
	)
	if err != nil || out != "1" {
		t.Fatalf("controller unhealthy after gated pod deletion, replicas: %q, err: %v", out, err)
	}
}

// Recreating a pod with the same name must get fresh gates with no stale state.
func TestRecreatePodAfterDeletion(t *testing.T) {
	profile := "e2e-recreate"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-recreated-pod"
	defer cleanupPod(podName, "default")
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}

	// Create, verify gated, delete
	var lastErr error
	waitFor(t, 15*time.Second, "first pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create first pod: %v", lastErr)
	}

	waitFor(t, 10*time.Second, "first pod gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	// Delete and wait for it to actually be gone
	_, _ = kubectl("delete", "pod", podName, "-n", "default", "--wait=true", "--timeout=15s")
	waitFor(t, 15*time.Second, "pod deletion confirmed", func() bool {
		_, err := kubectl("get", "pod", podName, "-n", "default")
		return err != nil
	})

	// Recreate same name
	waitFor(t, 15*time.Second, "second pod creation", func() bool {
		_, err := createHugePod(podName, "default", labels, nil)
		return err == nil
	})

	// Must be gated again with fresh gates
	waitFor(t, 10*time.Second, "second pod gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/awaiting-bin-pack")
	})
}

// After all prior tests, the controller must still be running with zero restarts.
func TestControllerHealthAfterChaos(t *testing.T) {
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "deployment", "kompakt-controller",
		"-o", "jsonpath={.status.availableReplicas}",
	)
	if err != nil {
		t.Fatalf("controller deployment not found: %s", out)
	}
	if out != "1" {
		t.Fatalf("controller not healthy, available replicas: %q", out)
	}

	// Any restart means the controller crashed during the test suite
	restarts, err := kubectl(
		"-n", "kompakt-system",
		"get", "pods", "-l", "app.kubernetes.io/name=kompakt",
		"-o", "jsonpath={.items[0].status.containerStatuses[0].restartCount}",
	)
	if err != nil {
		t.Fatalf("could not get restart count: %s", restarts)
	}
	if restarts != "0" {
		logs, _ := kubectl("-n", "kompakt-system", "logs", "-l", "app.kubernetes.io/name=kompakt", "--tail=30")
		t.Fatalf("controller restarted %s time(s) during e2e tests. Last logs:\n%s", restarts, logs)
	}
}

// Applying the same pod twice must not double-gate or crash the webhook.
func TestIdempotentPodApply(t *testing.T) {
	profile := "e2e-idempotent"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-idempotent-pod"
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
          cpu: 10m
          memory: 16Mi
`, podName, profile)

	var lastErr error
	waitFor(t, 15*time.Second, "first apply", func() bool {
		cleanupPod(podName, "default")
		_, err := kubectlApply(pod)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("first apply: %v", lastErr)
	}

	// Second apply must not cause double-gating
	_, err := kubectlApply(pod)
	if err != nil {
		// kubectl apply on an existing pod may fail for immutable fields, that's fine.
		// The point is: no webhook crash, no double-gating.
		t.Logf("second apply returned error (acceptable for immutable pod spec): %v", err)
	}

	// Verify no duplicate gates
	out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
	if err != nil {
		t.Fatalf("failed to get gates: %s", out)
	}
	gateCount := strings.Count(out, "kompakt.io/awaiting-bin-pack")
	if gateCount > 1 {
		t.Fatalf("duplicate gates detected (count=%d), gates: %s", gateCount, out)
	}
}

// Applying the same profile twice must succeed without error.
func TestIdempotentProfileApply(t *testing.T) {
	profile := "e2e-idempotent-profile"
	defer cleanupProfile(profile)

	out, err := createProfile(profile)
	if err != nil {
		t.Fatalf("first apply: %s", out)
	}

	// Second apply
	out, err = createProfile(profile)
	if err != nil {
		t.Fatalf("second apply should succeed (idempotent): %s", out)
	}

	// Verify still exists with no issues
	out, err = kubectl("get", "packingprofile", profile, "-o", "jsonpath={.metadata.name}")
	if err != nil {
		t.Fatalf("profile not found: %s", out)
	}
	if out != profile {
		t.Fatalf("unexpected profile name: %q", out)
	}
}
