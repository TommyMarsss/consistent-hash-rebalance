// Command sim runs the hotspot-aware consistent-hashing simulation and
// writes a self-contained static HTML report.
package main

import (
	"flag"
	"fmt"
	"log"

	chash "github.com/TommyMarsss/consistent-hash-rebalance"
)

func main() {
	out := flag.String("out", "report.html", "output HTML report path")
	flag.Parse()

	cfg := chash.DefaultSimConfig()
	rep := chash.RunSim(cfg)

	if err := chash.WriteHTMLReport(*out, rep); err != nil {
		log.Fatalf("write report: %v", err)
	}

	fmt.Printf("report written to %s\n", *out)
	fmt.Printf("  nodes=%d shards=%d requests=%d migrations=%d consistencyErrors=%d\n",
		len(rep.Nodes), len(rep.FinalShards), rep.TotalRequests,
		len(rep.Migrations), rep.ConsistencyError)
	for _, rc := range rep.RatioChecks {
		fmt.Printf("  %-28s moved %6d/%d  measured %.2f%%  expected %.2f%%\n",
			rc.Label, rc.Moved, rc.Total, rc.Measured*100, rc.Expected*100)
	}
}
