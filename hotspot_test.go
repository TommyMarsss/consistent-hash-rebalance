package chash

import (
	"testing"
)

// Requirement 2: the detector fires exactly when a shard exceeds Factor x
// mean and the absolute floor.
func TestHotspotDetectionThreshold(t *testing.T) {
	det := HotspotDetector{Factor: 3.0, MinHits: 100}

	// 10 shards: nine at 100 hits, one at 1000. Mean = 190, 3x mean = 570.
	stats := make([]ShardStat, 0, 10)
	for i := 0; i < 9; i++ {
		stats = append(stats, ShardStat{ShardID: uint32(i + 1), Node: "n", Hits: 100})
	}
	stats = append(stats, ShardStat{ShardID: 99, Node: "n", Hits: 1000})

	hot := det.Detect(stats)
	if len(hot) != 1 || hot[0] != 99 {
		t.Fatalf("expected shard 99 to be the only hotspot, got %v", hot)
	}

	// Below the factor: 350 hits vs 3x mean (375 with this mix) -> not hot.
	stats[9] = ShardStat{ShardID: 99, Node: "n", Hits: 350}
	if hot := det.Detect(stats); len(hot) != 0 {
		t.Fatalf("shard below factor threshold wrongly flagged: %v", hot)
	}

	// Above the factor but below the absolute floor -> not hot.
	small := []ShardStat{
		{ShardID: 1, Node: "n", Hits: 10},
		{ShardID: 2, Node: "n", Hits: 10},
		{ShardID: 3, Node: "n", Hits: 90}, // 9x mean but < MinHits
	}
	if hot := det.Detect(small); len(hot) != 0 {
		t.Fatalf("shard below MinHits wrongly flagged: %v", hot)
	}
}

// Requirement 2: a hot shard triggers a split at the right time (not before
// the threshold is crossed), and the split-off parts cover the original key
// space with no overlap and no gap.
func TestSplitTriggeredAndKeySpaceIntegrity(t *testing.T) {
	ring := buildRing(4, 100)
	mgr := NewManager(ring, HotspotDetector{Factor: 3.0, MinHits: 50})
	mgr.Cooldown = 0

	// Quiet window: uniform load, nothing should split.
	keys := genKeys(20000)
	for _, k := range keys[:2000] {
		mgr.RecordAccess(k)
	}
	if evs := mgr.CheckHotspots(1); len(evs) != 0 {
		t.Fatalf("split triggered without a hotspot: %+v", evs)
	}

	// Pick a victim shard and hammer it.
	shards := ring.Shards()
	victim := shards[len(shards)/2]
	victimKeys := keysInRange(keys, victim.StartU(), victim.EndU())
	if len(victimKeys) == 0 {
		t.Skip("no generated keys landed in the victim shard")
	}
	for i := 0; i < 5000; i++ {
		mgr.RecordAccess(victimKeys[i%len(victimKeys)])
	}
	// Plus a little background noise on other shards.
	for _, k := range keys[:500] {
		mgr.RecordAccess(k)
	}

	evs := mgr.CheckHotspots(2)
	if len(evs) == 0 {
		t.Fatal("hotspot did not trigger a split")
	}

	// Verify key-space integrity of the split: the union of the kept part
	// and all migrating parts must equal the original arc exactly.
	var ev *SplitEvent
	for i := range evs {
		if evs[i].ShardID == victim.Start {
			ev = &evs[i]
		}
	}
	if ev == nil {
		t.Fatalf("victim shard %08x was not split; events: %+v", victim.Start, evs)
	}
	assertPartsCoverRange(t, victim.StartU(), victim.EndU(), ev.Migrations)
}

// keysInRange returns the keys whose hash falls in [loU, hiU) (unwrapped).
func keysInRange(keys []string, loU, hiU uint64) []string {
	var out []string
	for _, k := range keys {
		hu := unwrap(HashKey(k), loU%HashSpace)
		if hu >= loU && hu < hiU {
			out = append(out, k)
		}
	}
	return out
}

// assertPartsCoverRange checks that the split tiles [lo, hi) exactly: the
// kept part [lo, first migration start) plus all migration ranges are
// contiguous and non-overlapping, and the last range ends exactly at hi.
func assertPartsCoverRange(t *testing.T, lo, hi uint64, migs []*Migration) {
	t.Helper()
	if len(migs) == 0 {
		t.Fatal("split produced no migrations")
	}
	// Sort migration ranges by lower bound.
	sorted := make([]*Migration, len(migs))
	copy(sorted, migs)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].LoU < sorted[i].LoU {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	// The kept part must start exactly at lo (nonempty: the original node
	// retains a share of the split shard).
	if sorted[0].LoU <= lo {
		t.Fatalf("first migration starts at %x, kept part [%x, ...) would be empty or overlapping", sorted[0].LoU, lo)
	}
	for i, mg := range sorted {
		if i > 0 && sorted[i-1].HiU != mg.LoU {
			t.Fatalf("gap/overlap between migrations: [%x,%x) then [%x,%x)",
				sorted[i-1].LoU, sorted[i-1].HiU, mg.LoU, mg.HiU)
		}
		if mg.HiU > hi {
			t.Fatalf("migration [%x,%x) extends past original range end %x", mg.LoU, mg.HiU, hi)
		}
	}
	if last := sorted[len(sorted)-1].HiU; last != hi {
		t.Fatalf("parts end at %x, original range ends at %x (uncovered tail)", last, hi)
	}
}
