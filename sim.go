package main

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

// RatioCheck records the measured key-migration ratio for a topology
// change, compared against the 1/N theoretical expectation.
type RatioCheck struct {
	Event          string  `json:"event"`
	Expected       float64 `json:"expected"`
	Actual         float64 `json:"actual"`
	MovedKeys      int     `json:"movedKeys"`
	TotalKeys      int     `json:"totalKeys"`
	UnrelatedMoves int     `json:"unrelatedMoves"` // must be 0 for a correct ring
}

// ShardReport is the report view of one shard.
type ShardReport struct {
	ID       string `json:"id"`
	Start    uint32 `json:"start"`
	End      uint32 `json:"end"`
	Node     string `json:"node"`
	Heat     uint64 `json:"heat"`
	SplitSeq int    `json:"splitSeq"`
	Hot      bool   `json:"hot"`
}

// VNodeReport is the report view of one virtual node.
type VNodeReport struct {
	Hash uint32 `json:"hash"`
	Node string `json:"node"`
}

// Report is everything the HTML page needs, embedded as JSON.
type Report struct {
	GeneratedAt   string           `json:"generatedAt"`
	Nodes         []string         `json:"nodes"`
	VNodes        []VNodeReport    `json:"vnodes"`
	Shards        []ShardReport    `json:"shards"`
	Events        []MigrationEvent `json:"events"`
	RatioChecks   []RatioCheck     `json:"ratioChecks"`
	Epochs        int              `json:"epochs"`
	Requests      int              `json:"requests"`
	DetectorMult  float64          `json:"detectorMult"`
	DetectorMin   uint64           `json:"detectorMin"`
	InitialShards int              `json:"initialShards"`
}

// SimConfig tunes the simulation.
type SimConfig struct {
	Nodes         int
	VNodes        int
	Shards        int
	Epochs        int
	ReqPerEpoch   int
	CensusKeys    int
	KeySpace      int
	HotStartEpoch int
	HotFraction   float64 // fraction of requests aimed at the hot shard
	SplitParts    int
	Seed          int64
}

func defaultSimConfig() SimConfig {
	return SimConfig{
		Nodes:         6,
		VNodes:        150,
		Shards:        64,
		Epochs:        24,
		ReqPerEpoch:   8000,
		CensusKeys:    20000,
		KeySpace:      20000,
		HotStartEpoch: 6,
		HotFraction:   0.45,
		SplitParts:    3,
		Seed:          42,
	}
}

// runSim executes the whole scenario and returns the report.
func runSim(cfg SimConfig) (*Report, error) {
	rng := rand.New(rand.NewSource(cfg.Seed))
	ring := NewRing(cfg.VNodes)
	for i := 0; i < cfg.Nodes; i++ {
		ring.AddNode(nodeName(i))
	}
	sm, err := NewShardManager(ring, cfg.Shards)
	if err != nil {
		return nil, err
	}
	det := NewHotspotDetector()
	mig := NewMigrator(sm)

	// Census keys: a fixed population used to measure migration ratios
	// and to count keys moved by each rebalancing event.
	census := make([]string, cfg.CensusKeys)
	for i := range census {
		census[i] = fmt.Sprintf("census-%d", i)
	}
	countInRange := func(s *Shard) int {
		n := 0
		for _, k := range census {
			if s.contains(hashKey(k)) {
				n++
			}
		}
		return n
	}
	mig.CopyKeys = countInRange

	// Request stream: zipf-skewed keys, plus a deterministic hot band
	// injected from HotStartEpoch on: a pool of keys that all land in
	// one chosen shard.
	zipf := rand.NewZipf(rng, 1.2, 1.0, uint64(cfg.KeySpace-1))
	hotPool := buildHotPool(rng, sm, 400)

	heat := map[string]uint64{}      // cumulative per-shard heat
	hotShardIDs := map[string]bool{} // shards that ever triggered a split
	var checks []RatioCheck
	totalReq := 0

	for epoch := 0; epoch < cfg.Epochs; epoch++ {
		counts := map[string]uint64{}
		for i := 0; i < cfg.ReqPerEpoch; i++ {
			var key string
			if epoch >= cfg.HotStartEpoch && rng.Float64() < cfg.HotFraction {
				key = hotPool[rng.Intn(len(hotPool))]
			} else {
				key = fmt.Sprintf("key-%d", zipf.Uint64())
			}
			_, sid := sm.Route(key)
			counts[sid]++
			heat[sid]++
			totalReq++
		}

		// Topology changes at fixed epochs.
		if epoch == 10 {
			checks = append(checks, addNodeAndCheck(ring, sm, nodeName(cfg.Nodes), census))
		}
		if epoch == 16 {
			checks = append(checks, removeNodeAndCheck(ring, sm, nodeName(1), census))
		}

		// Hotspot detection + split (at most one split per epoch keeps
		// the event log readable and avoids oscillation).
		ids := shardIDs(sm)
		hot, mean := det.Detect(ids, counts)
		if len(hot) > 0 {
			h := hot[0]
			sh, _ := sm.shardByID(h.ShardID)
			parts := cfg.SplitParts
			lb := NewLoadBalancer(ring.Nodes(), sm.Shards(), counts)
			share := h.Count / uint64(parts)
			targets := lb.PickTargets(parts, share, sh.NodeID)
			plan, err := sm.planSplit(h.ShardID, parts, targets)
			if err == nil {
				if _, err := mig.ExecuteSplit(plan, epoch,
					fmt.Sprintf("hotspot: %s at %.1fx mean (mean=%.0f)", h.ShardID, h.Ratio, mean),
					h.Ratio); err != nil {
					return nil, err
				}
				// highlight the pieces born from the hot shard
				for _, sub := range plan.subs {
					hotShardIDs[sub.ID] = true
				}
			}
		}
	}

	rep := &Report{
		GeneratedAt:   time.Now().Format(time.RFC3339),
		Nodes:         ring.Nodes(),
		Events:        mig.Events(),
		RatioChecks:   checks,
		Epochs:        cfg.Epochs,
		Requests:      totalReq,
		DetectorMult:  det.Multiplier,
		DetectorMin:   det.MinCount,
		InitialShards: cfg.Shards,
	}
	for _, v := range ring.VNodes() {
		rep.VNodes = append(rep.VNodes, VNodeReport{Hash: v.hash, Node: v.node})
	}
	for _, s := range sm.Shards() {
		rep.Shards = append(rep.Shards, ShardReport{
			ID: s.ID, Start: s.Start, End: s.End, Node: s.NodeID,
			Heat: heat[s.ID], SplitSeq: s.SplitSeq, Hot: hotShardIDs[s.ID],
		})
	}
	sort.Slice(rep.Shards, func(i, j int) bool { return rep.Shards[i].Start < rep.Shards[j].Start })
	return rep, nil
}

