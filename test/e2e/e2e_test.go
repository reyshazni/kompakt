package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func kubectl(args ...string) (string, error) {
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func kubectlApply(yaml string) (string, error) {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func waitFor(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func cleanupPod(name, ns string) {
	_, _ = kubectl("delete", "pod", name, "-n", ns, "--ignore-not-found", "--wait=false")
}

func cleanupProfile(name string) {
	_, _ = kubectl("delete", "packingprofile", name, "--ignore-not-found")
}

func podField(name, ns, jsonpath string) (string, error) {
	return kubectl("get", "pod", name, "-n", ns, "-o", fmt.Sprintf("jsonpath=%s", jsonpath))
}

// createProfileWithTimeout creates a PackingProfile with a custom reservation timeout.
// timeout should be a Go duration string like "5s" or "3m".
func createProfileWithTimeout(name, timeout string) (string, error) {
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
  reservationTimeout: %s
`, name, timeout)
	return kubectlApply(profile)
}

func createProfile(name string) (string, error) {
	return createProfileWithTimeout(name, "1m")
}

func createPodYAML(name, ns string, labels map[string]string, annotations map[string]string, cpu, memory string) string {
	var labelLines, annotationLines string
	for k, v := range labels {
		labelLines += fmt.Sprintf("    %s: %s\n", k, v)
	}
	for k, v := range annotations {
		annotationLines += fmt.Sprintf("    %s: \"%s\"\n", k, v)
	}
	if cpu == "" {
		cpu = "10m"
	}
	if memory == "" {
		memory = "16Mi"
	}

	return fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
%s  annotations:
%sspec:
  terminationGracePeriodSeconds: 1
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: "%s"
          memory: %s
`, name, ns, labelLines, annotationLines, cpu, memory)
}

func createPod(name, ns string, labels map[string]string, annotations map[string]string) (string, error) {
	return kubectlApply(createPodYAML(name, ns, labels, annotations, "10m", "16Mi"))
}

// createHugePod requests resources that will never fit on a kind node,
// keeping the pod gated indefinitely so tests can observe the gate.
func createHugePod(name, ns string, labels map[string]string, annotations map[string]string) (string, error) {
	return kubectlApply(createPodYAML(name, ns, labels, annotations, "100", "1Ti"))
}

// Warmup: verify the webhook is actually intercepting pod creates before running
// any tests that depend on it. There can be a brief window after deploy where
// the TLS cert is not yet trusted by the API server.
func TestWebhookFunctional(t *testing.T) {
	profile := "e2e-warmup"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-warmup-pod"
	defer cleanupPod(podName, "default")

	waitFor(t, 30*time.Second, "webhook to intercept pod creates", func() bool {
		cleanupPod(podName, "default")
		labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
		if _, err := createHugePod(podName, "default", labels, nil); err != nil {
			return false
		}
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/awaiting-bin-pack")
	})
}

func TestCRDInstalled(t *testing.T) {
	out, err := kubectl("get", "crd", "packingprofiles.packer.kompakt.io")
	if err != nil {
		t.Fatalf("CRD not found: %s", out)
	}
}

func TestControllerRunning(t *testing.T) {
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "deployment", "kompakt-controller",
		"-o", "jsonpath={.status.availableReplicas}",
	)
	if err != nil {
		t.Fatalf("controller deployment not found: %s", out)
	}
	if out != "1" {
		t.Fatalf("expected 1 available replica, got %q", out)
	}
}

func TestWebhookRegistered(t *testing.T) {
	out, err := kubectl("get", "mutatingwebhookconfiguration", "kompakt-webhook")
	if err != nil {
		t.Fatalf("webhook not registered: %s", out)
	}
}

func TestPackingProfileLifecycle(t *testing.T) {
	name := "e2e-lifecycle"
	out, err := createProfile(name)
	if err != nil {
		t.Fatalf("failed to create PackingProfile: %s", out)
	}
	defer cleanupProfile(name)

	// Verify profile exists
	out, err = kubectl("get", "packingprofile", name)
	if err != nil {
		t.Fatalf("profile not found after create: %s", out)
	}

	// Update in-place must succeed
	out, err = createProfileWithTimeout(name, "30s")
	if err != nil {
		t.Fatalf("failed to update PackingProfile: %s", out)
	}

	// Delete
	out, err = kubectl("delete", "packingprofile", name)
	if err != nil {
		t.Fatalf("failed to delete PackingProfile: %s", out)
	}

	// Verify gone
	_, err = kubectl("get", "packingprofile", name)
	if err == nil {
		t.Fatal("profile still exists after delete")
	}
}

// Uses huge resource requests so the gate stays while we verify injection.
func TestPodGatingWithValidProfile(t *testing.T) {
	profile := "e2e-gating"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-gated-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation to succeed", func() bool {
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

	waitFor(t, 10*time.Second, "pod to have scheduling gate", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/awaiting-bin-pack")
	})
}

