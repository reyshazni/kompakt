package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCase001_WaitForInflightNodeReady tests the full GPU scale-from-zero flow:
// Pod A passthroughs to trigger autoscaler, Pod B held on inflight, node arrives,
// Pod B released with real affinity.
//
// Simulates GOATScaler by injecting fake ProvisionNode events and fake nodes.
func TestCase001_WaitForInflightNodeReady(t *testing.T) {
	profile := "e2e-case001"
	defer cleanupProfile(profile)

	// Profile that demands a resource kind nodes don't have (fake.io/gpu).
	// WaitForScaleUp only (no BinPack, since we want passthrough behavior).
	profileYAML := fmt.Sprintf(`
apiVersion: packer.kompakt.io/v1alpha1
kind: PackingProfile
metadata:
  name: %s
spec:
  demandSource:
    type: ResourceRequest
    resources: [fake.io/gpu]
  capacitySource:
    type: NodeAllocatable
    resources: [fake.io/gpu]
    nodeGroupTemplates:
      - instanceType: ecs.fake-gpu.xlarge
        allocatable:
          fake.io/gpu: 2000
        labels:
          node.kubernetes.io/instance-type: ecs.fake-gpu.xlarge
  readinessSignal:
    nodeConditions:
      - type: Ready
        status: "True"
  rules:
    - name: WaitForScaleUp
  reservationTimeout: 5m
`, profile)
	if out, err := kubectlApply(profileYAML); err != nil {
		t.Fatalf("create profile: %s", out)
	}

	// --- Step 1: Pod A passthrough ---
	// No capacity anywhere (kind nodes don't have fake.io/gpu, no inflight).
	// WaitForScaleUp should release immediately (passthrough to trigger autoscaler).
	podA := "e2e-case001-pod-a"
	defer cleanupPod(podA, "default")

	podAYAML := fmt.Sprintf(`
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
    - name: gpu-app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          fake.io/gpu: "1"
        limits:
          fake.io/gpu: "1"
`, podA, profile)

	var lastErr error
	waitFor(t, 15*time.Second, "Pod A creation", func() bool {
		cleanupPod(podA, "default")
		_, err := kubectlApply(podAYAML)
		if err != nil {
			lastErr = err
			return false
		}
		return true
	})
	if lastErr != nil {
		t.Fatalf("failed to create Pod A: %v", lastErr)
	}

	// Pod A should passthrough (released without affinity)
	waitFor(t, 30*time.Second, "Pod A passthrough", func() bool {
		out, err := podField(podA, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})
	t.Log("Step 1 OK: Pod A passthrough")

	// --- Step 2: Inject fake GOATScaler ProvisionNode event ---
	// Simulates GOATScaler deciding to provision a GPU node.
	eventYAML := fmt.Sprintf(`
apiVersion: v1
kind: Event
metadata:
  name: e2e-case001-provision
  namespace: default
involvedObject:
  apiVersion: v1
  kind: Pod
  name: %s
  namespace: default
reason: ProvisionNode
message: "Provision node asa-case001-fake in Zone: us-east-1a with InstanceType: ecs.fake-gpu.xlarge, Triggered time %s"
source:
  component: GOATScaler
type: Normal
firstTimestamp: "%s"
lastTimestamp: "%s"
`, podA,
		time.Now().Format("2006-01-02 15:04:05.000"),
		time.Now().Format(time.RFC3339),
		time.Now().Format(time.RFC3339))

	if out, err := kubectlApply(eventYAML); err != nil {
		t.Fatalf("inject GOATScaler event: %s", out)
	}
	defer func() { _, _ = kubectl("delete", "event", "e2e-case001-provision", "-n", "default", "--ignore-not-found") }()
	t.Log("Step 2 OK: GOATScaler event injected")

	// Give controller time to pick up the event
	time.Sleep(3 * time.Second)

	// --- Step 3: Pod B should be held on inflight node ---
	podB := "e2e-case001-pod-b"
	defer cleanupPod(podB, "default")

	podBYAML := fmt.Sprintf(`
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
    - name: gpu-app
      image: busybox
      command: ["sleep", "3600"]
      resources:
        requests:
          fake.io/gpu: "1"
        limits:
          fake.io/gpu: "1"
`, podB, profile)

	if out, err := kubectlApply(podBYAML); err != nil {
		t.Fatalf("create Pod B: %s", out)
	}

	// Pod B should stay gated (held by WaitForScaleUp on inflight node)
	waitFor(t, 15*time.Second, "Pod B gated", func() bool {
		out, err := podField(podB, "default", "{.spec.schedulingGates[*].name}")
		if err != nil {
			return false
		}
		return strings.Contains(out, "kompakt.io/")
	})

	// Verify held-by annotation
	waitFor(t, 15*time.Second, "Pod B held-by annotation", func() bool {
		out, _ := podField(podB, "default", "{.metadata.annotations.kompakt\\.io/held-by}")
		return out == "WaitForScaleUp"
	})
	t.Log("Step 3 OK: Pod B held on inflight node")

	// --- Step 4: Simulate node arrival ---
	// Create a fake node with the provision-task-id label and the demanded resource.
	fakeNode := "e2e-case001-gpu-node"
	defer func() { _, _ = kubectl("delete", "node", fakeNode, "--ignore-not-found") }()

	nodeYAML := fmt.Sprintf(`
apiVersion: v1
kind: Node
metadata:
  name: %s
  labels:
    goatscaler.io/provision-task-id: asa-case001-fake
    goatscaler.io/managed: "true"
    node.kubernetes.io/instance-type: ecs.fake-gpu.xlarge
spec: {}
status:
  conditions:
    - type: Ready
      status: "True"
      lastHeartbeatTime: "%s"
      lastTransitionTime: "%s"
  allocatable:
    cpu: "16"
    memory: 64Gi
    fake.io/gpu: "2"
`, fakeNode, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))

	if out, err := kubectlApply(nodeYAML); err != nil {
		t.Fatalf("create fake node: %s", out)
	}
	t.Log("Step 4 OK: Fake node created")

	// --- Step 5: Pod B should be released with affinity to the fake node ---
	waitFor(t, 60*time.Second, "Pod B released", func() bool {
		out, err := podField(podB, "default", "{.spec.schedulingGates}")
		if err != nil {
			return false
		}
		return !strings.Contains(out, "kompakt.io/")
	})

	// Verify gate-reason
	reason, _ := podField(podB, "default", "{.metadata.annotations.kompakt\\.io/gate-reason}")
	if reason != "capacity" {
		t.Fatalf("expected gate-reason=capacity, got %q", reason)
	}

	// Verify target-node points to the fake node
	targetNode, _ := podField(podB, "default", "{.metadata.annotations.kompakt\\.io/target-node}")
	if targetNode != fakeNode {
		t.Fatalf("expected target-node=%s, got %q", fakeNode, targetNode)
	}

	// Verify node affinity
	affinity, _ := podField(podB, "default", "{.spec.affinity.nodeAffinity}")
	if !strings.Contains(affinity, fakeNode) {
		t.Fatalf("expected node affinity to %s, got %s", fakeNode, affinity)
	}

	t.Log("Step 5 OK: Pod B released with affinity to fake node")

	// Verify held-by annotation was cleaned up
	heldBy, _ := podField(podB, "default", "{.metadata.annotations.kompakt\\.io/held-by}")
	if heldBy != "" {
		t.Fatalf("expected held-by cleared after release, got %q", heldBy)
	}

	// Verify GateReleased event on Pod B
	waitFor(t, 10*time.Second, "GateReleased event on Pod B", func() bool {
		out, _ := kubectl("get", "events", "-n", "default",
			"--field-selector", fmt.Sprintf("involvedObject.name=%s,reason=GateReleased", podB),
			"-o", "jsonpath={.items[*].message}")
		return strings.Contains(out, "gate released")
	})

	t.Log("Case 001 PASSED: full GOATScaler inflight flow verified")
}
