package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestLeaderElection_SingleLeaderWithMultipleReplicas scales to 2 replicas and
// verifies exactly one holds the leader lease.
func TestLeaderElection_SingleLeaderWithMultipleReplicas(t *testing.T) {
	scaleController(t, 2)
	defer scaleController(t, 1)

	// Wait for lease to be acquired
	waitFor(t, 30*time.Second, "leader lease to be acquired", func() bool {
		out, _ := kubectl(
			"-n", "kompakt-system",
			"get", "lease", "kompakt.leader.election",
			"-o", "jsonpath={.spec.holderIdentity}",
		)
		return out != ""
	})

	leader := leaderPodName(t)
	pods := controllerPodNames(t)

	if len(pods) < 2 {
		t.Fatalf("expected at least 2 controller pods, got %d", len(pods))
	}

	// Leader must be one of the running pods
	found := false
	for _, p := range pods {
		if p == leader {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("leader %q is not among running pods %v", leader, pods)
	}

	t.Logf("leader=%s, all pods=%v", leader, pods)
}

// TestLeaderElection_WebhookWorksDuringHA verifies the webhook serves requests
// from any replica regardless of leader status.
func TestLeaderElection_WebhookWorksDuringHA(t *testing.T) {
	scaleController(t, 2)
	defer scaleController(t, 1)

	profile := "e2e-ha-webhook"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Rapidly create multiple pods; webhook must respond on any replica
	for i := 0; i < 5; i++ {
		podName := fmt.Sprintf("e2e-ha-webhook-%d", i)
		defer cleanupPod(podName, "default")

		labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
		var lastErr error
		waitFor(t, 10*time.Second, podName+" creation", func() bool {
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

	// All pods must be gated
	for i := 0; i < 5; i++ {
		podName := fmt.Sprintf("e2e-ha-webhook-%d", i)
		waitFor(t, 10*time.Second, podName+" gated", func() bool {
			out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
			if err != nil {
				return false
			}
			return strings.Contains(out, "kompakt.io/wait-for-workload-packing")
		})
	}
}

// TestLeaderElection_FailoverReleasesGates kills the leader pod and verifies
// the standby takes over and continues releasing gates.
func TestLeaderElection_FailoverReleasesGates(t *testing.T) {
	scaleController(t, 2)
	defer scaleController(t, 1)

	profile := "e2e-ha-failover"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Create a pod with small resources (should be released by controller)
	podName := "e2e-ha-failover-pod"
	defer cleanupPod(podName, "default")
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}

	// First verify controller works: create + release
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

	waitFor(t, 30*time.Second, "gate released before failover", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Now kill the leader
	leader := leaderPodName(t)
	t.Logf("killing leader pod: %s", leader)
	if out, err := kubectl("-n", "kompakt-system", "delete", "pod", leader, "--wait=false"); err != nil {
		t.Fatalf("failed to delete leader pod: %s", out)
	}

	// Wait for new leader to appear (different from the killed one)
	waitFor(t, 60*time.Second, "new leader elected", func() bool {
		out, _ := kubectl(
			"-n", "kompakt-system",
			"get", "lease", "kompakt.leader.election",
			"-o", "jsonpath={.spec.holderIdentity}",
		)
		if out == "" {
			return false
		}
		newLeader := strings.SplitN(out, "_", 2)[0]
		return newLeader != leader
	})

	newLeader := leaderPodName(t)
	t.Logf("new leader: %s (was: %s)", newLeader, leader)

	// Create a new pod after failover; the new leader must reconcile it
	podName2 := "e2e-ha-failover-pod2"
	defer cleanupPod(podName2, "default")

	waitFor(t, 15*time.Second, "post-failover pod creation", func() bool {
		cleanupPod(podName2, "default")
		_, err := createPod(podName2, "default", labels, nil)
		return err == nil
	})

	// New leader must release the gate
	waitFor(t, 30*time.Second, "gate released after failover", func() bool {
		out, err := podField(podName2, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// TestLeaderElection_GatedPodSurvivesFailover creates a gated pod with huge
// resources (stays gated), kills the leader, and verifies the pod is still
// gated and tracked by the new leader (no lost state).
func TestLeaderElection_GatedPodSurvivesFailover(t *testing.T) {
	scaleController(t, 2)
	defer scaleController(t, 1)

	profile := "e2e-ha-survive"
	if out, err := createProfileWithTimeout(profile, "5m"); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	podName := "e2e-ha-survive-pod"
	defer cleanupPod(podName, "default")
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

	// Verify gated
	waitFor(t, 10*time.Second, "pod gated", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/wait-for-workload-packing")
	})

	// Kill leader
	leader := leaderPodName(t)
	t.Logf("killing leader: %s", leader)
	_, _ = kubectl("-n", "kompakt-system", "delete", "pod", leader, "--wait=false")

	// Wait for new leader
	waitFor(t, 60*time.Second, "new leader after failover", func() bool {
		out, _ := kubectl(
			"-n", "kompakt-system",
			"get", "lease", "kompakt.leader.election",
			"-o", "jsonpath={.spec.holderIdentity}",
		)
		if out == "" {
			return false
		}
		newLeader := strings.SplitN(out, "_", 2)[0]
		return newLeader != leader
	})

	// Pod must still be gated (huge resources, no capacity)
	out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
	if err != nil {
		t.Fatalf("failed to get pod after failover: %s", out)
	}
	if !strings.Contains(out, "kompakt.io/wait-for-workload-packing") {
		t.Fatalf("gated pod lost its gates during failover, gates: %q", out)
	}
}

// TestLeaderElection_WebhookDuringLeaderDeath verifies pods are still gated by
// the webhook even when the leader pod is down and no leader exists yet.
func TestLeaderElection_WebhookDuringLeaderDeath(t *testing.T) {
	scaleController(t, 2)
	defer scaleController(t, 1)

	profile := "e2e-ha-webhookdown"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Kill the leader
	leader := leaderPodName(t)
	t.Logf("killing leader: %s", leader)
	_, _ = kubectl("-n", "kompakt-system", "delete", "pod", leader, "--wait=false")

	// Immediately try to create a pod (before new leader is elected).
	// Webhook is stateless; any replica can serve it.
	podName := "e2e-ha-webhookdown-pod"
	defer cleanupPod(podName, "default")
	labels := map[string]string{"packer.kompakt.io/packing-profile": profile}

	waitFor(t, 15*time.Second, "pod creation during leader death", func() bool {
		cleanupPod(podName, "default")
		_, err := createHugePod(podName, "default", labels, nil)
		return err == nil
	})

	// Pod must be gated even though controller leader might be dead
	waitFor(t, 10*time.Second, "pod gated during leader transition", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/wait-for-workload-packing")
	})
}

// TestLeaderElection_NoSplitBrain scales to 3 replicas and verifies that only
// one pod holds the lease at any point. Reads the lease multiple times over a
// window to detect flapping or dual-holders.
func TestLeaderElection_NoSplitBrain(t *testing.T) {
	scaleController(t, 3)
	defer scaleController(t, 1)

	// Wait for all pods ready
	waitFor(t, 90*time.Second, "3 replicas available", func() bool {
		out, err := kubectl(
			"-n", "kompakt-system",
			"get", "deployment", "kompakt-controller",
			"-o", "jsonpath={.status.availableReplicas}",
		)
		if err != nil {
			return false
		}
		return out == "3"
	})

	// Wait for lease to stabilize after previous HA tests
	waitFor(t, 30*time.Second, "leader lease to be acquired", func() bool {
		out, _ := kubectl(
			"-n", "kompakt-system",
			"get", "lease", "kompakt.leader.election",
			"-o", "jsonpath={.spec.holderIdentity}",
		)
		if out == "" {
			return false
		}
		// Verify the holder is actually a running pod
		holder := strings.SplitN(out, "_", 2)[0]
		pods := controllerPodNames(t)
		for _, p := range pods {
			if p == holder {
				return true
			}
		}
		return false
	})

	// Sample the lease holder 10 times over 10 seconds
	leaders := make(map[string]bool)
	for i := 0; i < 10; i++ {
		out, err := kubectl(
			"-n", "kompakt-system",
			"get", "lease", "kompakt.leader.election",
			"-o", "jsonpath={.spec.holderIdentity}",
		)
		if err != nil || out == "" {
			t.Logf("sample %d: no holder", i)
			time.Sleep(time.Second)
			continue
		}
		holder := strings.SplitN(out, "_", 2)[0]
		leaders[holder] = true
		time.Sleep(time.Second)
	}

	if len(leaders) != 1 {
		t.Fatalf("expected exactly 1 leader across 10 samples, saw %d: %v", len(leaders), leaders)
	}

	for leader := range leaders {
		t.Logf("consistent leader across all samples: %s", leader)
	}
}

// TestLeaderElection_ControllerHealthy runs after all HA tests to verify the
// controller is back to a healthy single-replica state.
func TestLeaderElection_ControllerHealthy(t *testing.T) {
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "deployment", "kompakt-controller",
		"-o", "jsonpath={.status.availableReplicas}",
	)
	if err != nil {
		t.Fatalf("controller not found: %s", out)
	}
	if out != "1" {
		t.Fatalf("expected 1 replica after HA tests, got %q", out)
	}
}
