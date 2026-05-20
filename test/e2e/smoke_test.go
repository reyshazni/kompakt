package e2e

import (
	"strings"
	"testing"
	"time"
)

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
