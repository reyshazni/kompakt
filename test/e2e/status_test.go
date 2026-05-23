package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- Kubernetes Events ---

// Gate release emits GateReleased event visible in kubectl describe pod.
func TestEventEmittedOnGateRelease(t *testing.T) {
	profile := "e2e-event-release"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-event-pod"
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

	waitFor(t, 15*time.Second, "GateReleased event", func() bool {
		out, _ := kubectl("get", "events", "-n", "default",
			"--field-selector", fmt.Sprintf("involvedObject.name=%s,reason=GateReleased", podName),
			"-o", "jsonpath={.items[*].message}")
		return strings.Contains(out, "gate released")
	})
}

// Gate hold emits GateHeld event visible in kubectl describe pod.
func TestEventEmittedOnGateHold(t *testing.T) {
	profile := "e2e-event-hold"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-event-hold-pod"
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

	waitFor(t, 30*time.Second, "GateHeld event", func() bool {
		out, _ := kubectl("get", "events", "-n", "default",
			"--field-selector", fmt.Sprintf("involvedObject.name=%s,reason=GateHeld", podName),
			"-o", "jsonpath={.items[*].message}")
		return strings.Contains(out, "gate held by rule")
	})
}

// Priority release emits GateReleased event with reason=priority.
func TestEventEmittedOnPriorityRelease(t *testing.T) {
	profile := "e2e-event-priority"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-event-priority-pod"
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

	waitFor(t, 15*time.Second, "priority GateReleased event", func() bool {
		out, _ := kubectl("get", "events", "-n", "default",
			"--field-selector", fmt.Sprintf("involvedObject.name=%s,reason=GateReleased", podName),
			"-o", "jsonpath={.items[*].message}")
		return strings.Contains(out, "reason=priority")
	})
}

// Timeout release emits GateReleased event with reason=timeout.
func TestEventEmittedOnTimeoutRelease(t *testing.T) {
	profile := "e2e-event-timeout"
	if out, err := createProfileWithTimeout(profile, "5s"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-event-timeout-pod"
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

	waitFor(t, 30*time.Second, "timeout GateReleased event", func() bool {
		out, _ := kubectl("get", "events", "-n", "default",
			"--field-selector", fmt.Sprintf("involvedObject.name=%s,reason=GateReleased", podName),
			"-o", "jsonpath={.items[*].message}")
		return strings.Contains(out, "reason=timeout")
	})
}

// --- Pod Annotations ---

// Released pod has gate-reason and target-node annotations.
func TestAnnotationSetOnRelease(t *testing.T) {
	profile := "e2e-annotation-release"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-annotated-pod"
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

	reason, err := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/gate-reason}")
	if err != nil {
		t.Fatalf("failed to read gate-reason: %s", reason)
	}
	if reason != "capacity" {
		t.Fatalf("expected gate-reason=capacity, got %q", reason)
	}

	node, err := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/target-node}")
	if err != nil {
		t.Fatalf("failed to read target-node: %s", node)
	}
	if node == "" {
		t.Fatal("expected target-node annotation after capacity release")
	}
}

// Held pod has held-by annotation showing which rule.
func TestAnnotationSetOnHold(t *testing.T) {
	profile := "e2e-annotation-hold"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-held-annotated-pod"
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

	waitFor(t, 30*time.Second, "held-by annotation set", func() bool {
		out, err := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/held-by}")
		if err != nil {
			return false
		}
		return out == "WaitForWorkloadPacking"
	})
}

// After release, held-by annotation should be removed.
func TestAnnotationHeldByRemovedOnRelease(t *testing.T) {
	profile := "e2e-annotation-cleanup"
	if out, err := createProfileWithTimeout(profile, "5s"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-annotation-cleanup-pod"
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

	// Wait for hold annotation to appear first
	waitFor(t, 15*time.Second, "held-by annotation", func() bool {
		out, _ := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/held-by}")
		return out != ""
	})

	// Wait for timeout release
	waitFor(t, 30*time.Second, "timeout release", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// held-by should be gone, gate-reason should be set
	heldBy, _ := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/held-by}")
	if heldBy != "" {
		t.Fatalf("held-by annotation should be removed after release, got %q", heldBy)
	}

	reason, _ := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/gate-reason}")
	if reason != "timeout" {
		t.Fatalf("expected gate-reason=timeout, got %q", reason)
	}
}

