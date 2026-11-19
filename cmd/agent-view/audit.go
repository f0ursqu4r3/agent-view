package main

import (
	"fmt"
	"io"
	"time"

	"agent-view/internal/audit"
	"agent-view/internal/cli"
)

// runAudit checks the hub on 127.0.0.1:<port> and prints the report: counts
// only. It returns 0, or 1 after it prints the error.
func runAudit(args cli.Args, stdout, stderr io.Writer) int {
	body, err := audit.Fetch(args.Port)
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: audit: %v\n", err)
		return 1
	}
	report, err := audit.Check(body, time.Now().UnixMilli())
	if err != nil {
		fmt.Fprintf(stderr, "agent-view: audit: %v\n", err)
		return 1
	}
	stdout.Write(report.JSON())
	return 0
}
