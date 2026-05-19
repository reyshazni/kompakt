package e2e

import (
	"context"
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
	ctx := context.Background()
	_ = ctx

	profile := `
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: e2e-test-cpu
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
  reservationTimeout: 1m
`

	// Create profile
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(profile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create PackingProfile: %s", string(out))
	}
	defer kubectl("delete", "packingprofile", "e2e-test-cpu", "--ignore-not-found")

	// Verify profile exists
	out2, err := kubectl("get", "packingprofile", "e2e-test-cpu")
	if err != nil {
		t.Fatalf("profile not found after create: %s", string(out2))
	}
}

func TestPodGatingWithValidProfile(t *testing.T) {
	// Create profile first
	profile := `
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: e2e-gating-test
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
  reservationTimeout: 1m
`
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(profile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create profile: %s", string(out))
	}
	defer kubectl("delete", "packingprofile", "e2e-gating-test", "--ignore-not-found")

	// Create a pod with the profile label
	pod := `
apiVersion: v1
kind: Pod
metadata:
  name: e2e-gated-pod
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: e2e-gating-test
spec:
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: 10m
          memory: 16Mi
`
	cmd = exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(pod)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create pod: %s", string(out))
	}
	defer kubectl("delete", "pod", "e2e-gated-pod", "-n", "default", "--ignore-not-found")

	// Verify the pod has a scheduling gate
	waitFor(t, 10*time.Second, "pod to have scheduling gate", func() bool {
		out, err := kubectl(
			"get", "pod", "e2e-gated-pod", "-n", "default",
			"-o", "jsonpath={.spec.schedulingGates[*].name}",
		)
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})
}

func TestPodRejectedWithInvalidProfile(t *testing.T) {
	// Create a pod referencing a non-existent profile
	pod := `
apiVersion: v1
kind: Pod
metadata:
  name: e2e-rejected-pod
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: does-not-exist
spec:
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: 10m
          memory: 16Mi
`
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(pod)
	out, err := cmd.CombinedOutput()
	defer kubectl("delete", "pod", "e2e-rejected-pod", "-n", "default", "--ignore-not-found")

	// The webhook should reject this pod
	if err == nil {
		t.Fatalf("expected pod creation to be rejected, but it succeeded: %s", string(out))
	}

	outStr := string(out)
	if !strings.Contains(strings.ToLower(outStr), "not found") &&
		!strings.Contains(strings.ToLower(outStr), "denied") {
		t.Logf("pod was rejected (expected), output: %s", outStr)
	}
}

func TestPodWithExcludeLabel(t *testing.T) {
	// Create profile
	profile := `
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: e2e-exclude-test
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
  reservationTimeout: 1m
`
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(profile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create profile: %s", string(out))
	}
	defer kubectl("delete", "packingprofile", "e2e-exclude-test", "--ignore-not-found")

	// Create pod with both profile label and exclude label
	pod := `
apiVersion: v1
kind: Pod
metadata:
  name: e2e-excluded-pod
  namespace: default
  labels:
    packer.kompakt.io/packing-profile: e2e-exclude-test
    kompakt.io/exclude: "true"
spec:
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: 10m
          memory: 16Mi
`
	cmd = exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(pod)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create excluded pod: %s", string(out))
	}
	defer kubectl("delete", "pod", "e2e-excluded-pod", "-n", "default", "--ignore-not-found")

	// Verify the pod does NOT have a scheduling gate
	time.Sleep(2 * time.Second)
	out, err := kubectl(
		"get", "pod", "e2e-excluded-pod", "-n", "default",
		"-o", "jsonpath={.spec.schedulingGates}",
	)
	if err != nil {
		t.Fatalf("failed to get pod: %s", out)
	}
	if strings.Contains(out, "kompakt.io/") {
		t.Fatalf("excluded pod should not have scheduling gates, got: %s", out)
	}
}

func TestPodWithoutLabelPassesThrough(t *testing.T) {
	// Create a pod without any kompakt label
	pod := `
apiVersion: v1
kind: Pod
metadata:
  name: e2e-unlabeled-pod
  namespace: default
spec:
  containers:
    - name: app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          cpu: 10m
          memory: 16Mi
`
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(pod)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create unlabeled pod: %s", string(out))
	}
	defer kubectl("delete", "pod", "e2e-unlabeled-pod", "-n", "default", "--ignore-not-found")

	// Verify the pod does NOT have a scheduling gate
	time.Sleep(2 * time.Second)
	out, err := kubectl(
		"get", "pod", "e2e-unlabeled-pod", "-n", "default",
		"-o", "jsonpath={.spec.schedulingGates}",
	)
	if err != nil {
		t.Fatalf("failed to get pod: %s", out)
	}
	if strings.Contains(out, "kompakt.io/") {
		t.Fatalf("unlabeled pod should not have scheduling gates, got: %s", out)
	}
}
