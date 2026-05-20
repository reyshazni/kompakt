package ledger

import (
	"testing"
)

func TestAddNode_FindFit(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000, "memory": 8 * 1024 * 1024 * 1024})

	name, _, err := l.FindFit(map[string]int64{"cpu": 1000, "memory": 2 * 1024 * 1024 * 1024})
	if err != nil {
		t.Fatalf("expected fit, got error: %v", err)
	}
	if name != "node-1" {
		t.Fatalf("expected node-1, got %s", name)
	}
}

func TestFindFit_NoCapacity(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 1000})
	l.UpdateUsage("node-1", map[string]int64{"cpu": 1000})

	_, _, err := l.FindFit(map[string]int64{"cpu": 500})
	if err == nil {
		t.Fatal("expected error for no capacity, got nil")
	}
}

func TestFindFit_BestFit(t *testing.T) {
	l := New()
	l.AddNode("big", map[string]int64{"cpu": 8000})
	l.AddNode("small", map[string]int64{"cpu": 2000})

	// BestFit should pick "small" (smallest sufficient)
	name, _, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "small" {
		t.Fatalf("expected small (best fit), got %s", name)
	}
}

func TestInflightNode_FindFit(t *testing.T) {
	l := New()
	// No existing nodes, but an inflight node coming
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})

	name, _, err := l.FindFit(map[string]int64{"cpu": 2000})
	if err != nil {
		t.Fatalf("expected fit on inflight node, got error: %v", err)
	}
	if name != "inflight-1" {
		t.Fatalf("expected inflight-1, got %s", name)
	}
}

func TestReserve_ReducesAvailable(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})

	if err := l.Reserve("node-1", map[string]int64{"cpu": 3000}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only 1000 left, 2000 demand should fail
	_, _, err := l.FindFit(map[string]int64{"cpu": 2000})
	if err == nil {
		t.Fatal("expected no fit after reservation")
	}
}

func TestReleaseReservation_FreesSlot(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})
	_ = l.Reserve("node-1", map[string]int64{"cpu": 3000})

	l.ReleaseReservation("node-1", map[string]int64{"cpu": 3000})

	// Should fit again
	_, _, err := l.FindFit(map[string]int64{"cpu": 2000})
	if err != nil {
		t.Fatalf("expected fit after release, got error: %v", err)
	}
}

func TestRemoveNode(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})
	l.RemoveNode("node-1")

	_, _, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err == nil {
		t.Fatal("expected no fit after node removal")
	}
}

func TestRemoveInflightNode(t *testing.T) {
	l := New()
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})
	l.RemoveInflightNode("inflight-1")

	_, _, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err == nil {
		t.Fatal("expected no fit after inflight removal")
	}
}

func TestReserve_InsufficientCapacity(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 2000})

	err := l.Reserve("node-1", map[string]int64{"cpu": 3000})
	if err == nil {
		t.Fatal("expected error reserving more than available")
	}
}

func TestInflightNode_EmptyAllocatable_NeverFits(t *testing.T) {
	// This is the critical bug: the ClusterAutoscalerDetector produces
	// inflight nodes with empty allocatable. available() returns 0 for
	// all resources, so FindFit never matches these nodes.
	l := New()
	l.AddInflightNode("inflight-1", map[string]int64{})

	_, _, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err == nil {
		t.Fatal("expected no fit on inflight node with empty allocatable, but got a fit")
	}
}

func TestFindFit_MultiResource(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000, "memory": 8000})

	// Fits both resources
	name, _, err := l.FindFit(map[string]int64{"cpu": 2000, "memory": 4000})
	if err != nil {
		t.Fatalf("expected fit, got error: %v", err)
	}
	if name != "node-1" {
		t.Fatalf("expected node-1, got %s", name)
	}

	// One resource exceeds capacity
	_, _, err = l.FindFit(map[string]int64{"cpu": 2000, "memory": 16000})
	if err == nil {
		t.Fatal("expected no fit when memory exceeds capacity")
	}
}

func TestFindFit_BestFit_MixedExistingAndInflight(t *testing.T) {
	l := New()
	l.AddNode("existing", map[string]int64{"cpu": 8000})
	l.AddInflightNode("inflight", map[string]int64{"cpu": 2000})

	// BestFit should pick inflight (smallest sufficient = 2000-1000=1000 slack)
	name, _, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "inflight" {
		t.Fatalf("expected inflight (best fit), got %s", name)
	}
}

