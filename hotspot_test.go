package main

import (
	"fmt"
	"testing"
)

func ids(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("s%d", i)
	}
	return out
}

// A shard at >= Multiplier x mean (and >= MinCount) must be flagged.
func TestDetectorTriggersAboveThreshold(t *testing.T) {
	d := &HotspotDetector{Multiplier: 3.0, MinCount: 1000}
	counts := map[string]uint64{}
	for _, id := range ids(10) {
		counts[id] = 1000
	}
	counts["s4"] = 10_000 // mean = 1900, threshold = 5700
	hot, mean := d.Detect(ids(10), counts)
	if mean != 1900 {
		t.Fatalf("mean = %v, want 1900", mean)
	}
	if len(hot) != 1 || hot[0].ShardID != "s4" {
		t.Fatalf("hot = %+v, want only s4", hot)
	}
}

// Uniform load must produce no hotspots.
func TestDetectorUniformNoHot(t *testing.T) {
	d := NewHotspotDetector()
	counts := map[string]uint64{}
	for _, id := range ids(16) {
		counts[id] = 5000
	}
	if hot, _ := d.Detect(ids(16), counts); len(hot) != 0 {
		t.Fatalf("uniform load flagged as hot: %+v", hot)
	}
}

// A quiet system (all counts below MinCount) must never rebalance,
// even if one shard is relatively much hotter than the others.
func TestDetectorQuietBelowMinCount(t *testing.T) {
	d := &HotspotDetector{Multiplier: 3.0, MinCount: 1000}
	counts := map[string]uint64{"s0": 900, "s1": 10, "s2": 10, "s3": 10}
	if hot, _ := d.Detect(ids(4), counts); len(hot) != 0 {
		t.Fatalf("quiet system flagged hot: %+v", hot)
	}
}

// The split must happen at exactly the epoch where load crosses the
// threshold — not before, and not later.
func TestSplitTiming(t *testing.T) {
	d := &HotspotDetector{Multiplier: 3.0, MinCount: 1000}
	const hotFrom = 5
	var triggerEpochs []int
	for epoch := 0; epoch < 10; epoch++ {
		counts := map[string]uint64{}
		for _, id := range ids(10) {
			counts[id] = 1000
		}
		if epoch >= hotFrom {
			counts["s7"] = 8000 // mean 1700, threshold 5100
		}
		hot, _ := d.Detect(ids(10), counts)
		if len(hot) > 0 {
			triggerEpochs = append(triggerEpochs, epoch)
		}
	}
	if len(triggerEpochs) != 5 {
		t.Fatalf("triggered at %v, want epochs 5..9", triggerEpochs)
	}
	if triggerEpochs[0] != hotFrom {
		t.Fatalf("first trigger at epoch %d, want %d", triggerEpochs[0], hotFrom)
	}
}

// PickTargets must avoid the current owner when alternatives exist and
// must spread load across the least-loaded nodes.
func TestPickTargetsSpreads(t *testing.T) {
	nodes := []string{"a", "b", "c", "d"}
	lb := &LoadBalancer{
		nodeLoad: map[string]uint64{"a": 9000, "b": 100, "c": 200, "d": 300},
		nodes:    nodes,
	}
	got := lb.PickTargets(3, 1000, "a")
	seen := map[string]bool{}
	for _, n := range got {
		if n == "a" {
			t.Fatalf("picked the excluded owner: %v", got)
		}
		if seen[n] {
			t.Fatalf("picked %s twice while alternatives exist: %v", n, got)
		}
		seen[n] = true
	}
	if len(got) != 3 {
		t.Fatalf("got %d targets, want 3", len(got))
	}
}
