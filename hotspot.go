package chash

// HotspotDetector decides which shards are "hot" in a statistics window.
//
// A shard is hot when both hold:
//   - its hits exceed an absolute floor (MinHits), so tiny shards on a quiet
//     system never trigger splits;
//   - its hits exceed Factor times the mean hits per shard, so only shards
//     that are outliers relative to their peers trigger splits.
type HotspotDetector struct {
	Factor  float64 // e.g. 3.0: hot means > 3x the mean
	MinHits uint64  // absolute floor, e.g. 200
}

// ShardStat holds the access statistics of one shard for one window.
type ShardStat struct {
	ShardID uint32 // shard start hash
	Node    string
	Hits    uint64
}

// Detect returns the IDs of the hot shards in this window.
func (d HotspotDetector) Detect(stats []ShardStat) []uint32 {
	if len(stats) == 0 {
		return nil
	}
	var total uint64
	for _, s := range stats {
		total += s.Hits
	}
	mean := float64(total) / float64(len(stats))
	var hot []uint32
	for _, s := range stats {
		if s.Hits >= d.MinHits && float64(s.Hits) > d.Factor*mean {
			hot = append(hot, s.ShardID)
		}
	}
	return hot
}