func TestReserve_OnInflightNode(t *testing.T) {
	l := New()
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})

	if err := l.Reserve("inflight-1", map[string]int64{"cpu": 2000}); err != nil {
		t.Fatalf("expected reserve on inflight to succeed, got: %v", err)
	}

	// Only 2000 left, 3000 demand should fail
	_, _, err := l.FindFit(map[string]int64{"cpu": 3000})
	if err == nil {
		t.Fatal("expected no fit after reserving on inflight node")
	}
}

func TestReserve_NonExistentNode(t *testing.T) {
	l := New()

	err := l.Reserve("does-not-exist", map[string]int64{"cpu": 1000})
	if err == nil {
		t.Fatal("expected error reserving on non-existent node")
	}
}

func TestReleaseReservation_NonExistentNode(t *testing.T) {
	l := New()
	// Should not panic
	l.ReleaseReservation("does-not-exist", map[string]int64{"cpu": 1000})
}

func TestReleaseReservation_UnderflowProtection(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})
	_ = l.Reserve("node-1", map[string]int64{"cpu": 1000})

	// Release more than reserved -- should clamp to 0, not go negative
	l.ReleaseReservation("node-1", map[string]int64{"cpu": 5000})

	// Node should have full capacity available
	name, _, err := l.FindFit(map[string]int64{"cpu": 4000})
	if err != nil {
		t.Fatalf("expected fit after underflow release, got: %v", err)
	}
	if name != "node-1" {
		t.Fatalf("expected node-1, got %s", name)
	}
}

func TestUpdateUsage_NonExistentNode(t *testing.T) {
	l := New()
	// Should not panic
	l.UpdateUsage("does-not-exist", map[string]int64{"cpu": 1000})
}

func TestAddNode_OverwritesExisting(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 2000})
	l.AddNode("node-1", map[string]int64{"cpu": 8000})

	// Should use the new capacity (8000), not old (2000)
	name, _, err := l.FindFit(map[string]int64{"cpu": 6000})
	if err != nil {
		t.Fatalf("expected fit with overwritten capacity, got: %v", err)
	}
	if name != "node-1" {
		t.Fatalf("expected node-1, got %s", name)
	}
}

func TestSnapshot(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000, "memory": 8000})
	l.AddNode("node-2", map[string]int64{"cpu": 2000, "memory": 4000})
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})

	snap := l.Snapshot()
	if snap.NodeCount != 2 {
		t.Fatalf("expected 2 nodes, got %d", snap.NodeCount)
	}
	if snap.InflightCount != 1 {
		t.Fatalf("expected 1 inflight, got %d", snap.InflightCount)
	}
	if snap.TotalAllocatable["cpu"] != 6000 {
		t.Fatalf("expected 6000 total cpu, got %d", snap.TotalAllocatable["cpu"])
	}
	if snap.TotalAllocatable["memory"] != 12000 {
		t.Fatalf("expected 12000 total memory, got %d", snap.TotalAllocatable["memory"])
	}
}

func TestSnapshot_ExcludesInflight(t *testing.T) {
	l := New()
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})

	snap := l.Snapshot()
	if snap.TotalAllocatable["cpu"] != 0 {
		t.Fatalf("snapshot should not include inflight allocatable, got cpu=%d", snap.TotalAllocatable["cpu"])
	}
}

func TestFindFit_EmptyLedger(t *testing.T) {
	l := New()
	_, _, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err == nil {
		t.Fatal("expected no fit on empty ledger")
	}
}

func TestFindFit_DemandResourceNotInAllocatable(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})

	// Demand a resource that doesn't exist in allocatable
	_, _, err := l.FindFit(map[string]int64{"nvidia.com/gpu": 1})
	if err == nil {
		t.Fatal("expected no fit when demanded resource not in allocatable")
	}
}

