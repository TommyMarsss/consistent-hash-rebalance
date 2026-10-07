package chash

import (
	"fmt"
	"math/rand"
)

// NumBuckets is the number of fixed hash-space buckets used for heatmap data.
const NumBuckets = 256

// SimConfig configures a simulation run.
type SimConfig struct {
	Seed             int64
	NumNodes         int
	VnodesPerNode    int
	NumKeys          int
	Epochs           int
	BaseRequests     int // uniform requests per epoch
	HotspotStart     int // first epoch with injected hotspot traffic
	HotspotEnd       int // last epoch with injected hotspot traffic
	HotspotRequests  int // extra requests per epoch aimed at the victim shard
	AddNodeEpoch     int // epoch at which a node is added (0 = never)
	RemoveNodeEpoch  int // epoch at which a node is removed (0 = never)
	MigrationStep    float64
	Detector         HotspotDetector
	CooldownEpochs   int
	MaxParts         int
	ConsistencyCheck int // number of keys sampled per epoch for invariant checks
}

// DefaultSimConfig returns the configuration used by the demo binary.
func DefaultSimConfig() SimConfig {
	return SimConfig{
		Seed:             42,
		NumNodes:         4,
		VnodesPerNode:    100,
		NumKeys:          30000,
		Epochs:           28,
		BaseRequests:     4000,
		HotspotStart:     4,
		HotspotEnd:       14,
		HotspotRequests:  3000,
		AddNodeEpoch:     18,
		RemoveNodeEpoch:  24,
		MigrationStep:    0.25,
		Detector:         HotspotDetector{Factor: 3.0, MinHits: 200},
		CooldownEpochs:   2,
		MaxParts:         3,
		ConsistencyCheck: 4000,
	}
}

// EventJSON is one reportable event.
type EventJSON struct {
	Epoch int    `json:"epoch"`
	Type  string `json:"type"`
	Text  string `json:"text"`
}

// MigrationJSON describes one migration for the report.
type MigrationJSON struct {
	ID         int    `json:"id"`
	Lo         uint64 `json:"lo"`
	Hi         uint64 `json:"hi"`
	From       string `json:"from"`
	To         string `json:"to"`
	StartEpoch int    `json:"startEpoch"`
	EndEpoch   int    `json:"endEpoch"` // 0 while still running
}

// ShardJSON describes one final ring arc.
type ShardJSON struct {
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
	Node  string `json:"node"`
}

// RatioCheck records a measured key-migration ratio vs its expectation.
type RatioCheck struct {
	Label        string  `json:"label"`
	Expected     float64 `json:"expected"`     // ideal 1/N under balanced ownership
	Measured     float64 `json:"measured"`     // actual fraction of keys that moved
	OnlyAffected bool    `json:"onlyAffected"` // every moved key involved the added/removed node
	Moved        int     `json:"moved"`
	Total        int     `json:"total"`
}

// SimReport is everything the HTML report needs.
type SimReport struct {
	Nodes            []string        `json:"nodes"`
	FinalShards      []ShardJSON     `json:"finalShards"`
	BucketHits       [][]uint64      `json:"bucketHits"` // [epoch][bucket]
	Events           []EventJSON     `json:"events"`
	Migrations       []MigrationJSON `json:"migrations"`
	RatioChecks      []RatioCheck    `json:"ratioChecks"`
	HotFactor        float64         `json:"hotFactor"`
	HotMinHits       uint64          `json:"hotMinHits"`
	TotalRequests    uint64          `json:"totalRequests"`
	ConsistencyError int             `json:"consistencyErrors"`
	Epochs           int             `json:"epochs"`
}

