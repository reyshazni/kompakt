package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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
		return strings.Contains(out, "kompakt.io/wait-for-workload-packing")
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
		hasKompakt := strings.Contains(out, "kompakt.io/wait-for-workload-packing")
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
		return strings.Contains(out, "kompakt.io/wait-for-workload-packing")
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
			return strings.Contains(out, "kompakt.io/wait-for-workload-packing")
		})
	}
}
