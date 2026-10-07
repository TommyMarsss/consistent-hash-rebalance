package main

import (
	"sort"
	"sync"
)

// hashKey is FNV-1a 32-bit with a murmur3-style finalizer (fmix32).
// Plain FNV-1a diffuses poorly for the short, similar strings we hash
// (node IDs, sequential keys), which clumps virtual nodes and skews
// migration ratios; the finalizer gives full avalanche at almost no
// cost. The ring space is [0, 2^32).
func hashKey(s string) uint32 {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

// vnode is one virtual node on the ring.
type vnode struct {
	hash uint32
	node string
}

// Ring is a consistent hash ring with virtual nodes.
// It is safe for concurrent use.
type Ring struct {
	mu            sync.RWMutex
	vnodesPerNode int
	points        []vnode // sorted by hash
	nodes         map[string]struct{}
}

// NewRing creates a ring where each physical node owns vnodesPerNode
// virtual nodes.
func NewRing(vnodesPerNode int) *Ring {
	if vnodesPerNode <= 0 {
		vnodesPerNode = 150
	}
	return &Ring{
		vnodesPerNode: vnodesPerNode,
		nodes:         make(map[string]struct{}),
	}
}

// AddNode adds a physical node and its virtual nodes to the ring.
func (r *Ring) AddNode(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[id]; ok {
		return
	}
	r.nodes[id] = struct{}{}
	for i := 0; i < r.vnodesPerNode; i++ {
		h := hashKey(id + "#" + itoa(i))
		r.points = append(r.points, vnode{hash: h, node: id})
	}
	sort.Slice(r.points, func(i, j int) bool {
		if r.points[i].hash == r.points[j].hash {
			return r.points[i].node < r.points[j].node
		}
		return r.points[i].hash < r.points[j].hash
	})
}

// RemoveNode removes a physical node and all of its virtual nodes.
func (r *Ring) RemoveNode(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[id]; !ok {
		return
	}
	delete(r.nodes, id)
	kept := r.points[:0]
	for _, p := range r.points {
		if p.node != id {
			kept = append(kept, p)
		}
	}
	r.points = kept
}

// GetNode returns the node responsible for key, or "" if the ring is empty.
func (r *Ring) GetNode(key string) string {
	return r.GetNodeByHash(hashKey(key))
}

// GetNodeByHash returns the node owning the arc that contains h:
// the first virtual node clockwise from h.
func (r *Ring) GetNodeByHash(h uint32) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.points) == 0 {
		return ""
	}
	i := sort.Search(len(r.points), func(i int) bool { return r.points[i].hash >= h })
	if i == len(r.points) {
		i = 0
	}
	return r.points[i].node
}

// Nodes returns the sorted list of physical node IDs.
func (r *Ring) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.nodes))
	for n := range r.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// VNodes returns a copy of the virtual node list (sorted by hash).
func (r *Ring) VNodes() []vnode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]vnode, len(r.points))
	copy(out, r.points)
	return out
}

// itoa is a small strconv.Itoa for non-negative ints.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
