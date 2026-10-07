package main

import (
	"testing"
)

func newTestShardManager(t *testing.T, nodes, parts int) *ShardManager {
	t.Helper()
	ring := buildRing(nodes, 100)
	sm, err := NewShardManager(ring, parts)
	if err != nil {
		t.Fatalf("NewShardManager: %v", err)
	}
	return sm
}

// The initial table must cover [0, 2^32) with no gaps or overlaps.
func TestInitialTableCoverage(t *testing.T) {
	sm := newTestShardManager(t, 4, 64)
	shards := sm.Shards()
	if len(shards) != 64 {
		t.Fatalf("got %d shards, want 64", len(shards))
	}
	if shards[0].Start != 0 {
		t.Fatalf("first shard starts at %d", shards[0].Start)
	}
	if shards[len(shards)-1].End != 0 {
		t.Fatalf("last shard ends at %d, want 0 (top of space)", shards[len(shards)-1].End)
	}
	for i := 1; i < len(shards); i++ {
		if shards[i-1].End != shards[i].Start {
			t.Fatalf("gap/overlap between %s and %s", shards[i-1].ID, shards[i].ID)
		}
	}
}

// After a split the sub-ranges must partition the original range exactly:
// no overlap, no hole, and the table invariant must still hold.
func TestSplitNoOverlapNoGap(t *testing.T) {
	for _, parts := range []int{2, 3, 5, 7} {
		sm := newTestShardManager(t, 4, 32)
		target := sm.Shards()[10]
		owners := make([]string, parts)
		for i := range owners {
			owners[i] = "n0"
		}
		plan, err := sm.planSplit(target.ID, parts, owners)
		if err != nil {
			t.Fatalf("planSplit(%d): %v", parts, err)
		}
		// union of subs == original range
		if plan.subs[0].Start != target.Start {
			t.Fatalf("parts=%d: first sub starts at %d, want %d", parts, plan.subs[0].Start, target.Start)
		}
		if plan.subs[len(plan.subs)-1].End != target.End {
			t.Fatalf("parts=%d: last sub ends at %d, want %d", parts, plan.subs[len(plan.subs)-1].End, target.End)
		}
		for i := 1; i < len(plan.subs); i++ {
			if plan.subs[i-1].End != plan.subs[i].Start {
				t.Fatalf("parts=%d: gap/overlap between subs %d and %d", parts, i-1, i)
			}
		}
		// execute and re-validate the live table
		mig := NewMigrator(sm)
		if _, err := mig.ExecuteSplit(plan, 0, "test", 1); err != nil {
			t.Fatalf("ExecuteSplit: %v", err)
		}
		if err := validateTable(sm.snapshot()); err != nil {
			t.Fatalf("parts=%d: table invalid after split: %v", parts, err)
		}
	}
}

// Stage 1 of a split must not change any routing decision.
func TestSplitStage1KeepsRouting(t *testing.T) {
	sm := newTestShardManager(t, 4, 32)
	target := sm.Shards()[5]
	plan, err := sm.planSplit(target.ID, 3, []string{"n1", "n2", "n3"})
	if err != nil {
		t.Fatal(err)
	}
	probe := makeKeys(5000)
	before := make(map[string]string, len(probe))
	for _, k := range probe {
		n, _ := sm.Route(k)
		before[k] = n
	}
	if err := sm.applySplitStage1(plan); err != nil {
		t.Fatal(err)
	}
	for _, k := range probe {
		n, _ := sm.Route(k)
		if n != before[k] {
			t.Fatalf("stage-1 split rerouted %q from %s to %s", k, before[k], n)
		}
	}
}

// Every hash position must route to exactly one shard, before and after
// many random splits (coverage invariant under churn).
func TestCoverageUnderRepeatedSplits(t *testing.T) {
	sm := newTestShardManager(t, 3, 16)
	mig := NewMigrator(sm)
	for round := 0; round < 20; round++ {
		shards := sm.Shards()
		target := shards[round%len(shards)]
		plan, err := sm.planSplit(target.ID, 2, []string{"n0", "n1"})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if _, err := mig.ExecuteSplit(plan, round, "churn", 1); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if err := validateTable(sm.snapshot()); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}
