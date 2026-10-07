package chash

// Shard is one contiguous arc of the hash ring owned by a single node.
// The arc is [Start, End) in unwrapped coordinates; use EndU because End
// may be numerically smaller than Start for the arc that wraps around.
type Shard struct {
	Start uint32 // inclusive arc start; also the shard's stable ID
	End   uint32 // raw end (next point's hash); may be <= Start (wrap)
	Node  string
}

// StartU is the arc start in unwrapped coordinates.
func (s Shard) StartU() uint64 { return uint64(s.Start) }

// EndU is the arc end (exclusive) in unwrapped coordinates, in (Start, Start+2^32].
func (s Shard) EndU() uint64 {
	e := uint64(s.End)
	if e <= uint64(s.Start) {
		e += HashSpace
	}
	return e
}

// Width returns the arc width in hash units.
func (s Shard) Width() uint64 { return s.EndU() - s.StartU() }

// unwrap maps a raw hash into the unwrapped coordinate system anchored at lo:
// any h < lo is shifted up by one full hash space.
func unwrap(h uint32, lo uint64) uint64 {
	hu := uint64(h)
	if hu < lo {
		hu += HashSpace
	}
	return hu
}

// Migration tracks the incremental move of one sub-range [LoU, HiU) of the
// hash space from node From to node To.
//
// Routing consistency during migration: keys with unwrap(hash) < CursorU
// have already been copied and are served by To; keys >= CursorU are still
// served by From. Every key therefore has exactly one owner at all times,
// and the cursor only moves forward, so a key flips owner at most once.
type Migration struct {
	ID      int
	ShardID uint32 // start of the original shard that was split
	LoU     uint64 // unwrapped inclusive lower bound of the moving range
	HiU     uint64 // unwrapped exclusive upper bound of the moving range
	From    string
	To      string
	CursorU uint64 // everything in [LoU, CursorU) is already at To
}

// Contains reports whether raw hash h falls inside the migrating range.
func (m *Migration) Contains(h uint32) bool {
	hu := unwrap(h, m.LoU%HashSpace)
	return hu >= m.LoU && hu < m.HiU
}

// RouteHash returns the node that currently owns raw hash h, which must be
// inside the migrating range.
func (m *Migration) RouteHash(h uint32) string {
	if unwrap(h, m.LoU%HashSpace) < m.CursorU {
		return m.To
	}
	return m.From
}

// Progress returns the migrated fraction of the range in [0, 1].
func (m *Migration) Progress() float64 {
	if m.HiU <= m.LoU {
		return 1
	}
	p := float64(m.CursorU-m.LoU) / float64(m.HiU-m.LoU)
	if p > 1 {
		p = 1
	}
	return p
}

// advance moves the cursor forward by delta hash units. It reports whether
// the migration is complete after the move.
func (m *Migration) advance(delta uint64) bool {
	m.CursorU += delta
	if m.CursorU >= m.HiU {
		m.CursorU = m.HiU
		return true
	}
	return false
}

// rangesOverlap reports whether unwrapped ranges [aLo,aHi) and [bLo,bHi)
// overlap, allowing either to be shifted by whole hash spaces.
func rangesOverlap(aLo, aHi, bLo, bHi uint64) bool {
	al, ah := int64(aLo), int64(aHi)
	bl, bh := int64(bLo), int64(bHi)
	hs := int64(HashSpace)
	for _, d := range []int64{-hs, 0, hs} {
		if al < bh+d && bl+d < ah {
			return true
		}
	}
	return false
}
