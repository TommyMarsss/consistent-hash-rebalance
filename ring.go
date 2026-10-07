// Package chash implements a hotspot-aware consistent hashing sharding
// system: a consistent hash ring with virtual nodes, hotspot detection with
// dynamic shard splitting, and migration-safe routing.
package chash

import (
	"hash/fnv"
	"sort"
	"strconv"
)

// HashSpace is the size of the hash ring: [0, 2^32).
const HashSpace = uint64(1) << 32

// HashKey hashes a key onto the ring. FNV-1a alone disperses short,
// near-identical strings (like "node-1#vnode-37") poorly, so the result is
// passed through a 32-bit bit-mixing finalizer to uniformize positions.
func HashKey(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return fmix32(h.Sum32())
}

// fmix32 is the standard 32-bit avalanche finalizer (as used by MurmurHash3):
// a fixed sequence of xorshift-multiply steps, implemented here directly.
func fmix32(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

// vnode is one virtual node point on the ring.
type vnode struct {
	hash uint32
	node string
}

// Ring is a consistent hash ring with virtual nodes.
//
// The arc owned by point i is [points[i].hash, points[i+1].hash) (wrapping
// around for the last point). A key is owned by the last point whose hash
// is <= hash(key), wrapping to the last point if there is none (the
// "predecessor" convention), so arcs and lookups always agree.
type Ring struct {
	vnodesPerNode int
	points        []vnode // sorted by hash
	nodes         map[string]bool
}

// NewRing creates an empty ring. vnodesPerNode is the number of virtual
// nodes (points) each physical node contributes.
func NewRing(vnodesPerNode int) *Ring {
	return &Ring{
		vnodesPerNode: vnodesPerNode,
		nodes:         make(map[string]bool),
	}
}

// VnodesPerNode returns the configured virtual nodes per physical node.
func (r *Ring) VnodesPerNode() int { return r.vnodesPerNode }

// Nodes returns the sorted list of physical node names.
func (r *Ring) Nodes() []string {
	out := make([]string, 0, len(r.nodes))
	for n := range r.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AddNode adds a physical node and its virtual nodes to the ring.
func (r *Ring) AddNode(node string) {
	if r.nodes[node] {
		return
	}
	r.nodes[node] = true
	for i := 0; i < r.vnodesPerNode; i++ {
		r.points = append(r.points, vnode{
			hash: HashKey(node + "#vnode-" + strconv.Itoa(i)),
			node: node,
		})
	}
	r.sortPoints()
}

// RemoveNode removes a physical node and all its virtual nodes.
func (r *Ring) RemoveNode(node string) {
	if !r.nodes[node] {
		return
	}
	delete(r.nodes, node)
	kept := r.points[:0]
	for _, p := range r.points {
		if p.node != node {
			kept = append(kept, p)
		}
	}
	r.points = kept
}

// InsertPoint inserts a single extra point (used when a shard split commits).
func (r *Ring) InsertPoint(hash uint32, node string) {
	r.points = append(r.points, vnode{hash: hash, node: node})
	r.sortPoints()
}

func (r *Ring) sortPoints() {
	sort.Slice(r.points, func(i, j int) bool {
		if r.points[i].hash != r.points[j].hash {
			return r.points[i].hash < r.points[j].hash
		}
		return r.points[i].node < r.points[j].node
	})
}

// ownerIndex returns the index of the point owning hash h: the last point
// with hash <= h, wrapping to the last point when h is before all points.
func (r *Ring) ownerIndex(h uint32) int {
	idx := sort.Search(len(r.points), func(i int) bool {
		return r.points[i].hash > h
	}) - 1
	if idx < 0 {
		idx = len(r.points) - 1 // wrap around
	}
	return idx
}

// GetNode returns the node owning the given key.
func (r *Ring) GetNode(key string) string {
	if len(r.points) == 0 {
		return ""
	}
	return r.points[r.ownerIndex(HashKey(key))].node
}

// GetNodeForHash returns the node owning the given ring position.
func (r *Ring) GetNodeForHash(h uint32) string {
	if len(r.points) == 0 {
		return ""
	}
	return r.points[r.ownerIndex(h)].node
}

// ShardStartFor returns the ring position of the start of the arc that
// contains h. The arc start doubles as the shard's stable identifier.
func (r *Ring) ShardStartFor(h uint32) uint32 {
	return r.points[r.ownerIndex(h)].hash
}

// NumPoints returns the number of points (virtual nodes) on the ring.
func (r *Ring) NumPoints() int { return len(r.points) }

// Shards returns the current arcs of the ring, in ring order. Each arc is a
// shard: [Start, End) in unwrapped coordinates (EndU() may exceed 2^32 for
// the arc that wraps past the end of the hash space).
func (r *Ring) Shards() []Shard {
	n := len(r.points)
	out := make([]Shard, 0, n)
	for i := 0; i < n; i++ {
		end := r.points[(i+1)%n].hash
		out = append(out, Shard{
			Start: r.points[i].hash,
			End:   end,
			Node:  r.points[i].node,
		})
	}
	return out
}