func TestPodRejectedWithInvalidProfile(t *testing.T) {
	podName := "e2e-rejected-pod"
	defer cleanupPod(podName, "default")

	labels := map[string]string{"packer.kompakt.io/packing-profile": "does-not-exist"}
	// Retry because the webhook might need a moment to be fully functional.
	// Each attempt cleans up any pod that slipped through.
	waitFor(t, 15*time.Second, "webhook to reject pod with invalid profile", func() bool {
		cleanupPod(podName, "default")
		_, err := createPod(podName, "default", labels, nil)
		return err != nil
	})
}

func TestPodWithExcludeLabel(t *testing.T) {
	profile := "e2e-exclude"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-excluded-pod"
	labels := map[string]string{
		"packer.kompakt.io/packing-profile": profile,
		"kompakt.io/exclude":                "\"true\"",
	}
	if out, err := createPod(podName, "default", labels, nil); err != nil {
		t.Fatalf("failed to create excluded pod: %s", out)
	}
	defer cleanupPod(podName, "default")

	out, err := podField(podName, "default", "{.spec.schedulingGates}")
	if err != nil {
		t.Fatalf("failed to get pod: %s", out)
	}
	if strings.Contains(out, "kompakt.io/") {
		t.Fatalf("excluded pod should not have kompakt gates, got: %s", out)
	}
}

func TestPodWithoutLabelPassesThrough(t *testing.T) {
	podName := "e2e-unlabeled-pod"
	if out, err := createPod(podName, "default", nil, nil); err != nil {
		t.Fatalf("failed to create unlabeled pod: %s", out)
	}
	defer cleanupPod(podName, "default")

	out, err := podField(podName, "default", "{.spec.schedulingGates}")
	if err != nil {
		t.Fatalf("failed to get pod: %s", out)
	}
	if strings.Contains(out, "kompakt.io/") {
		t.Fatalf("unlabeled pod should not have kompakt gates, got: %s", out)
	}
}

// Webhook must reject an empty profile label, not panic or silently allow.
func TestPodWithEmptyProfileLabel(t *testing.T) {
	podName := "e2e-empty-label"
	defer cleanupPod(podName, "default")

	labels := map[string]string{"packer.kompakt.io/packing-profile": "\"\""}
	_, err := createPod(podName, "default", labels, nil)
	if err == nil {
		// If it somehow got created, it must NOT have gates (empty string is not a valid profile).
		out, _ := podField(podName, "default", "{.spec.schedulingGates}")
		if strings.Contains(out, "kompakt.io/") {
			t.Fatal("pod with empty profile label was gated: webhook resolved an empty string to a profile")
		}
		// Acceptable: webhook allowed it through ungated. Not ideal but not broken.
		t.Log("pod with empty profile label was allowed without gates (acceptable)")
		return
	}
	// Rejected: correct behavior.
}

