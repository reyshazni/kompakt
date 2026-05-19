package matcher

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/reyshazni/kompakt/api/v1alpha1"
)

func TestResolve_NotFound(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	resolver := NewProfileResolver(client)
	_, err := resolver.Resolve(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for non-existent profile, got nil")
	}
}

func TestResolve_Found(t *testing.T) {
	profile := &v1alpha1.PackingProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-profile",
		},
		Spec: v1alpha1.PackingProfileSpec{
			DemandSource: v1alpha1.DemandSource{
				Type:      "ResourceRequest",
				Resources: []string{"cpu", "memory"},
			},
			CapacitySource: v1alpha1.CapacitySource{
				Type:      "NodeAllocatable",
				Resources: []string{"cpu", "memory"},
			},
			ReadinessSignal: v1alpha1.ReadinessSignal{},
			Rules: []v1alpha1.RuleRef{
				{Name: "BinPackOnInflightCapacity"},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(profile).
		Build()

	resolver := NewProfileResolver(client)
	got, err := resolver.Resolve(context.Background(), "test-profile")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "test-profile" {
		t.Fatalf("expected profile name test-profile, got %s", got.Name)
	}
	if len(got.Spec.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(got.Spec.Rules))
	}
}
