package main

import (
	"fmt"
	"math"
	"testing"
)

// routeAll maps every key to its ring node.
func routeAll(r *Ring, keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = r.GetNode(k)
	}
	return out
}

func makeKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}
	return keys
}

func buildRing(nodes, vnodes int) *Ring {
	r := NewRing(vnodes)
	for i := 0; i < nodes; i++ {
		r.AddNode(fmt.Sprintf("n%d", i))
	}
	return r
}

// Adding one node to N must move ~1/(N+1) of keys, and every moved key
// must move TO the new node (no unrelated reshuffling).
func TestAddNodeMigrationRatio(t *testing.T) {
	const N = 8
	r := buildRing(N, 200)
	keys := makeKeys(100_000)
	before := routeAll(r, keys)

	r.AddNode("n-new")
	after := routeAll(r, keys)

	moved, unrelated := 0, 0
	for _, k := range keys {
		if after[k] != before[k] {
			moved++
			if after[k] != "n-new" {
				unrelated++
			}
		}
	}
	if unrelated != 0 {
		t.Fatalf("%d keys moved between old nodes on add; want 0", unrelated)
	}
	want := 1.0 / (N + 1)
	got := float64(moved) / float64(len(keys))
	if math.Abs(got-want) > want*0.5 {
		t.Fatalf("add-node migration ratio %.4f, want ≈ %.4f (±50%%)", got, want)
	}
	t.Logf("add: moved %.2f%% (theory %.2f%%)", got*100, want*100)
}

// Removing one of N nodes must move ~1/N of keys, and every moved key
// must come FROM the removed node.
func TestRemoveNodeMigrationRatio(t *testing.T) {
	const N = 9
	r := buildRing(N, 200)
	keys := makeKeys(100_000)
	before := routeAll(r, keys)

	r.RemoveNode("n3")
	after := routeAll(r, keys)

	moved, unrelated := 0, 0
	for _, k := range keys {
		if after[k] != before[k] {
			moved++
			if before[k] != "n3" {
				unrelated++
			}
		}
	}
	if unrelated != 0 {
		t.Fatalf("%d keys moved although they were not on the removed node", unrelated)
	}
	want := 1.0 / N
	got := float64(moved) / float64(len(keys))
	if math.Abs(got-want) > want*0.5 {
		t.Fatalf("remove-node migration ratio %.4f, want ≈ %.4f (±50%%)", got, want)
	}
	t.Logf("remove: moved %.2f%% (theory %.2f%%)", got*100, want*100)
}

// Routing must be deterministic and stable while the ring is unchanged.
func TestRingDeterministic(t *testing.T) {
	r := buildRing(5, 150)
	for _, k := range makeKeys(1000) {
		if r.GetNode(k) != r.GetNode(k) {
			t.Fatalf("non-deterministic route for %q", k)
		}
	}
	if got := (&Ring{}).GetNode("x"); got != "" {
		t.Fatalf("empty ring returned %q", got)
	}
}

// Every key must land on one of the ring's nodes (sanity, full coverage).
func TestRingCoverage(t *testing.T) {
	r := buildRing(4, 100)
	valid := map[string]bool{}
	for _, n := range r.Nodes() {
		valid[n] = true
	}
	for _, k := range makeKeys(10_000) {
		if !valid[r.GetNode(k)] {
			t.Fatalf("key %q routed to unknown node", k)
		}
	}
}
