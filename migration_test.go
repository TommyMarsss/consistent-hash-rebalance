package chash

import (
	"testing"
)

// Requirement 3: throughout an in-progress migration, every key must route
// to exactly one node; keys in the migrating range may only route to From
// or To according to the cursor, and keys outside the range must be
// unaffected.
func TestRoutingConsistencyDuringMigration(t *testing.T) {
	ring := buildRing(4, 100)
	mgr := NewManager(ring, HotspotDetector{Factor: 3, MinHits: 50})
	keys := genKeys(30000)

	// Choose a shard and split it manually into two parts.
	shards := ring.Shards()
	victim := shards[10]
	lo, hi := victim.StartU(), victim.EndU()
	mid := lo + (hi-lo)/2
	from := victim.Node
	to := ""
	for _, n := range ring.Nodes() {
		if n != from {
			to = n
			break
		}
	}
	mg := &Migration{ID: 1, ShardID: victim.Start, LoU: mid, HiU: hi, From: from, To: to, CursorU: mid}
	mgr.migrations = append(mgr.migrations, mg)

	// Baseline routing for keys outside the migrating range.
	type outside struct {
		key  string
		node string
	}
	var outsideKeys []outside
	for _, k := range keys {
		if !mg.Contains(HashKey(k)) {
			outsideKeys = append(outsideKeys, outside{k, mgr.Route(k)})
		}
	}

	// Advance the migration in 20 steps; check invariants at each step.
	steps := 20
	stepSize := (hi - mid) / uint64(steps)
	for step := 0; step < steps; step++ {
		mg.advance(stepSize)
		for _, k := range keys {
			h := HashKey(k)
			got := mgr.Route(k)
			// Determinism: a second lookup must agree.
			if got != mgr.Route(k) {
				t.Fatalf("step %d: key %s routed non-deterministically", step, k)
			}
			if mg.Contains(h) {
				want := from
				if unwrap(h, mg.LoU%HashSpace) < mg.CursorU {
					want = to
				}
				if got != want {
					t.Fatalf("step %d: key %s in migrating range routed to %s, want %s (cursor=%x)",
						step, k, got, want, mg.CursorU)
				}
			}
		}
		// Keys outside the range never move.
		for _, oc := range outsideKeys {
			if got := mgr.Route(oc.key); got != oc.node {
				t.Fatalf("step %d: unrelated key %s moved from %s to %s", step, oc.key, oc.node, got)
			}
		}
	}

	// Finish and commit.
	mg.CursorU = mg.HiU
	done := mgr.AdvanceMigrations(1.0)
	if len(done) != 1 {
		t.Fatalf("expected 1 completed migration, got %d", len(done))
	}
	if len(mgr.ActiveMigrations()) != 0 {
		t.Fatalf("migrations still active after completion")
	}

	// After commit: keys in [mid, hi) belong to `to`; keys in [lo, mid)
	// still belong to `from`.
	for _, k := range keys {
		h := HashKey(k)
		hu := unwrap(h, lo)
		if hu >= mid && hu < hi {
			if got := mgr.Route(k); got != to {
				t.Fatalf("after commit: key %s routed to %s, want %s", k, got, to)
			}
		} else if hu >= lo && hu < mid {
			if got := mgr.Route(k); got != from {
				t.Fatalf("after commit: key %s in kept part routed to %s, want %s", k, got, from)
			}
		}
	}

	// The ring must still partition the hash space exactly.
	var total uint64
	for _, s := range ring.Shards() {
		total += s.Width()
	}
	if total != HashSpace {
		t.Fatalf("after commit: arcs cover %d units, want %d", total, HashSpace)
	}
}

// Requirement 3: concurrent migrations on disjoint ranges must not
// interfere — each key is claimed by at most one migration.
func TestConcurrentMigrationsDisjoint(t *testing.T) {
	ring := buildRing(4, 100)
	mgr := NewManager(ring, HotspotDetector{Factor: 3, MinHits: 50})
	keys := genKeys(20000)

	shards := ring.Shards()
	nodes := ring.Nodes()
	target := func(from string) string {
		for _, n := range nodes {
			if n != from {
				return n
			}
		}
		return ""
	}
	// Split two different shards.
	for i, idx := range []int{5, len(shards) / 2} {
		s := shards[idx]
		lo, hi := s.StartU(), s.EndU()
		mid := lo + (hi-lo)/2
		mgr.migrations = append(mgr.migrations, &Migration{
			ID: i + 1, ShardID: s.Start, LoU: mid, HiU: hi,
			From: s.Node, To: target(s.Node), CursorU: mid,
		})
	}

	for step := 0; step < 10; step++ {
		for _, mg := range mgr.migrations {
			mg.advance((mg.HiU - mg.LoU) / 10)
		}
		for _, k := range keys {
			h := HashKey(k)
			claims := 0
			var want string
			for _, mg := range mgr.migrations {
				if mg.Contains(h) {
					claims++
					want = mg.RouteHash(h)
				}
			}
			if claims > 1 {
				t.Fatalf("key %s claimed by %d migrations", k, claims)
			}
			if claims == 1 && mgr.Route(k) != want {
				t.Fatalf("key %s routed to %s, migration says %s", k, mgr.Route(k), want)
			}
			if mgr.Route(k) == "" {
				t.Fatalf("key %s has no route", k)
			}
		}
	}
}
