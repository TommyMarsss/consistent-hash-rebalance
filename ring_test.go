package chash

import (
	"fmt"
	"math"
	"testing"
)

func genKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%06d", i)
	}
	return keys
}

func buildRing(nodes, vnodes int) *Ring {
	r := NewRing(vnodes)
	for i := 0; i < nodes; i++ {
		r.AddNode(fmt.Sprintf("node-%d", i))
	}
	return r
}

func routeAll(r *Ring, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = r.GetNode(k)
	}
	return out
}

// Requirement 1: adding a node must move ~1/(N+1) of keys, and every moved
// key must move to the new node (no unrelated reshuffling).
func TestAddNodeMigrationRatio(t *testing.T) {
	const numNodes = 5
	r := buildRing(numNodes, 150)
	keys := genKeys(50000)
	before := routeAll(r, keys)

	r.AddNode("node-new")

	moved := 0
	for _, k := range keys {
		after := r.GetNode(k)
		if after != before[k] {
			moved++
			if after != "node-new" {
				t.Fatalf("key %s moved from %s to unrelated node %s", k, before[k], after)
			}
		}
	}
	ratio := float64(moved) / float64(len(keys))
	expected := 1.0 / float64(numNodes+1)
	if math.Abs(ratio-expected) > 0.05 {
		t.Fatalf("migration ratio %.4f deviates from expected %.4f by more than 0.05", ratio, expected)
	}
	t.Logf("add node: moved %d/%d = %.4f (expected %.4f)", moved, len(keys), ratio, expected)
}

// Requirement 1: removing a node must move ~1/N of keys, and only keys that
// were on the removed node may move.
func TestRemoveNodeMigrationRatio(t *testing.T) {
	const numNodes = 6
	r := buildRing(numNodes, 150)
	keys := genKeys(50000)
	before := routeAll(r, keys)

	r.RemoveNode("node-2")

	moved := 0
	for _, k := range keys {
		after := r.GetNode(k)
		if after != before[k] {
			moved++
			if before[k] != "node-2" {
				t.Fatalf("key %s moved off %s although node-2 was removed", k, before[k])
			}
			if after == "node-2" {
				t.Fatalf("key %s still routed to removed node", k)
			}
		}
	}
	ratio := float64(moved) / float64(len(keys))
	expected := 1.0 / float64(numNodes)
	if math.Abs(ratio-expected) > 0.05 {
		t.Fatalf("migration ratio %.4f deviates from expected %.4f by more than 0.05", ratio, expected)
	}
	t.Logf("remove node: moved %d/%d = %.4f (expected %.4f)", moved, len(keys), ratio, expected)
}

// The arcs of the ring must partition the whole hash space exactly.
func TestRingArcsCoverFullHashSpace(t *testing.T) {
	r := buildRing(4, 100)
	var total uint64
	for _, s := range r.Shards() {
		total += s.Width()
	}
	if total != HashSpace {
		t.Fatalf("arcs cover %d hash units, want %d", total, HashSpace)
	}
}

// Routing must be deterministic: repeated lookups agree.
func TestRoutingDeterministic(t *testing.T) {
	r := buildRing(4, 100)
	for _, k := range genKeys(5000) {
		if r.GetNode(k) != r.GetNode(k) {
			t.Fatalf("non-deterministic route for %s", k)
		}
	}
}
