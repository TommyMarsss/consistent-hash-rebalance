package main

// MigrationEvent records one executed rebalancing action for the report.
type MigrationEvent struct {
	Seq       int      `json:"seq"`
	Epoch     int      `json:"epoch"`
	Reason    string   `json:"reason"`
	OldShard  string   `json:"oldShard"`
	OldNode   string   `json:"oldNode"`
	NewShards []string `json:"newShards"`
	Targets   []string `json:"targets"` // target node per new shard
	KeysMoved int      `json:"keysMoved"`
	Ratio     float64  `json:"ratio"` // hot shard count / mean at detection time
}

// Migrator executes split plans against a ShardManager. Every plan runs
// in two stages:
//
//	Stage 1: replace the old shard by the sub-shards, all still owned by
//	         the old node. Routing is unchanged; the table always covers
//	         the full key space.
//	Stage 2: for each sub-shard whose final owner differs, "copy" the
//	         data, then atomically flip that sub-shard's owner.
//
// Because each table version is published atomically and always satisfies
// the coverage invariant, a routing query at any instant sees exactly one
// owner per key: the old owner before the flip, the new owner after.
type Migrator struct {
	sm     *ShardManager
	events []MigrationEvent
	// OnStep, if set, is called after every published table version with
	// the number of steps published so far. Tests use it to hammer the
	// router while a migration is in flight.
	OnStep func(published int)
	// CopyKeys, if set, simulates the data-copy phase for a sub-shard and
	// returns the number of keys copied (used for reporting).
	CopyKeys func(s *Shard) int
}

// NewMigrator builds a Migrator over sm.
func NewMigrator(sm *ShardManager) *Migrator { return &Migrator{sm: sm} }

// Events returns the recorded migration events.
func (m *Migrator) Events() []MigrationEvent { return m.events }

// ExecuteSplit runs one split plan to completion. epoch and reason are
// recorded on the event; ratio is the detector's count/mean at trigger.
func (m *Migrator) ExecuteSplit(plan *splitPlan, epoch int, reason string, ratio float64) (*MigrationEvent, error) {
	old, ok := m.sm.shardByID(plan.oldID)
	if !ok {
		return nil, nil // shard already gone (e.g. re-split); nothing to do
	}
	oldNode := old.NodeID

	// Stage 1: install sub-shards, routing unchanged.
	if err := m.sm.applySplitStage1(plan); err != nil {
		return nil, err
	}
	if m.OnStep != nil {
		m.OnStep(1)
	}

	// Stage 2: move each sub-shard to its final owner.
	ev := &MigrationEvent{
		Epoch:    epoch,
		Reason:   reason,
		OldShard: plan.oldID,
		OldNode:  oldNode,
		Ratio:    ratio,
	}
	published := 1
	for i, sub := range plan.subs {
		ev.NewShards = append(ev.NewShards, sub.ID)
		ev.Targets = append(ev.Targets, sub.NodeID)
		if sub.NodeID == oldNode {
			continue // piece stays put: no copy, no flip
		}
		// Copy phase: data is transferred while routing still points at
		// the old owner, so readers never observe a gap.
		if m.CopyKeys != nil {
			staged := *sub
			staged.NodeID = oldNode
			ev.KeysMoved += m.CopyKeys(&staged)
		}
		// Commit phase: one atomic publish flips this sub-shard's owner.
		if err := m.sm.applyMove(sub.ID, sub.NodeID); err != nil {
			return nil, err
		}
		published++
		if m.OnStep != nil {
			m.OnStep(published)
		}
		_ = i
	}
	ev.Seq = len(m.events)
	m.events = append(m.events, *ev)
	return ev, nil
}
