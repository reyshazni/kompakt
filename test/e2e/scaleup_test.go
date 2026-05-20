package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func createScaleUpProfile(name string) (string, error) {
	return createScaleUpProfileWithTimeout(name, "1m")
}

func createScaleUpProfileWithTimeout(name, timeout string) (string, error) {
	profile := fmt.Sprintf(`
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: %s
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
    - name: WaitForScaleUp
  reservationTimeout: %s
`, name, timeout)
	return kubectlApply(profile)
}

func createBothRulesProfile(name, timeout string) (string, error) {
	profile := fmt.Sprintf(`
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: %s
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
    - name: WaitForScaleUp
  reservationTimeout: %s
`, name, timeout)
	return kubectlApply(profile)
}

// --- WaitForScaleUp gate injection ---

// Webhook injects the correct gate name for WaitForScaleUp.
// We verify via the gate-reason annotation because WaitForScaleUp passthrough
// releases the gate almost instantly in kind (no inflight nodes).
func TestWaitForScaleUp_GateName(t *testing.T) {
	// Use BothRules profile so we can observe the awaiting-scale-up gate
	// (BinPack holds huge pods, giving us time to inspect gates).
	profile := "e2e-scaleup-gate"
	if out, err := createBothRulesProfile(profile, "5m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-scaleup-gate-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
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

	// Both gates should be present since BinPack holds
	waitFor(t, 10*time.Second, "awaiting-scale-up gate injected", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/awaiting-scale-up")
	})
}

// --- WaitForScaleUp release behavior ---

