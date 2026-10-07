package chash

import "sort"

// SplitEvent describes a split that was started for a hot shard.
type SplitEvent struct {
	ShardID    uint32
	From       string
	Migrations []*Migration // one per split-off part
}

// Manager owns the live routing state: the ring plus any in-flight
// migrations. It is the single place that answers "which node serves this
// key", which is what keeps routing unique and deterministic while
// migrations are in progress.
type Manager struct {
	Ring      *Ring
	Detector  HotspotDetector
	MaxParts  int // max parts a hot shard is split into (>= 2)
	Cooldown  int // epochs a shard is left alone after being split
	MaxActive int // max concurrent migrations (0 = unlimited)

	hits       map[uint32]uint64 // shard start -> hits in current window
	migrations []*Migration      // active migrations
	cooldown   map[uint32]int    // shard start -> epoch until which splits are suppressed
	nextMigID  int
}

// NewManager creates a Manager over ring r.
func NewManager(r *Ring, det HotspotDetector) *Manager {
	return &Manager{
		Ring:      r,
		Detector:  det,
		MaxParts:  3,
		Cooldown:  2,
		MaxActive: 16,
		hits:      make(map[uint32]uint64),
		cooldown:  make(map[uint32]int),
		nextMigID: 1,
	}
}

// Route returns the single node that currently serves key. Keys inside an
// in-flight migration range are routed by the migration cursor; every other
// key is routed by the ring.
func (m *Manager) Route(key string) string {
	return m.RouteHash(HashKey(key))
}

// RouteHash is Route for an already-hashed key.
func (m *Manager) RouteHash(h uint32) string {
	for _, mg := range m.migrations {
		if mg.Contains(h) {
			return mg.RouteHash(h)
		}
	}
	return m.Ring.GetNodeForHash(h)
}

// RecordAccess attributes one access of key to the shard containing it.
func (m *Manager) RecordAccess(key string) {
	h := HashKey(key)
	m.hits[m.Ring.ShardStartFor(h)]++
}

// WindowStats snapshots and clears the current statistics window.
func (m *Manager) WindowStats() []ShardStat {
	shards := m.Ring.Shards()
	stats := make([]ShardStat, 0, len(shards))
	for _, s := range shards {
		stats = append(stats, ShardStat{
			ShardID: s.Start,
			Node:    s.Node,
			Hits:    m.hits[s.Start],
		})
	}
	m.hits = make(map[uint32]uint64)
	return stats
}

// ActiveMigrations returns the in-flight migrations.
func (m *Manager) ActiveMigrations() []*Migration { return m.migrations }

// nodeLoads aggregates the window stats per node.
func nodeLoads(stats []ShardStat) map[string]uint64 {
	loads := make(map[string]uint64)
	for _, s := range stats {
		loads[s.Node] += s.Hits
	}
	return loads
}

// leastLoadedNodes returns the nodes (excluding exclude) sorted by ascending
// window load, ties broken by name for determinism.
func leastLoadedNodes(nodes []string, loads map[string]uint64, exclude string) []string {
	cand := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n != exclude {
			cand = append(cand, n)
		}
	}
	sort.Slice(cand, func(i, j int) bool {
		if loads[cand[i]] != loads[cand[j]] {
			return loads[cand[i]] < loads[cand[j]]
		}
		return cand[i] < cand[j]
	})
	return cand
}

// CheckHotspots inspects the current window, splits hot shards and returns
// the split events. epoch is used for cooldown bookkeeping.
func (m *Manager) CheckHotspots(epoch int) []SplitEvent {
	stats := m.WindowStats()
	hot := m.Detector.Detect(stats)
	if len(hot) == 0 {
		return nil
	}
	hotSet := make(map[uint32]bool, len(hot))
	for _, id := range hot {
		hotSet[id] = true
	}
	loads := nodeLoads(stats)
	nodes := m.Ring.Nodes()

	var events []SplitEvent
	for _, s := range m.Ring.Shards() {
		if !hotSet[s.Start] {
			continue
		}
		if epoch < m.cooldown[s.Start] {
			continue
		}
		if m.MaxActive > 0 && len(m.migrations) >= m.MaxActive {
			break
		}
		if m.shardMigrating(s) {
			continue
		}
		// More parts for hotter shards: 2 parts normally, up to MaxParts
		// when the shard is more than 2x the hot threshold.
		var hits uint64
		for _, st := range stats {
			if st.ShardID == s.Start {
				hits = st.Hits
			}
		}
		parts := 2
		if m.MaxParts >= 3 && float64(hits) > 2*m.Detector.Factor*meanOf(stats) {
			parts = 3
		}
		targets := leastLoadedNodes(nodes, loads, s.Node)
		if len(targets) == 0 {
			continue
		}
		ev := m.splitShard(s, parts, targets)
		if ev == nil {
			continue
		}
		m.cooldown[s.Start] = epoch + m.Cooldown
		events = append(events, *ev)
	}
	return events
}

func meanOf(stats []ShardStat) float64 {
	var total uint64
	for _, s := range stats {
		total += s.Hits
	}
	if len(stats) == 0 {
		return 0
	}
	return float64(total) / float64(len(stats))
}

// shardMigrating reports whether any active migration overlaps the shard.
func (m *Manager) shardMigrating(s Shard) bool {
	for _, mg := range m.migrations {
		if rangesOverlap(s.StartU(), s.EndU(), mg.LoU, mg.HiU) {
			return true
		}
	}
	return false
}

// splitShard splits shard s into `parts` contiguous parts. The first part
// stays on the current node; each remaining part becomes a migration to one
// of the target nodes (least loaded first, wrapping if needed).
func (m *Manager) splitShard(s Shard, parts int, targets []string) *SplitEvent {
	if parts < 2 {
		return nil
	}
	lo, hi := s.StartU(), s.EndU()
	width := hi - lo
	if width < uint64(parts) {
		return nil // arc too small to split further
	}
	ev := &SplitEvent{ShardID: s.Start, From: s.Node}
	for p := 1; p < parts; p++ {
		partLo := lo + width*uint64(p)/uint64(parts)
		partHi := lo + width*uint64(p+1)/uint64(parts)
		to := targets[(p-1)%len(targets)]
		mg := &Migration{
			ID:      m.nextMigID,
			ShardID: s.Start,
			LoU:     partLo,
			HiU:     partHi,
			From:    s.Node,
			To:      to,
			CursorU: partLo,
		}
		m.nextMigID++
		m.migrations = append(m.migrations, mg)
		ev.Migrations = append(ev.Migrations, mg)
	}
	return ev
}

// AdvanceMigrations moves every active migration cursor forward by
// stepFraction of its remaining range and commits the finished ones.
// Committing inserts the split point into the ring so subsequent lookups go
// straight to the new owner. It returns the migrations that completed.
func (m *Manager) AdvanceMigrations(stepFraction float64) []*Migration {
	var done []*Migration
	kept := m.migrations[:0]
	for _, mg := range m.migrations {
		remaining := mg.HiU - mg.CursorU
		step := uint64(float64(mg.HiU-mg.LoU) * stepFraction)
		if step == 0 {
			step = 1
		}
		if step > remaining {
			step = remaining
		}
		if mg.advance(step) {
			m.Ring.InsertPoint(uint32(mg.LoU%HashSpace), mg.To)
			done = append(done, mg)
		} else {
			kept = append(kept, mg)
		}
	}
	m.migrations = kept
	return done
}
