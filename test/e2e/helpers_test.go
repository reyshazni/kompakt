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

// leaderPodName returns the name of the current leader by reading the Lease object.
func leaderPodName(t *testing.T) string {
	t.Helper()
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "lease", "kompakt.leader.election",
		"-o", "jsonpath={.spec.holderIdentity}",
	)
	if err != nil {
		t.Fatalf("failed to get leader lease: %s", out)
	}
	// holderIdentity format is "pod-name_uuid", extract pod name
	parts := strings.SplitN(out, "_", 2)
	return parts[0]
}

// controllerPodNames returns all controller pod names.
func controllerPodNames(t *testing.T) []string {
	t.Helper()
	out, err := kubectl(
		"-n", "kompakt-system",
		"get", "pods", "-l", "app.kubernetes.io/name=kompakt",
		"-o", "jsonpath={.items[*].metadata.name}",
	)
	if err != nil {
		t.Fatalf("failed to list controller pods: %s", out)
	}
	if out == "" {
		return nil
	}
	return strings.Fields(out)
}

// scaleController sets the deployment replica count and waits for rollout.
func scaleController(t *testing.T, replicas int) {
	t.Helper()
	out, err := kubectl(
		"-n", "kompakt-system",
		"scale", "deployment/kompakt-controller",
		fmt.Sprintf("--replicas=%d", replicas),
	)
	if err != nil {
		t.Fatalf("failed to scale controller to %d: %s", replicas, out)
	}
	waitFor(t, 90*time.Second, fmt.Sprintf("controller scaled to %d", replicas), func() bool {
		out, err := kubectl(
			"-n", "kompakt-system",
			"get", "deployment", "kompakt-controller",
			"-o", "jsonpath={.status.availableReplicas}",
		)
		if err != nil {
			return false
		}
		return out == fmt.Sprintf("%d", replicas)
	})
}
