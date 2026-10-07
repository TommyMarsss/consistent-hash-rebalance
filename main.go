package main

import (
	"fmt"
	"os"
)

func main() {
	out := "report.html"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	rep, err := runSim(defaultSimConfig())
	if err != nil {
		fmt.Fprintln(os.Stderr, "simulation failed:", err)
		os.Exit(1)
	}
	if err := writeReportHTML(rep, out); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		os.Exit(1)
	}
	fmt.Printf("report written to %s\n", out)
	fmt.Printf("  nodes=%d shards=%d requests=%d events=%d\n",
		len(rep.Nodes), len(rep.Shards), rep.Requests, len(rep.Events))
	for _, c := range rep.RatioChecks {
		fmt.Printf("  %-14s expected %.2f%%  actual %.2f%%  unrelated=%d\n",
			c.Event, c.Expected*100, c.Actual*100, c.UnrelatedMoves)
	}
}