func nodeName(i int) string { return fmt.Sprintf("node-%c", 'A'+i) }

func shardIDs(sm *ShardManager) []string {
	shards := sm.Shards()
	ids := make([]string, len(shards))
	for i, s := range shards {
		ids[i] = s.ID
	}
	return ids
}

// buildHotPool returns keys that all hash into one arbitrarily chosen
// shard (the one containing hash position 1/3 of the space).
func buildHotPool(rng *rand.Rand, sm *ShardManager, n int) []string {
	targetPos := uint32(1 << 32 / 3)
	_, sid := sm.RouteHash(targetPos)
	sh, _ := sm.shardByID(sid)
	pool := make([]string, 0, n)
	for len(pool) < n {
		k := fmt.Sprintf("hot-%d", rng.Int63())
		if sh.contains(hashKey(k)) {
			pool = append(pool, k)
		}
	}
	return pool
}

// addNodeAndCheck adds a node, measures the key-migration ratio on the
// census, and reassigns shards whose ring owner changed.
func addNodeAndCheck(ring *Ring, sm *ShardManager, node string, census []string) RatioCheck {
	before := make(map[string]string, len(census))
	for _, k := range census {
		before[k] = ring.GetNode(k)
	}
	n := len(ring.Nodes())
	ring.AddNode(node)
	moved, unrelated := 0, 0
	for _, k := range census {
		after := ring.GetNode(k)
		if after != before[k] {
			moved++
			if after != node {
				unrelated++ // a correct ring never does this
			}
		}
	}
	reassignShards(ring, sm)
	return RatioCheck{
		Event:          "add " + node,
		Expected:       1.0 / float64(n+1),
		Actual:         float64(moved) / float64(len(census)),
		MovedKeys:      moved,
		TotalKeys:      len(census),
		UnrelatedMoves: unrelated,
	}
}

// removeNodeAndCheck mirrors addNodeAndCheck for node removal.
func removeNodeAndCheck(ring *Ring, sm *ShardManager, node string, census []string) RatioCheck {
	before := make(map[string]string, len(census))
	for _, k := range census {
		before[k] = ring.GetNode(k)
	}
	n := len(ring.Nodes())
	ring.RemoveNode(node)
	moved, unrelated := 0, 0
	for _, k := range census {
		after := ring.GetNode(k)
		if after != before[k] {
			moved++
			if before[k] != node {
				unrelated++ // only keys of the removed node may move
			}
		}
	}
	reassignShards(ring, sm)
	return RatioCheck{
		Event:          "remove " + node,
		Expected:       1.0 / float64(n),
		Actual:         float64(moved) / float64(len(census)),
		MovedKeys:      moved,
		TotalKeys:      len(census),
		UnrelatedMoves: unrelated,
	}
}

// reassignShards moves every shard whose ring owner (by midpoint) no
// longer matches its current owner — the shard-level reflection of the
// ring's minimal migration.
func reassignShards(ring *Ring, sm *ShardManager) {
	for _, s := range sm.Shards() {
		mid := s.Start + uint32(s.size()/2)
		want := ring.GetNodeByHash(mid)
		if want != "" && want != s.NodeID {
			_ = sm.applyMove(s.ID, want)
		}
	}
}
