package chash

import (
	"math"
	"testing"
)

// End-to-end: the full simulation must detect the injected hotspot, split
// it, keep routing consistent at every epoch, and measure node add/remove
// migration ratios close to 1/N.
func TestSimulationInvariants(t *testing.T) {
	cfg := DefaultSimConfig()
	rep := RunSim(cfg)

	if rep.ConsistencyError != 0 {
		t.Fatalf("simulation produced %d routing consistency errors", rep.ConsistencyError)
	}
	if len(rep.Migrations) == 0 {
		t.Fatal("injected hotspot never triggered a split migration")
	}
	if len(rep.RatioChecks) != 2 {
		t.Fatalf("expected 2 ratio checks (add+remove), got %d", len(rep.RatioChecks))
	}
	for _, rc := range rep.RatioChecks {
		// The exact minimality property must always hold: only keys
		// involving the added/removed node may move.
		if !rc.OnlyAffected {
			t.Fatalf("%s: keys unrelated to the membership change moved", rc.Label)
		}
		// The ratio should be in the vicinity of the ideal 1/N (looser for
		// removes, which happen after hotspot rebalancing has deliberately
		// skewed ownership).
		if math.Abs(rc.Measured-rc.Expected) > 0.1 {
			t.Fatalf("%s: measured ratio %.4f too far from expected %.4f",
				rc.Label, rc.Measured, rc.Expected)
		}
		t.Logf("%s: measured %.4f expected %.4f", rc.Label, rc.Measured, rc.Expected)
	}
	// All migrations must have completed by the end of the run.
	for _, mg := range rep.Migrations {
		if mg.EndEpoch == 0 {
			t.Fatalf("migration #%d never completed", mg.ID)
		}
	}
	// Final shards must tile the hash space.
	var total uint64
	for _, s := range rep.FinalShards {
		end := uint64(s.End)
		if end <= uint64(s.Start) {
			end += HashSpace
		}
		total += end - uint64(s.Start)
	}
	if total != HashSpace {
		t.Fatalf("final shards cover %d units, want %d", total, HashSpace)
	}
}
