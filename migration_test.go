package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// During a split+move, every routing query for any key must return
// exactly one node, and that node must always be a legitimate owner of
// the key at that moment: the old owner before the commit, the final
// owner after. No key may ever flap or route to a third party.
func TestRoutingConsistencyDuringMigration(t *testing.T) {
	ring := buildRing(4, 100)
	sm, err := NewShardManager(ring, 32)
	if err != nil {
		t.Fatal(err)
	}
	target := sm.Shards()[7]
	oldNode := target.NodeID
	targets := []string{"n1", "n2", "n3"}
	plan, err := sm.planSplit(target.ID, 3, targets)
	if err != nil {
		t.Fatal(err)
	}

	// Probe keys that all live inside the shard being split.
	var probe []string
	for i := 0; len(probe) < 300; i++ {
		k := fmt.Sprintf("probe-%d", i)
		if target.contains(hashKey(k)) {
			probe = append(probe, k)
		}
	}

	mig := NewMigrator(sm)
	mig.OnStep = func(published int) {
		// slow the migration down so readers observe intermediate
		// table versions, and check the invariant on every version
		if err := validateTable(sm.snapshot()); err != nil {
			t.Errorf("table invalid after %d publishes: %v", published, err)
		}
		time.Sleep(3 * time.Millisecond)
	}

	// Concurrent readers record the owner sequence of every probe key.
	// Keys are partitioned across workers so each observed slice has a
	// single writer.
	observed := make([][]string, len(probe))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for i := w; i < len(probe); i += 4 {
					n, _ := sm.Route(probe[i])
					if n == "" {
						t.Errorf("key %q routed to no node during migration", probe[i])
						return
					}
					seq := observed[i]
					if len(seq) == 0 || seq[len(seq)-1] != n {
						observed[i] = append(seq, n)
					}
				}
			}
		}(w)
	}

	if _, err := mig.ExecuteSplit(plan, 0, "test", 1); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()

	// Final owner of each probe key (after the migration completes).
	final := make(map[string]string, len(probe))
	for _, k := range probe {
		n, _ := sm.Route(k)
		final[k] = n
	}

	allowed := map[string]bool{oldNode: true}
	for _, tn := range targets {
		allowed[tn] = true
	}
	for i, k := range probe {
		seq := observed[i]
		if len(seq) == 0 {
			t.Fatalf("key %q was never observed", k)
		}
		transitions := 0
		for j, n := range seq {
			if !allowed[n] {
				t.Fatalf("key %q routed to stranger node %q (seq %v)", k, n, seq)
			}
			if j > 0 && seq[j-1] != n {
				transitions++
				// the only legal transition: old owner -> final owner
				if seq[j-1] != oldNode || n != final[k] {
					t.Fatalf("key %q illegal transition %s -> %s (seq %v, final %s)",
						k, seq[j-1], n, seq, final[k])
				}
			}
		}
		if transitions > 1 {
			t.Fatalf("key %q flapped %d times (seq %v)", k, transitions, seq)
		}
	}
}

// After a split completes, each sub-shard must be owned by its planned
// target, and routing into the old shard's range must hit the targets.
func TestMigrationCompletesOwnership(t *testing.T) {
	ring := buildRing(4, 100)
	sm, err := NewShardManager(ring, 32)
	if err != nil {
		t.Fatal(err)
	}
	target := sm.Shards()[3]
	targets := []string{"n0", "n1", "n2"}
	plan, err := sm.planSplit(target.ID, 3, targets)
	if err != nil {
		t.Fatal(err)
	}
	mig := NewMigrator(sm)
	ev, err := mig.ExecuteSplit(plan, 0, "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ev == nil {
		t.Fatal("no event recorded")
	}
	for i, sub := range plan.subs {
		s, ok := sm.shardByID(sub.ID)
		if !ok {
			t.Fatalf("sub-shard %s missing after migration", sub.ID)
		}
		if s.NodeID != targets[i] {
			t.Fatalf("sub-shard %s owned by %s, want %s", sub.ID, s.NodeID, targets[i])
		}
	}
	// old shard ID must be gone
	if _, ok := sm.shardByID(target.ID); ok {
		t.Fatalf("old shard %s still present", target.ID)
	}
	// a key inside sub i must route to targets[i]
	for i, sub := range plan.subs {
		mid := sub.Start + uint32(sub.size()/2)
		n, sid := sm.RouteHash(mid)
		if n != targets[i] || sid != sub.ID {
			t.Fatalf("midpoint of %s routed to (%s,%s), want (%s,%s)",
				sub.ID, n, sid, targets[i], sub.ID)
		}
	}
}

// A key that never enters the migrated range must keep its owner
// throughout — migration must not disturb unrelated keys.
func TestMigrationDoesNotDisturbUnrelatedKeys(t *testing.T) {
	ring := buildRing(4, 100)
	sm, err := NewShardManager(ring, 32)
	if err != nil {
		t.Fatal(err)
	}
	target := sm.Shards()[9]
	plan, err := sm.planSplit(target.ID, 2, []string{"n1", "n2"})
	if err != nil {
		t.Fatal(err)
	}
	keys := makeKeys(20_000)
	before := map[string]string{}
	for _, k := range keys {
		if target.contains(hashKey(k)) {
			continue
		}
		n, _ := sm.Route(k)
		before[k] = n
	}
	mig := NewMigrator(sm)
	if _, err := mig.ExecuteSplit(plan, 0, "test", 1); err != nil {
		t.Fatal(err)
	}
	for k, n := range before {
		after, _ := sm.Route(k)
		if after != n {
			t.Fatalf("unrelated key %q moved %s -> %s", k, n, after)
		}
	}
}
