package ledger

import (
	"errors"
	"math"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
)

var errNoFit = errors.New("no node with sufficient capacity")

type nodeEntry struct {
	allocatable map[string]int64
	used        map[string]int64
	reserved    map[string]int64
	labels      map[string]string
	taints      []corev1.Taint
}

// PodSchedulingConstraints holds the subset of pod scheduling constraints
// that Kompakt checks during fit evaluation.
type PodSchedulingConstraints struct {
	Tolerations  []corev1.Toleration
	NodeSelector map[string]string
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

// AddNode registers an existing node with its allocatable resources, labels, and taints.
func (l *NodeLedger) AddNode(name string, allocatable map[string]int64, labels map[string]string, taints []corev1.Taint) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodes[name] = &nodeEntry{
		allocatable: copyMap(allocatable),
		used:        make(map[string]int64),
		reserved:    make(map[string]int64),
		labels:      copyStringMap(labels),
		taints:      taints,
	}
}

// ClearNodes removes all existing nodes from the ledger.
func (l *NodeLedger) ClearNodes() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodes = make(map[string]*nodeEntry)
}

// ClearInflight removes all in-flight nodes from the ledger.
func (l *NodeLedger) ClearInflight() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inflight = make(map[string]*nodeEntry)
}

// ClearInflightByPrefix removes in-flight nodes whose name starts with the
// given prefix. Used for per-profile isolation: each profile clears only its
// own inflight entries (prefixed with "profileName/").
func (l *NodeLedger) ClearInflightByPrefix(prefix string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for name := range l.inflight {
		if strings.HasPrefix(name, prefix) {
			delete(l.inflight, name)
		}
	}
}

// SnapshotReservations returns a copy of all current reservations
// keyed by node name. Used to preserve reservations across ledger rebuilds.
func (l *NodeLedger) SnapshotReservations() map[string]map[string]int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	snap := make(map[string]map[string]int64)
	for name, n := range l.nodes {
		if len(n.reserved) > 0 {
			snap[name] = copyMap(n.reserved)
		}
	}
	for name, n := range l.inflight {
		if len(n.reserved) > 0 {
			snap[name] = copyMap(n.reserved)
		}
	}
	return snap
}

// RestoreReservations re-applies previously snapshotted reservations.
// Reservations for nodes that no longer exist in the ledger are silently dropped.
func (l *NodeLedger) RestoreReservations(snap map[string]map[string]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for name, reserved := range snap {
		if n, ok := l.nodes[name]; ok {
			n.reserved = copyMap(reserved)
		}
		if n, ok := l.inflight[name]; ok {
			n.reserved = copyMap(reserved)
		}
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
func (l *NodeLedger) AddInflightNode(name string, allocatable map[string]int64, labels map[string]string, taints []corev1.Taint) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inflight[name] = &nodeEntry{
		allocatable: copyMap(allocatable),
		used:        make(map[string]int64),
		reserved:    make(map[string]int64),
		labels:      copyStringMap(labels),
		taints:      taints,
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
// If constraints is non-nil, nodes must also satisfy taints/tolerations and nodeSelector.
func (l *NodeLedger) FindFit(demand map[string]int64, constraints *PodSchedulingConstraints) (name string, isInflight bool, err error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	bestName := ""
	bestSlack := int64(math.MaxInt64)
	bestInflight := false

	check := func(nodeName string, n *nodeEntry, inflight bool) {
		// Check scheduling constraints (taints, nodeSelector)
		if !nodeMatchesConstraints(n, constraints) {
			return
		}
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
func (l *NodeLedger) FindFitExisting(demand map[string]int64, constraints *PodSchedulingConstraints) (string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	bestName := ""
	bestSlack := int64(math.MaxInt64)

	for name, n := range l.nodes {
		if !nodeMatchesConstraints(n, constraints) {
			continue
		}
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

// HasInflightSignal returns true if there are in-flight nodes with unknown
// capacity (empty allocatable). This indicates Layer 1 detected a scale-up
// but Layer 2 hasn't provided capacity data yet. Used by WaitForScaleUp
// to hold pods when a node is coming but capacity is unknown.
func (l *NodeLedger) HasInflightSignal() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, n := range l.inflight {
		if len(n.allocatable) == 0 {
			return true
		}
	}
	return false
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

func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// nodeMatchesConstraints checks taints/tolerations and nodeSelector.
func nodeMatchesConstraints(n *nodeEntry, constraints *PodSchedulingConstraints) bool {
	if constraints == nil {
		return true
	}
	// Check taints: every NoSchedule/NoExecute taint must be tolerated
	for _, taint := range n.taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if !toleratesTaint(constraints.Tolerations, taint) {
			return false
		}
	}
	// Check nodeSelector: all selector labels must exist on node
	for key, val := range constraints.NodeSelector {
		if n.labels[key] != val {
			return false
		}
	}
	return true
}

func toleratesTaint(tolerations []corev1.Toleration, taint corev1.Taint) bool {
	for _, t := range tolerations {
		if t.Operator == corev1.TolerationOpExists && (t.Key == "" || t.Key == taint.Key) {
			if t.Effect == "" || t.Effect == taint.Effect {
				return true
			}
		}
		if t.Key == taint.Key && t.Value == taint.Value {
			if t.Effect == "" || t.Effect == taint.Effect {
				return true
			}
		}
	}
	return false
}