// WaitForScaleUp releases with node affinity when existing node has capacity.
func TestWaitForScaleUp_ExistingCapacity_ReleasedWithAffinity(t *testing.T) {
	profile := "e2e-scaleup-existing"
	if out, err := createScaleUpProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-scaleup-released"
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

	waitFor(t, 30*time.Second, "gate released", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	out, err := podField(podName, "default", "{.spec.affinity.nodeAffinity}")
	if err != nil {
		t.Fatalf("failed to read node affinity: %s", out)
	}
	if !strings.Contains(out, "kubernetes.io/hostname") {
		t.Fatalf("expected node affinity after WaitForScaleUp release, got: %s", out)
	}
}

// WaitForScaleUp with no capacity anywhere should still release (passthrough).
// In kind, nodes always exist, so "no capacity" = huge resource request.
// The passthrough path fires when no node (existing or inflight) fits.
// Since WaitForScaleUp has no inflight data in kind, huge pods passthrough.
func TestWaitForScaleUp_NoCapacity_Passthrough(t *testing.T) {
	profile := "e2e-scaleup-passthrough"
	if out, err := createScaleUpProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-scaleup-passthrough-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
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

	// WaitForScaleUp passthrough: no capacity anywhere, should release
	// immediately (so autoscaler can see it). No node affinity.
	waitFor(t, 30*time.Second, "passthrough release", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Passthrough should NOT set node affinity
	out, _ := podField(podName, "default", "{.spec.affinity.nodeAffinity}")
	if strings.Contains(out, "kubernetes.io/hostname") {
		t.Fatal("passthrough release should not inject node affinity")
	}
}

// WaitForScaleUp timeout releases gate for pods that can never fit.
func TestWaitForScaleUp_Timeout(t *testing.T) {
	profile := "e2e-scaleup-timeout"
	// Very short timeout so we don't wait long in e2e
	if out, err := createScaleUpProfileWithTimeout(profile, "5s"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-scaleup-timeout-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
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

	waitFor(t, 30*time.Second, "timeout release", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// --- Both rules together ---

// Both rules together inject both gates.
func TestBothRules_BothGatesInjected(t *testing.T) {
	profile := "e2e-both-rules"
	if out, err := createBothRulesProfile(profile, "1m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-both-gates-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
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

	waitFor(t, 10*time.Second, "both gates injected", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		hasBinPack := strings.Contains(out, "kompakt.io/awaiting-bin-pack")
		hasScaleUp := strings.Contains(out, "kompakt.io/awaiting-scale-up")
		return hasBinPack && hasScaleUp
	})
}

// Both rules: small pod should be released (BinPack finds existing capacity).
func TestBothRules_SmallPod_Released(t *testing.T) {
	profile := "e2e-both-small"
	if out, err := createBothRulesProfile(profile, "1m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-both-small-pod"
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

	waitFor(t, 30*time.Second, "gate released", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Should have node affinity from BinPack
	out, err := podField(podName, "default", "{.spec.affinity.nodeAffinity}")
	if err != nil {
		t.Fatalf("failed to read affinity: %s", out)
	}
	if !strings.Contains(out, "kubernetes.io/hostname") {
		t.Fatalf("expected node affinity, got: %s", out)
	}
}

// Both rules: priority=high overrides both rules.
func TestBothRules_PriorityHigh_Overrides(t *testing.T) {
	profile := "e2e-both-priority"
	if out, err := createBothRulesProfile(profile, "5m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-both-priority-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	annotations := map[string]string{"kompakt.io/priority": "high"}
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

	waitFor(t, 15*time.Second, "priority release", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// --- Chaos: profile changes while pods are gated ---

// Switching a profile's rules while pods are gated should not crash.
func TestWaitForScaleUp_ProfileUpdateWhileGated(t *testing.T) {
	// Start with BinPack (holds huge pods), then switch to short timeout.
	profile := "e2e-scaleup-update"
	if out, err := createProfileWithTimeout(profile, "5m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-scaleup-update-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
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

	waitFor(t, 10*time.Second, "pod gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	// Update profile: switch to short timeout
	if out, err := createProfileWithTimeout(profile, "5s"); err != nil {
		t.Fatalf("update profile: %s", out)
	}

	// Pod should eventually be released (timeout kicks in)
	waitFor(t, 30*time.Second, "gate released after profile update", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Controller must still be healthy
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "deployment", "kompakt-controller",
		"-o", "jsonpath={.status.availableReplicas}",
	)
	if err != nil || out != "1" {
		t.Fatalf("controller unhealthy after profile update, replicas: %q", out)
	}
}

// Deleting a profile releases all gated pods (profile not found path).
func TestWaitForScaleUp_ProfileDeleteReleasesGates(t *testing.T) {
	// Use BinPack to hold huge pods, then delete the profile.
	profile := "e2e-scaleup-delete"
	if out, err := createProfileWithTimeout(profile, "5m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}

	podName := "e2e-scaleup-delete-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
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

	waitFor(t, 10*time.Second, "pod gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	if out, err := kubectl("delete", "packingprofile", profile); err != nil {
		t.Fatalf("failed to delete profile: %s", out)
	}

	waitFor(t, 30*time.Second, "gates released after profile deletion", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// --- Chaos: burst of pods with WaitForScaleUp ---

// Burst create multiple pods with WaitForScaleUp; all should be processed.
func TestWaitForScaleUp_BurstCreation(t *testing.T) {
	profile := "e2e-scaleup-burst"
	if out, err := createScaleUpProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	time.Sleep(2 * time.Second)

	const count = 10
	type result struct {
		name string
		err  error
	}
	results := make(chan result, count)

	for i := 0; i < count; i++ {
		podName := fmt.Sprintf("e2e-scaleup-burst-%d", i)
		defer cleanupPod(podName, "default")

		go func(name string) {
			labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
			_, err := createPod(name, "default", labels, nil)
			results <- result{name: name, err: err}
		}(podName)
	}

	var failures []string
	for i := 0; i < count; i++ {
		r := <-results
		if r.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", r.name, r.err))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("burst failures (%d/%d):\n%s", len(failures), count, strings.Join(failures, "\n"))
	}

	// All should eventually be released (kind has capacity)
	for i := 0; i < count; i++ {
		podName := fmt.Sprintf("e2e-scaleup-burst-%d", i)
		waitFor(t, 60*time.Second, podName+" released", func() bool {
			out, err := podField(podName, "default", "{.spec.schedulingGates}")
			if err != nil {
				return false
			}
			return !strings.Contains(out, "kompakt.io/")
		})
	}
}

// --- Chaos: controller restart with gated WaitForScaleUp pods ---

// Controller restart should pick up existing gated pods and continue processing.
func TestWaitForScaleUp_ControllerRestartRecovery(t *testing.T) {
	// Use BinPack to hold huge pods, then kill controller, then shorten timeout.
	profile := "e2e-scaleup-restart"
	if out, err := createProfileWithTimeout(profile, "5m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	for i := 0; i < 3; i++ {
		podName := fmt.Sprintf("e2e-scaleup-restart-%d", i)
		defer cleanupPod(podName, "default")
		labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
		var lastErr error
		waitFor(t, 15*time.Second, podName+" creation", func() bool {
			cleanupPod(podName, "default")
			_, err := createHugePod(podName, "default", labels, nil)
			if err != nil {
				lastErr = err
				return false
			}
			return true
		})
		if lastErr != nil {
			t.Fatalf("failed to create %s: %v", podName, lastErr)
		}
	}

	for i := 0; i < 3; i++ {
		podName := fmt.Sprintf("e2e-scaleup-restart-%d", i)
		waitFor(t, 10*time.Second, podName+" gated", func() bool {
			out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
			if err != nil {
				return false
			}
			return strings.Contains(out, "kompakt.io/")
		})
	}

	// Kill all controller pods
	pods := controllerPodNames(t)
	for _, p := range pods {
		t.Logf("killing controller pod: %s", p)
		_, _ = kubectl("-n", "kompakt-system", "delete", "pod", p, "--wait=false")
	}

	waitFor(t, 90*time.Second, "controller restarted", func() bool {
		out, err := kubectl(
			"-n", "kompakt-system",
			"get", "deployment", "kompakt-controller",
			"-o", "jsonpath={.status.availableReplicas}",
		)
		if err != nil {
			return false
		}
		return out == "1"
	})

	// Shorten timeout so pods get released by the restarted controller
	if out, err := createProfileWithTimeout(profile, "3s"); err != nil {
		t.Fatalf("update profile timeout: %s", out)
	}

	for i := 0; i < 3; i++ {
		podName := fmt.Sprintf("e2e-scaleup-restart-%d", i)
		waitFor(t, 60*time.Second, podName+" released after restart", func() bool {
			out, err := podField(podName, "default", "{.spec.schedulingGates}")
			if err != nil {
				return false
			}
			return !strings.Contains(out, "kompakt.io/")
		})
	}
}

// --- Chaos: namespace deletion with gated pods ---

// Deleting a namespace with gated pods should not crash the controller.
func TestWaitForScaleUp_NamespaceDeletion(t *testing.T) {
	ns := "e2e-scaleup-ns-delete"
	_, _ = kubectlApply(fmt.Sprintf(`
apiVersion: v1
kind: Namespace
metadata:
  name: %s
`, ns))

	// Use BinPack to hold huge pods
	profile := "e2e-scaleup-nsdelete"
	if out, err := createProfileWithTimeout(profile, "5m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-scaleup-nsdelete-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation", func() bool {
		cleanupPod(podName, ns)
		_, err := createHugePod(podName, ns, labels, nil)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod: %v", lastErr)
	}

	waitFor(t, 10*time.Second, "pod gated", func() bool {
		out, err := podField(podName, ns, "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	// Delete the entire namespace
	_, _ = kubectl("delete", "namespace", ns, "--wait=false")

	// Wait for namespace to be gone
	waitFor(t, 60*time.Second, "namespace deleted", func() bool {
		_, err := kubectl("get", "namespace", ns)
		return err != nil
	})

	// Controller must still be healthy
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "deployment", "kompakt-controller",
		"-o", "jsonpath={.status.availableReplicas}",
	)
	if err != nil || out != "1" {
		t.Fatalf("controller unhealthy after namespace deletion, replicas: %q", out)
	}

	// Verify no restarts
	restarts, _ := kubectl(
		"-n", "kompakt-system",
		"get", "pods", "-l", "app.kubernetes.io/name=kompakt",
		"-o", "jsonpath={.items[0].status.containerStatuses[0].restartCount}",
	)
	if restarts != "0" {
		logs, _ := kubectl("-n", "kompakt-system", "logs", "-l", "app.kubernetes.io/name=kompakt", "--tail=20")
		t.Fatalf("controller restarted %s time(s) after namespace deletion.\n%s", restarts, logs)
	}
}
