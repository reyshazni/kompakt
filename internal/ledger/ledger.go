package ledger

import (
	"errors"
	"math"
	"sync"
)

var errNoFit = errors.New("no node with sufficient capacity")

type nodeEntry struct {
	allocatable map[string]int64
	used        map[string]int64
	reserved    map[string]int64
}

func (n *nodeEntry) available(resource string) int64 {
	return n.allocatable[resource] - n.used[resource] - n.reserved[resource]
}

// NodeLedger tracks cluster capacity: existing nodes, in-flight nodes, and reservations.
type NodeLedger struct {
	mu       sync.RWMutex
	nodes    map[string]*nodeEntry
	inflight map[string]*nodeEntry
}

// New creates an empty ledger.
func New() *NodeLedger {
	return &NodeLedger{
		nodes:    make(map[string]*nodeEntry),
		inflight: make(map[string]*nodeEntry),
	}
}

// AddNode registers an existing node with its allocatable resources.
func (l *NodeLedger) AddNode(name string, allocatable map[string]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodes[name] = &nodeEntry{
		allocatable: copyMap(allocatable),
		used:        make(map[string]int64),
		reserved:    make(map[string]int64),
	}
}

// RemoveNode removes a node from tracking.
func (l *NodeLedger) RemoveNode(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.nodes, name)
}

// UpdateUsage sets the current resource consumption on a node.
func (l *NodeLedger) UpdateUsage(name string, used map[string]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n, ok := l.nodes[name]; ok {
		n.used = copyMap(used)
	}
}

// AddInflightNode registers a node that is being provisioned.
func (l *NodeLedger) AddInflightNode(name string, allocatable map[string]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inflight[name] = &nodeEntry{
		allocatable: copyMap(allocatable),
		used:        make(map[string]int64),
		reserved:    make(map[string]int64),
	}
}

// RemoveInflightNode removes an in-flight node (arrived or failed).
func (l *NodeLedger) RemoveInflightNode(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.inflight, name)
}

// Reserve holds capacity on a node for a gated pod.
func (l *NodeLedger) Reserve(nodeName string, demand map[string]int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := l.findEntry(nodeName)
	if n == nil {
		return errNoFit
	}

	for res, qty := range demand {
		if n.available(res) < qty {
			return errNoFit
		}
	}

	for res, qty := range demand {
		n.reserved[res] += qty
	}
	return nil
}

// ReleaseReservation frees reserved capacity on a node.
func (l *NodeLedger) ReleaseReservation(nodeName string, demand map[string]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := l.findEntry(nodeName)
	if n == nil {
		return
	}
	for res, qty := range demand {
		n.reserved[res] -= qty
		if n.reserved[res] < 0 {
			n.reserved[res] = 0
		}
	}
}

// FindFit returns the name of the node with the smallest sufficient
// unreserved capacity (BestFit). Considers both existing and in-flight nodes.
// isInflight indicates whether the match came from an in-flight node.
// When existing and in-flight nodes have equal slack, existing is preferred.
func (l *NodeLedger) FindFit(demand map[string]int64) (name string, isInflight bool, err error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	bestName := ""
	bestSlack := int64(math.MaxInt64)
	bestInflight := false

	check := func(nodeName string, n *nodeEntry, inflight bool) {
		fits := true
		var totalSlack int64
		for res, qty := range demand {
			avail := n.available(res)
			if avail < qty {
				fits = false
				break
			}
			totalSlack += avail - qty
		}
		if !fits {
			return
		}
		// Prefer existing over inflight at equal slack
		if totalSlack < bestSlack || (totalSlack == bestSlack && !inflight && bestInflight) {
			bestName = nodeName
			bestSlack = totalSlack
			bestInflight = inflight
		}
	}

	for n, e := range l.nodes {
		check(n, e, false)
	}
	for n, e := range l.inflight {
		check(n, e, true)
	}

	if bestName == "" {
		return "", false, errNoFit
	}
	return bestName, bestInflight, nil
}

// FindFitExisting returns the name of the existing node with the smallest
// sufficient unreserved capacity (BestFit). Only considers existing nodes,
// not in-flight nodes.
func (l *NodeLedger) FindFitExisting(demand map[string]int64) (string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	bestName := ""
	bestSlack := int64(math.MaxInt64)

	for name, n := range l.nodes {
		fits := true
		var totalSlack int64
		for res, qty := range demand {
			avail := n.available(res)
			if avail < qty {
				fits = false
				break
			}
			totalSlack += avail - qty
		}
		if fits && totalSlack < bestSlack {
			bestName = name
			bestSlack = totalSlack
		}
	}

	if bestName == "" {
		return "", errNoFit
	}
	return bestName, nil
}

// LedgerSnapshot holds a point-in-time summary of ledger state for metrics.
type LedgerSnapshot struct {
	NodeCount        int
	InflightCount    int
	TotalAllocatable map[string]int64
}

// Snapshot returns a summary of the current ledger state.
func (l *NodeLedger) Snapshot() LedgerSnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()

	total := make(map[string]int64)
	for _, n := range l.nodes {
		for res, qty := range n.allocatable {
			total[res] += qty
		}
	}

	return LedgerSnapshot{
		NodeCount:        len(l.nodes),
		InflightCount:    len(l.inflight),
		TotalAllocatable: total,
	}
}

func (l *NodeLedger) findEntry(name string) *nodeEntry {
	if n, ok := l.nodes[name]; ok {
		return n
	}
	if n, ok := l.inflight[name]; ok {
		return n
	}
	return nil
}

func copyMap(m map[string]int64) map[string]int64 {
	c := make(map[string]int64, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