// RunSim executes the simulation and returns the report data. It panics on
// no invariant violations; violations are counted in the report instead.
func RunSim(cfg SimConfig) *SimReport {
	rng := rand.New(rand.NewSource(cfg.Seed))
	ring := NewRing(cfg.VnodesPerNode)
	for i := 0; i < cfg.NumNodes; i++ {
		ring.AddNode(fmt.Sprintf("node-%d", i))
	}
	mgr := NewManager(ring, cfg.Detector)
	mgr.Cooldown = cfg.CooldownEpochs
	mgr.MaxParts = cfg.MaxParts

	keys := make([]string, cfg.NumKeys)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%06d", i)
	}

	// Victim shard: the arc containing a fixed probe hash. All keys landing
	// in it form the hotspot key set.
	victimHash := HashKey("hotspot-victim-probe")
	victimStart := ring.ShardStartFor(victimHash)
	var hotKeys []string
	for _, k := range keys {
		if ring.ShardStartFor(HashKey(k)) == victimStart {
			hotKeys = append(hotKeys, k)
		}
	}

	rep := &SimReport{
		Nodes:      ring.Nodes(),
		HotFactor:  cfg.Detector.Factor,
		HotMinHits: cfg.Detector.MinHits,
		Epochs:     cfg.Epochs,
	}
	migStart := map[int]int{} // migration ID -> start epoch

	// routeSnapshot captures the current routing of every key.
	routeSnapshot := func() map[string]string {
		snap := make(map[string]string, len(keys))
		for _, k := range keys {
			snap[k] = mgr.Route(k)
		}
		return snap
	}

	checkConsistency := func(epoch int) {
		// Sample keys: routing must be deterministic and, for keys inside a
		// migrating range, must agree with the cursor rule and only ever name
		// From or To.
		for i := 0; i < cfg.ConsistencyCheck; i++ {
			k := keys[rng.Intn(len(keys))]
			h := HashKey(k)
			r1, r2 := mgr.Route(k), mgr.Route(k)
			if r1 != r2 || r1 == "" {
				rep.ConsistencyError++
				continue
			}
			for _, mg := range mgr.ActiveMigrations() {
				if mg.Contains(h) {
					want := mg.From
					if unwrap(h, mg.LoU%HashSpace) < mg.CursorU {
						want = mg.To
					}
					if r1 != want {
						rep.ConsistencyError++
					}
				}
			}
		}
	}

	for epoch := 1; epoch <= cfg.Epochs; epoch++ {
		// --- membership changes ---
		if epoch == cfg.AddNodeEpoch {
			before := routeSnapshot()
			newNode := fmt.Sprintf("node-%d", cfg.NumNodes)
			ring.AddNode(newNode)
			cfg.NumNodes++
			moved := 0
			onlyAffected := true
			for _, k := range keys {
				if after := mgr.Route(k); after != before[k] {
					moved++
					if after != newNode {
						onlyAffected = false
					}
				}
			}
			n := len(ring.Nodes())
			rc := RatioCheck{
				Label:        fmt.Sprintf("add %s (epoch %d)", newNode, epoch),
				Expected:     1.0 / float64(n),
				Measured:     float64(moved) / float64(len(keys)),
				OnlyAffected: onlyAffected,
				Moved:        moved,
				Total:        len(keys),
			}
			rep.RatioChecks = append(rep.RatioChecks, rc)
			rep.Events = append(rep.Events, EventJSON{epoch, "node_add",
				fmt.Sprintf("加入节点 %s：迁移 %d/%d key（%.2f%%，理论 %.2f%%）",
					newNode, moved, len(keys), rc.Measured*100, rc.Expected*100)})
			rep.Nodes = ring.Nodes()
		}
		if epoch == cfg.RemoveNodeEpoch {
			victim := ring.Nodes()[1]
			before := routeSnapshot()
			ring.RemoveNode(victim)
			moved := 0
			onlyAffected := true
			for _, k := range keys {
				if mgr.Route(k) != before[k] {
					moved++
					if before[k] != victim {
						onlyAffected = false
					}
				}
			}
			n := len(ring.Nodes()) + 1
			rc := RatioCheck{
				Label:        fmt.Sprintf("remove %s (epoch %d)", victim, epoch),
				Expected:     1.0 / float64(n),
				Measured:     float64(moved) / float64(len(keys)),
				OnlyAffected: onlyAffected,
				Moved:        moved,
				Total:        len(keys),
			}
			rep.RatioChecks = append(rep.RatioChecks, rc)
			rep.Events = append(rep.Events, EventJSON{epoch, "node_remove",
				fmt.Sprintf("移除节点 %s：迁移 %d/%d key（%.2f%%，理论 %.2f%%）",
					victim, moved, len(keys), rc.Measured*100, rc.Expected*100)})
			rep.Nodes = ring.Nodes()
		}

		// --- request stream ---
		bucketHits := make([]uint64, NumBuckets)
		serve := func(k string) {
			mgr.RecordAccess(k)
			bucketHits[HashKey(k)>>(32-8)]++
			rep.TotalRequests++
		}
		for i := 0; i < cfg.BaseRequests; i++ {
			serve(keys[rng.Intn(len(keys))])
		}
		if epoch >= cfg.HotspotStart && epoch <= cfg.HotspotEnd && len(hotKeys) > 0 {
			for i := 0; i < cfg.HotspotRequests; i++ {
				serve(hotKeys[rng.Intn(len(hotKeys))])
			}
		}
		rep.BucketHits = append(rep.BucketHits, bucketHits)

		// --- hotspot detection & splits ---
		for _, ev := range mgr.CheckHotspots(epoch) {
			for _, mg := range ev.Migrations {
				migStart[mg.ID] = epoch
				rep.Migrations = append(rep.Migrations, MigrationJSON{
					ID: mg.ID, Lo: mg.LoU, Hi: mg.HiU,
					From: mg.From, To: mg.To, StartEpoch: epoch,
				})
				rep.Events = append(rep.Events, EventJSON{epoch, "split_start",
					fmt.Sprintf("热点分片 %08x 拆分：区间 [%08x, %08x) 从 %s 迁往 %s",
						ev.ShardID, uint32(mg.LoU%HashSpace), uint32(mg.HiU%HashSpace), mg.From, mg.To)})
			}
		}

		// --- migration progress ---
		for _, mg := range mgr.AdvanceMigrations(cfg.MigrationStep) {
			for i := range rep.Migrations {
				if rep.Migrations[i].ID == mg.ID {
					rep.Migrations[i].EndEpoch = epoch
				}
			}
			rep.Events = append(rep.Events, EventJSON{epoch, "split_done",
				fmt.Sprintf("迁移 #%d 完成：[%08x, %08x) 现由 %s 服务",
					mg.ID, uint32(mg.LoU%HashSpace), uint32(mg.HiU%HashSpace), mg.To)})
		}

		checkConsistency(epoch)
	}

	for _, s := range ring.Shards() {
		rep.FinalShards = append(rep.FinalShards, ShardJSON{Start: s.Start, End: s.End, Node: s.Node})
	}
	return rep
}
