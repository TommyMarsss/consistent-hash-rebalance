package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// End-to-end: the simulation must trigger at least one hotspot split,
// keep topology-change migration ratios near 1/N with zero unrelated
// moves, and produce a self-contained HTML report.
func TestSimEndToEnd(t *testing.T) {
	cfg := defaultSimConfig()
	rep, err := runSim(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Events) == 0 {
		t.Fatal("no hotspot split was triggered; the hot band should force one")
	}
	if len(rep.RatioChecks) != 2 {
		t.Fatalf("got %d ratio checks, want 2 (add + remove)", len(rep.RatioChecks))
	}
	for _, c := range rep.RatioChecks {
		if c.UnrelatedMoves != 0 {
			t.Errorf("%s: %d unrelated moves", c.Event, c.UnrelatedMoves)
		}
		if math.Abs(c.Actual-c.Expected) > c.Expected*0.6+0.01 {
			t.Errorf("%s: actual %.4f too far from expected %.4f", c.Event, c.Actual, c.Expected)
		}
	}
	out := filepath.Join(t.TempDir(), "report.html")
	if err := writeReportHTML(rep, out); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() < 10_000 {
		t.Fatalf("report suspiciously small: %d bytes", fi.Size())
	}
}