// Trace ID annotation set by webhook on every gated pod.
func TestTraceIDAnnotation(t *testing.T) {
	profile := "e2e-traceid"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-traceid-pod"
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

	traceID, err := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/trace-id}")
	if err != nil {
		t.Fatalf("failed to read trace-id: %s", traceID)
	}
	if len(traceID) != 8 {
		t.Fatalf("expected 8-char trace ID, got %q (len=%d)", traceID, len(traceID))
	}
}

// --- PackingProfile Status Conditions ---

// Valid profile shows Ready=True, ProfileValid=True, LedgerReady=True.
func TestProfileStatusConditions_AllTrue(t *testing.T) {
	profile := "e2e-status-conditions"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-status-pod"
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

	waitFor(t, 30*time.Second, "Ready=True", func() bool {
		out, _ := kubectl("get", "packingprofile", profile,
			"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
		return out == "True"
	})

	conditions := []struct {
		condType string
		expected string
	}{
		{"ProfileValid", "True"},
		{"LedgerReady", "True"},
	}
	for _, c := range conditions {
		out, _ := kubectl("get", "packingprofile", profile,
			"-o", fmt.Sprintf("jsonpath={.status.conditions[?(@.type==\"%s\")].status}", c.condType))
		if out != c.expected {
			t.Fatalf("expected %s=%s, got %q", c.condType, c.expected, out)
		}
	}
}

// WaitForNodeReady without nodeGroupTemplates shows ProfileValid=False.
func TestProfileStatusConditions_MisconfiguredProfile(t *testing.T) {
	profile := "e2e-status-misconfig"
	if out, err := createScaleUpProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Create pod to trigger reconciliation
	podName := "e2e-status-misconfig-pod"
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

	// WaitForNodeReady without nodeGroupTemplates = ProfileValid=False
	waitFor(t, 30*time.Second, "ProfileValid=False", func() bool {
		out, _ := kubectl("get", "packingprofile", profile,
			"-o", "jsonpath={.status.conditions[?(@.type==\"ProfileValid\")].status}")
		return out == "False"
	})

	// Ready should also be False
	out, _ := kubectl("get", "packingprofile", profile,
		"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
	if out != "False" {
		t.Fatalf("expected Ready=False when ProfileValid=False, got %q", out)
	}

	// Reason should be ConfigurationError
	reason, _ := kubectl("get", "packingprofile", profile,
		"-o", "jsonpath={.status.conditions[?(@.type==\"ProfileValid\")].reason}")
	if reason != "ConfigurationError" {
		t.Fatalf("expected reason=ConfigurationError, got %q", reason)
	}
}

// --- kubectl get columns ---

// kubectl get packingprofiles shows READY column with correct value.
func TestKubectlGetShowsReadyColumn(t *testing.T) {
	profile := "e2e-kubectl-ready"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-kubectl-ready-pod"
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

	waitFor(t, 30*time.Second, "READY column shows True", func() bool {
		out, err := kubectl("get", "packingprofile", profile)
		if err != nil {
			return false
		}
		return strings.Contains(out, "READY") && strings.Contains(out, "True")
	})
}

// kubectl get shows GATES and INFLIGHT columns.
func TestKubectlGetShowsGatesAndInflight(t *testing.T) {
	profile := "e2e-kubectl-columns"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-kubectl-col-pod"
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

	// Wait for status to be populated
	waitFor(t, 30*time.Second, "status columns populated", func() bool {
		out, err := kubectl("get", "packingprofile", profile)
		if err != nil {
			return false
		}
		return strings.Contains(out, "GATES") && strings.Contains(out, "INFLIGHT")
	})
}
