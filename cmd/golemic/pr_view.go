package main

import (
	"flag"
	"fmt"
	"io"

	"golemic/internal/preflight"
)

// runPRView implements `golemic pr-view --pr N`: prints the pull request view
// for PR number N to stdout, wrapping the underlying GitHub CLI call.
func runPRView(args []string, stdout, stderr io.Writer, executor preflight.Executor) int {
	fs := flag.NewFlagSet("pr-view", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var prNum int
	fs.IntVar(&prNum, "pr", 0, "PR number (required)")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}
	if prNum <= 0 {
		fmt.Fprintln(stderr, "missing required flag: --pr") //nolint:errcheck
		return 1
	}

	out, err := executor.RunWithEnv(nil, "gh", "pr", "view", fmt.Sprintf("%d", prNum))
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", formatGHError(err)) //nolint:errcheck
		return 1
	}
	fmt.Fprint(stdout, out) //nolint:errcheck
	return 0
}
