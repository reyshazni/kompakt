package ledger

import (
	"testing"
)

func TestAddNode_FindFit(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000, "memory": 8 * 1024 * 1024 * 1024})

	name, err := l.FindFit(map[string]int64{"cpu": 1000, "memory": 2 * 1024 * 1024 * 1024})
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

	_, err := l.FindFit(map[string]int64{"cpu": 500})
	if err == nil {
		t.Fatal("expected error for no capacity, got nil")
	}
}

func TestFindFit_BestFit(t *testing.T) {
	l := New()
	l.AddNode("big", map[string]int64{"cpu": 8000})
	l.AddNode("small", map[string]int64{"cpu": 2000})

	// BestFit should pick "small" (smallest sufficient)
	name, err := l.FindFit(map[string]int64{"cpu": 1000})
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

	name, err := l.FindFit(map[string]int64{"cpu": 2000})
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
	_, err := l.FindFit(map[string]int64{"cpu": 2000})
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
	_, err := l.FindFit(map[string]int64{"cpu": 2000})
	if err != nil {
		t.Fatalf("expected fit after release, got error: %v", err)
	}
}

func TestRemoveNode(t *testing.T) {
	l := New()
	l.AddNode("node-1", map[string]int64{"cpu": 4000})
	l.RemoveNode("node-1")

	_, err := l.FindFit(map[string]int64{"cpu": 1000})
	if err == nil {
		t.Fatal("expected no fit after node removal")
	}
}

func TestRemoveInflightNode(t *testing.T) {
	l := New()
	l.AddInflightNode("inflight-1", map[string]int64{"cpu": 4000})
	l.RemoveInflightNode("inflight-1")

	_, err := l.FindFit(map[string]int64{"cpu": 1000})
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