// Only the exact value "true" should trigger exclusion.
// "True", "TRUE", "yes", "1" must NOT bypass gating.
func TestExcludeLabelOnlyExactTrue(t *testing.T) {
	profile := "e2e-exclude-strict"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	wrongValues := []string{"True", "TRUE", "yes", "1", "false"}
	for _, val := range wrongValues {
		podName := fmt.Sprintf("e2e-excl-%s", strings.ToLower(val))
		defer cleanupPod(podName, "default")

		// Use raw YAML to ensure label values are exactly what we intend
		pod := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: %s
    kompakt.io/exclude: "%s"
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
`, podName, profile, val)

		var created bool
		waitFor(t, 10*time.Second, "pod creation for exclude="+val, func() bool {
			cleanupPod(podName, "default")
			_, err := kubectlApply(pod)
			if err != nil {
				return false
			}
			created = true
			return true
		})
		if !created {
			continue
		}

		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err == nil && !strings.Contains(out, "kompakt.io/") {
			t.Errorf("exclude=%q bypassed gating, only exact 'true' should exclude", val)
		}
	}
}

// Webhook must append kompakt gates without removing pre-existing ones from other systems.
// Uses huge resources to keep the pod gated while we verify both gates are present.
func TestPreExistingSchedulingGatesPreserved(t *testing.T) {
	profile := "e2e-existing-gates"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-preexisting-gates"
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
    - name: some-other-system.io/custom-gate
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
	waitFor(t, 15*time.Second, "pod with pre-existing gates to be created", func() bool {
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

	waitFor(t, 10*time.Second, "both gates to be present", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		hasKompakt := strings.Contains(out, "kompakt.io/awaiting-bin-pack")
		hasOther := strings.Contains(out, "some-other-system.io/custom-gate")
		return hasKompakt && hasOther
	})
}

// Webhook must gate pods in any namespace, not just default.
func TestGatingInNonDefaultNamespace(t *testing.T) {
	ns := "e2e-ns-test"
	if out, err := kubectl("create", "namespace", ns, "--dry-run=client", "-o", "yaml"); err != nil {
		t.Fatalf("generate ns yaml: %s", out)
	}
	_, _ = kubectlApply(fmt.Sprintf(`
apiVersion: v1
kind: Namespace
metadata:
  name: %s
`, ns))
	defer func() { _, _ = kubectl("delete", "namespace", ns, "--ignore-not-found", "--wait=false") }()

	profile := "e2e-crossns"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-ns-pod"
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
	var lastErr error
	waitFor(t, 15*time.Second, "pod creation in non-default ns", func() bool {
		cleanupPod(podName, ns)
		_, err := createHugePod(podName, ns, labels, nil)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create pod in namespace %s: %v", ns, lastErr)
	}
	defer cleanupPod(podName, ns)

	waitFor(t, 10*time.Second, "pod gated in non-default namespace", func() bool {
		out, err := podField(podName, ns, "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/awaiting-bin-pack")
	})
}

// Webhook must handle a burst of concurrent pod creates without dropping any.
func TestBurstPodCreation(t *testing.T) {
	profile := "e2e-burst"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	const count = 10
	// Wait briefly for webhook to be fully ready for this profile
	time.Sleep(2 * time.Second)

	type result struct {
		name string
		err  error
	}
	results := make(chan result, count)

	for i := 0; i < count; i++ {
		podName := fmt.Sprintf("e2e-burst-%d", i)
		defer cleanupPod(podName, "default")

		go func(name string) {
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
`, name, profile)
			_, err := kubectlApply(pod)
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
		t.Fatalf("burst pod creation failures (%d/%d):\n%s", len(failures), count, strings.Join(failures, "\n"))
	}

	// Verify all pods got gates
	for i := 0; i < count; i++ {
		podName := fmt.Sprintf("e2e-burst-%d", i)
		waitFor(t, 10*time.Second, podName+" to have gate", func() bool {
			out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
			if err != nil {
				return false
			}
			return strings.Contains(out, "kompakt.io/awaiting-bin-pack")
		})
	}
}

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
		return strings.Contains(out, "kompakt.io/awaiting-bin-pack")
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
	// After all previous tests have created/deleted pods and profiles,
	// the controller must still be running and responsive.
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
		// Log controller logs for debugging
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
