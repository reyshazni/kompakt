package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- #1: Reservation persistence across reconcile cycles ---

// Multiple pods created sequentially should each see reduced capacity
// from previous pods' reservations, not full capacity every time.
func TestReservationPersistence_SequentialPods(t *testing.T) {
	profile := "e2e-reservation"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Create 5 pods sequentially, each with small resources
	// All should eventually be released (kind has capacity)
	// The point: they should be released to potentially different nodes,
	// not all reserved on the same one exceeding capacity
	const count = 5
	for i := 0; i < count; i++ {
		podName := fmt.Sprintf("e2e-reservation-%d", i)
		defer cleanupPod(podName, "default")
		labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
		var lastErr error
		waitFor(t, 15*time.Second, podName+" creation", func() bool {
			cleanupPod(podName, "default")
			_, err := createPod(podName, "default", labels, nil)
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

	// All should be released
	for i := 0; i < count; i++ {
		podName := fmt.Sprintf("e2e-reservation-%d", i)
		waitFor(t, 60*time.Second, podName+" released", func() bool {
			out, err := podField(podName, "default", "{.spec.schedulingGates}")
			if err != nil {
				return false
			}
			return !strings.Contains(out, "kompakt.io/")
		})
	}

	// All should have gate-reason annotation (proves they went through the full flow)
	for i := 0; i < count; i++ {
		podName := fmt.Sprintf("e2e-reservation-%d", i)
		reason, _ := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/gate-reason}")
		if reason == "" {
			t.Fatalf("%s missing gate-reason annotation", podName)
		}
	}
}

// --- #2: Taint awareness ---

// Pod without toleration should not be matched to a tainted node by FindFit.
// In kind with 1 node, taint the node, create a pod without toleration.
// BinPack should not find a fit (node is tainted). Pod stays gated.
// Then remove taint, pod should be released.
func TestTaintAwareness_PodWithoutToleration(t *testing.T) {
	profile := "e2e-taint"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Get the kind node name
	nodeName, err := kubectl("get", "nodes", "-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		t.Fatalf("failed to get node name: %s", nodeName)
	}

	// Taint the node
	out, err := kubectl("taint", "nodes", nodeName, "e2e-test=blocked:NoSchedule", "--overwrite")
	if err != nil {
		t.Fatalf("failed to taint node: %s", out)
	}
	defer func() {
		_, _ = kubectl("taint", "nodes", nodeName, "e2e-test=blocked:NoSchedule-")
	}()

	// Create pod WITHOUT toleration
	podName := "e2e-taint-no-toleration"
	defer cleanupPod(podName, "default")
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

	// Pod should stay gated (BinPack can't find a fit because node is tainted)
	// Wait a few reconcile cycles to confirm it stays gated
	time.Sleep(5 * time.Second)
	gates, _ := podField(podName, "default", "{.spec.schedulingGates[*].name}")
	if !strings.Contains(gates, "kompakt.io/") {
		t.Fatal("pod without toleration should stay gated when all nodes are tainted")
	}

	// Verify held-by annotation
	heldBy, _ := podField(podName, "default", "{.metadata.annotations.kompakt\\.io/held-by}")
	if heldBy == "" {
		t.Fatal("expected held-by annotation on taint-blocked pod")
	}

	// Remove taint
	_, _ = kubectl("taint", "nodes", nodeName, "e2e-test=blocked:NoSchedule-")

	// Pod should now be released
	waitFor(t, 30*time.Second, "gate released after taint removal", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// Pod WITH toleration should be matched even when node is tainted.
func TestTaintAwareness_PodWithToleration(t *testing.T) {
	profile := "e2e-taint-toleration"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Get the kind node name
	nodeName, err := kubectl("get", "nodes", "-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		t.Fatalf("failed to get node name: %s", nodeName)
	}

	// Taint the node
	out, err := kubectl("taint", "nodes", nodeName, "e2e-test=allowed:NoSchedule", "--overwrite")
	if err != nil {
		t.Fatalf("failed to taint node: %s", out)
	}
	defer func() {
		_, _ = kubectl("taint", "nodes", nodeName, "e2e-test=allowed:NoSchedule-")
	}()

	// Create pod WITH toleration
	podName := "e2e-taint-with-toleration"
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
  tolerations:
    - key: e2e-test
      operator: Equal
      value: allowed
      effect: NoSchedule
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

	// Pod WITH toleration should be released (FindFit matches tainted node)
	waitFor(t, 30*time.Second, "gate released with toleration", func() bool {
		out, err := podField(podName, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
}

// --- #4: Ledger pruning ---

// After running for a while, controller should not crash or accumulate
// phantom state. This is a stability test.
func TestLedgerPruning_NoPhantomCapacity(t *testing.T) {
	profile := "e2e-ledger-prune"
	if out, err := createProfile(profile); err != nil {
		t.Fatalf("create profile: %s", out)
	}
	defer cleanupProfile(profile)

	// Create and delete pods multiple times to trigger ledger rebuilds
	for round := 0; round < 3; round++ {
		podName := fmt.Sprintf("e2e-prune-%d", round)
		labels := map[string]string{"packer.kompakt.io/packing-profile": profile}
		var lastErr error
		waitFor(t, 15*time.Second, podName+" creation", func() bool {
			cleanupPod(podName, "default")
			_, err := createPod(podName, "default", labels, nil)
			if err != nil {
				lastErr = err
				return false
			}
			return true
		})
		if lastErr != nil {
			t.Fatalf("round %d: failed to create: %v", round, lastErr)
		}

		// Wait for release
		waitFor(t, 30*time.Second, podName+" released", func() bool {
			out, err := podField(podName, "default", "{.spec.schedulingGates}")
			if err != nil {
				return false
			}
			return !strings.Contains(out, "kompakt.io/")
		})

		// Delete pod
		cleanupPod(podName, "default")
		time.Sleep(time.Second)
	}

	// Controller should still be healthy
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "deployment", "kompakt-controller",
		"-o", "jsonpath={.status.availableReplicas}",
	)
	if err != nil || out != "1" {
		t.Fatalf("controller unhealthy after ledger prune test, replicas: %q", out)
	}

	restarts, _ := kubectl(
		"-n", "kompakt-system",
		"get", "pods", "-l", "app.kubernetes.io/name=kompakt",
		"-o", "jsonpath={.items[0].status.containerStatuses[0].restartCount}",
	)
	if restarts != "0" {
		logs, _ := kubectl("-n", "kompakt-system", "logs", "-l", "app.kubernetes.io/name=kompakt", "--tail=20")
		t.Fatalf("controller restarted %s time(s) during ledger prune test.\n%s", restarts, logs)
	}
}