func TestReserve_MultipleReservations(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})

	if err := l.Reserve("node-1", map[string]int64{"cpu": 1000}); err != nil {
		t.Fatalf("first reserve failed: %v", err)
	}
	if err := l.Reserve("node-1", map[string]int64{"cpu": 1000}); err != nil {
		t.Fatalf("second reserve failed: %v", err)
	}
	if err := l.Reserve("node-1", map[string]int64{"cpu": 1000}); err != nil {
		t.Fatalf("third reserve failed: %v", err)
	}
	// 4000 - 3000 reserved = 1000 left, so 2000 demand should fail
	err := l.Reserve("node-1", map[string]int64{"cpu": 2000})
	if err == nil {
		t.Fatal("expected error on fourth reserve exceeding remaining capacity")
	}
}

func TestFindFit_UsageReducesAvailable(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})
	l.UpdateUsage("node-1", map[string]int64{"cpu": 3000})

	// Only 1000 available, 2000 demand should fail
	_, _, err := l.FindFit(map[string]int64{"cpu": 2000})
	if err == nil {
		t.Fatal("expected no fit when usage consumes capacity")
	}

	// 1000 demand should fit
	name, _, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("expected fit for 1000 with 1000 available, got: %v", err)
	}
	if name != "node-1" {
		t.Fatalf("expected node-1, got %s", name)
	}
}

// --- FindFit with isInflight ---

func TestFindFit_ReturnsIsInflight_True(t *testing.T) {
	l := New()
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})

	name, isInflight, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("expected fit, got error: %v", err)
	}
	if name != "inflight-1" {
		t.Fatalf("expected inflight-1, got %s", name)
	}
	if !isInflight {
		t.Fatal("expected isInflight=true for inflight node")
	}
}

func TestFindFit_ReturnsIsInflight_False(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})

	name, isInflight, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("expected fit, got error: %v", err)
	}
	if name != "node-1" {
		t.Fatalf("expected node-1, got %s", name)
	}
	if isInflight {
		t.Fatal("expected isInflight=false for existing node")
	}
}

func TestFindFit_PrefersExistingOverInflight_WhenEqualSlack(t *testing.T) {
	l := New()
	l.AddNode("existing", map[string]int64{"cpu": 2000})
	l.AddInflightNode("inflight", map[string]int64{"cpu": 2000})

	// Both have same slack (1000). Should prefer existing (stable capacity).
	name, isInflight, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("expected fit, got error: %v", err)
	}
	if isInflight {
		t.Fatalf("expected existing node preferred over inflight, got %s (isInflight=%v)", name, isInflight)
	}
}

func TestFindFit_PicksBestFitAcrossBoth(t *testing.T) {
	l := New()
	l.AddNode("big-existing", map[string]int64{"cpu": 8000})
	l.AddInflightNode("small-inflight", map[string]int64{"cpu": 2000})

	// small-inflight has less slack (1000 vs 7000), should win BestFit
	name, isInflight, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("expected fit, got error: %v", err)
	}
	if name != "small-inflight" {
		t.Fatalf("expected small-inflight (best fit), got %s", name)
	}
	if !isInflight {
		t.Fatal("expected isInflight=true")
	}
}

// --- FindFitExisting ---

func TestFindFitExisting_IgnoresInflight(t *testing.T) {
	l := New()
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})

	_, err := l.FindFitExisting(map[string]int64{"cpu": 1000})
	if err == nil {
		t.Fatal("expected no fit from FindFitExisting when only inflight nodes exist")
	}
}

func TestFindFitExisting_FindsExisting(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 2000})

	name, err := l.FindFitExisting(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("expected fit on existing, got error: %v", err)
	}
	if name != "node-1" {
		t.Fatalf("expected node-1, got %s", name)
	}
}

func TestFindFitExisting_BestFitAmongExisting(t *testing.T) {
	l := New()
	l.AddNode("big", map[string]int64{"cpu": 8000})
	l.AddNode("small", map[string]int64{"cpu": 2000})
	l.AddInflightNode("smallest", map[string]int64{"cpu": 1500})

	// BestFit among existing only: small (slack=1000) beats big (slack=7000)
	// smallest inflight (slack=500) should be ignored
	name, err := l.FindFitExisting(map[string]int64{"cpu": 1000})
	if err != nil {
		t.Fatalf("expected fit, got error: %v", err)
	}
	if name != "small" {
		t.Fatalf("expected small (best fit among existing), got %s", name)
	}
}
