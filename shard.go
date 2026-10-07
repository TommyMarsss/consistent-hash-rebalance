package main

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// Shard is a contiguous half-open range [Start, End) of the 32-bit hash
// space owned by exactly one node. The full ring space is always covered
// by exactly one set of non-overlapping shards.
type Shard struct {
	ID       string `json:"id"`
	Start    uint32 `json:"start"`
	End      uint32 `json:"end"` // exclusive; End == 0 means "up to 2^32"
	NodeID   string `json:"node"`
	SplitSeq int    `json:"splitSeq"` // how many splits produced this shard
}

// contains reports whether hash h falls in the shard. End == 0 is the
// top of the space (2^32), so the last shard is [Start, 2^32).
func (s *Shard) contains(h uint32) bool {
	if s.End == 0 {
		return h >= s.Start
	}
	return h >= s.Start && h < s.End
}

func (s *Shard) size() uint64 {
	if s.End == 0 {
		return 1<<32 - uint64(s.Start)
	}
	return uint64(s.End) - uint64(s.Start)
}

// shardTable is an immutable routing snapshot. Routing reads never take
// locks: they load the current table pointer atomically. Writers build a
// new table and publish it with a single atomic store, so a reader always
// sees one complete, consistent routing table — never a mix of two.
type shardTable struct {
	shards []*Shard          // sorted by Start, non-overlapping, full coverage
	byID   map[string]*Shard // same shard pointers, indexed by ID
}

// ShardManager owns the shard layout and the atomic table publication.
type ShardManager struct {
	mu    sync.Mutex // serializes layout mutations (splits, moves)
	cur   atomic.Pointer[shardTable]
	seq   int
	ring  *Ring
	parts int // initial shard count (for reference in reports)
}

// NewShardManager partitions the whole hash space into parts equal-size
// shards and assigns each to the node the ring picks for the shard's
// midpoint.
func NewShardManager(ring *Ring, parts int) (*ShardManager, error) {
	if parts <= 0 {
		return nil, errors.New("parts must be positive")
	}
	if len(ring.Nodes()) == 0 {
		return nil, errors.New("ring has no nodes")
	}
	sm := &ShardManager{ring: ring, parts: parts}
	shards := make([]*Shard, 0, parts)
	step := uint64(1) << 32 / uint64(parts)
	for i := 0; i < parts; i++ {
		start := uint32(uint64(i) * step)
		var end uint32
		if i < parts-1 {
			end = uint32(uint64(i+1) * step)
		}
		mid := start
		if end == 0 {
			mid = start + uint32((1<<32-uint64(start))/2)
		} else {
			mid = start + (end-start)/2
		}
		sh := &Shard{
			ID:     fmt.Sprintf("s%d", i),
			Start:  start,
			End:    end,
			NodeID: ring.GetNodeByHash(mid),
		}
		shards = append(shards, sh)
	}
	sm.seq = parts
	t := &shardTable{shards: shards, byID: indexShards(shards)}
	if err := validateTable(t); err != nil {
		return nil, err
	}
	sm.cur.Store(t)
	return sm, nil
}

func indexShards(shards []*Shard) map[string]*Shard {
	m := make(map[string]*Shard, len(shards))
	for _, s := range shards {
		m[s.ID] = s
	}
	return m
}

// validateTable checks the coverage invariant: shards sorted, no gaps,
// no overlaps, together covering [0, 2^32).
func validateTable(t *shardTable) error {
	if len(t.shards) == 0 {
		return errors.New("empty shard table")
	}
	if t.shards[0].Start != 0 {
		return fmt.Errorf("table does not start at 0 (starts at %d)", t.shards[0].Start)
	}
	for i, s := range t.shards {
		if s.End != 0 && s.End <= s.Start {
			return fmt.Errorf("shard %s has empty/invalid range [%d,%d)", s.ID, s.Start, s.End)
		}
		if i > 0 {
			prev := t.shards[i-1]
			if prev.End == 0 {
				return fmt.Errorf("shard %s before %s already reaches top of space", prev.ID, s.ID)
			}
			if prev.End != s.Start {
				return fmt.Errorf("gap/overlap between %s (end %d) and %s (start %d)",
					prev.ID, prev.End, s.ID, s.Start)
			}
		}
	}
	if t.shards[len(t.shards)-1].End != 0 {
		return fmt.Errorf("table does not reach top of space (last end %d)", t.shards[len(t.shards)-1].End)
	}
	return nil
}

// snapshot returns the current immutable routing table.
func (sm *ShardManager) snapshot() *shardTable { return sm.cur.Load() }

// Route maps a key to (nodeID, shardID) using the current committed
// table. It always returns exactly one answer; during a migration the
// answer flips from old owner to new owner exactly once, at publish time.
func (sm *ShardManager) Route(key string) (node, shardID string) {
	return sm.RouteHash(hashKey(key))
}

