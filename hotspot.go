package main

import "sort"

// HotspotDetector decides which shards are "hot" at the end of an
// observation epoch. A shard is hot when its access count in the epoch
// is at least Multiplier times the mean count across all shards AND at
// least MinCount absolute requests (so a quiet system never rebalances).
type HotspotDetector struct {
	Multiplier float64
	MinCount   uint64
}

// NewHotspotDetector with sane defaults: 3x the mean, at least 1000 hits.
func NewHotspotDetector() *HotspotDetector {
	return &HotspotDetector{Multiplier: 3.0, MinCount: 1000}
}

// HotShard is one detection result.
type HotShard struct {
	ShardID string
	Count   uint64
	Ratio   float64 // count / mean
}

// Detect returns the hot shards (hottest first) and the epoch mean.
// counts maps shardID -> accesses observed in the epoch; shards missing
// from the map count as zero.
func (d *HotspotDetector) Detect(shardIDs []string, counts map[string]uint64) ([]HotShard, float64) {
	if len(shardIDs) == 0 {
		return nil, 0
	}
	var total uint64
	for _, id := range shardIDs {
		total += counts[id]
	}
	mean := float64(total) / float64(len(shardIDs))
	threshold := mean * d.Multiplier
	var hot []HotShard
	for _, id := range shardIDs {
		c := counts[id]
		if c < d.MinCount {
			continue
		}
		if float64(c) >= threshold && float64(c) > mean {
			hot = append(hot, HotShard{ShardID: id, Count: c, Ratio: float64(c) / mean})
		}
	}
	sort.Slice(hot, func(i, j int) bool { return hot[i].Count > hot[j].Count })
	return hot, mean
}

// LoadBalancer picks target nodes for split pieces: the least-loaded
// nodes, preferring nodes other than the shard's current owner.
type LoadBalancer struct {
	// nodeLoad maps nodeID -> current load (sum of shard access counts).
	nodeLoad map[string]uint64
	nodes    []string
}

// NewLoadBalancer builds a balancer from all node IDs and the current
// per-shard access counts.
func NewLoadBalancer(nodes []string, shards []*Shard, counts map[string]uint64) *LoadBalancer {
	lb := &LoadBalancer{nodeLoad: make(map[string]uint64, len(nodes)), nodes: append([]string(nil), nodes...)}
	for _, n := range nodes {
		lb.nodeLoad[n] = 0
	}
	for _, s := range shards {
		lb.nodeLoad[s.NodeID] += counts[s.ID]
	}
	return lb
}

// PickTargets returns the n least-loaded nodes, excluding exclude when
// possible. Loads are treated as if each pick adds share load, so
// consecutive picks spread out.
func (lb *LoadBalancer) PickTargets(n int, share uint64, exclude string) []string {
	type cand struct {
		node string
		load uint64
	}
	cands := make([]cand, 0, len(lb.nodes))
	for _, node := range lb.nodes {
		cands = append(cands, cand{node, lb.nodeLoad[node]})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].load == cands[j].load {
			return cands[i].node < cands[j].node
		}
		return cands[i].load < cands[j].load
	})
	out := make([]string, 0, n)
	used := make(map[string]bool)
	for i := 0; i < n; i++ {
		chosen := ""
		for _, c := range cands {
			if c.node == exclude && len(cands) > 1 {
				continue
			}
			if used[c.node] && len(used) < len(cands)-1 {
				continue
			}
			chosen = c.node
			break
		}
		if chosen == "" {
			chosen = cands[0].node
		}
		out = append(out, chosen)
		used[chosen] = true
		// reflect the new load so the next pick accounts for it
		for j := range cands {
			if cands[j].node == chosen {
				cands[j].load += share
			}
		}
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].load == cands[j].load {
				return cands[i].node < cands[j].node
			}
			return cands[i].load < cands[j].load
		})
	}
	return out
}