// RouteHash is Route for a pre-hashed position on the ring.
func (sm *ShardManager) RouteHash(h uint32) (node, shardID string) {
	t := sm.snapshot()
	i := sort.Search(len(t.shards), func(i int) bool {
		s := t.shards[i]
		return s.End == 0 || h < s.End
	})
	s := t.shards[i]
	return s.NodeID, s.ID
}

// Shards returns a copy of the current shard list.
func (sm *ShardManager) Shards() []*Shard {
	t := sm.snapshot()
	out := make([]*Shard, len(t.shards))
	for i, s := range t.shards {
		c := *s
		out[i] = &c
	}
	return out
}

// shardByID looks up a shard in the current table.
func (sm *ShardManager) shardByID(id string) (*Shard, bool) {
	t := sm.snapshot()
	s, ok := t.byID[id]
	return s, ok
}

// splitPlan describes how one shard is divided: the new sub-shards and
// the node each should end up on.
type splitPlan struct {
	oldID string
	subs  []*Shard // NodeID already set to the final owner
}

// planSplit divides shard id into parts contiguous sub-ranges. owners[i]
// is the final owner of sub-range i. It does not modify the live table;
// it returns the plan for the Migrator to execute.
func (sm *ShardManager) planSplit(id string, parts int, owners []string) (*splitPlan, error) {
	if parts < 2 {
		return nil, errors.New("split needs at least 2 parts")
	}
	if len(owners) != parts {
		return nil, errors.New("owners length must equal parts")
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	s, ok := sm.shardByID(id)
	if !ok {
		return nil, fmt.Errorf("shard %s not found", id)
	}
	size := s.size()
	if uint64(parts) > size {
		return nil, fmt.Errorf("shard %s too small to split into %d parts", id, parts)
	}
	subs := make([]*Shard, 0, parts)
	base := uint64(s.Start)
	for i := 0; i < parts; i++ {
		start := uint32(base + size*uint64(i)/uint64(parts))
		var end uint32
		if i == parts-1 {
			end = s.End // keep 0 (top of space) on the last piece
		} else {
			end = uint32(base + size*uint64(i+1)/uint64(parts))
		}
		subs = append(subs, &Shard{
			ID:       fmt.Sprintf("%s.%d", s.ID, sm.seq+i),
			Start:    start,
			End:      end,
			NodeID:   owners[i],
			SplitSeq: s.SplitSeq + 1,
		})
	}
	sm.seq += parts
	return &splitPlan{oldID: s.ID, subs: subs}, nil
}

// publish atomically replaces the routing table. The new table must
// satisfy the coverage invariant; this is the only way the table changes.
func (sm *ShardManager) publish(shards []*Shard) error {
	t := &shardTable{shards: shards, byID: indexShards(shards)}
	if err := validateTable(t); err != nil {
		return err
	}
	sm.cur.Store(t)
	return nil
}

// applySplitStage1 replaces the old shard with the sub-shards, all still
// owned by the old owner, so routing is unchanged. Returns the staged
// table. Must be called under sm.mu via the Migrator.
func (sm *ShardManager) applySplitStage1(plan *splitPlan) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	t := sm.snapshot()
	old, ok := t.byID[plan.oldID]
	if !ok {
		return fmt.Errorf("shard %s no longer exists", plan.oldID)
	}
	staged := make([]*Shard, 0, len(plan.subs))
	for _, sub := range plan.subs {
		c := *sub
		c.NodeID = old.NodeID // still the old owner: routing unchanged
		staged = append(staged, &c)
	}
	return sm.publish(replaceShard(t.shards, old.ID, staged))
}

// applyMove flips the owner of shardID to to. Must be called under sm.mu
// via the Migrator.
func (sm *ShardManager) applyMove(shardID, to string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	t := sm.snapshot()
	target, ok := t.byID[shardID]
	if !ok {
		return fmt.Errorf("shard %s no longer exists", shardID)
	}
	if target.NodeID == to {
		return nil
	}
	moved := &Shard{
		ID:       target.ID,
		Start:    target.Start,
		End:      target.End,
		NodeID:   to,
		SplitSeq: target.SplitSeq,
	}
	return sm.publish(replaceShard(t.shards, shardID, []*Shard{moved}))
}

// replaceShard returns a new sorted shard slice with the shard oldID
// replaced by repl (which must cover exactly the old shard's range).
func replaceShard(shards []*Shard, oldID string, repl []*Shard) []*Shard {
	out := make([]*Shard, 0, len(shards)+len(repl)-1)
	for _, s := range shards {
		if s.ID == oldID {
			out = append(out, repl...)
		} else {
			out = append(out, s)
		}
	}
	return out
}
