package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

var cbmAllowedSubs = []string{
	"search_graph", "trace_call_path", "query_graph",
	"get_architecture", "get_graph_schema", "get_code_snippet",
	"search_code", "detect_changes",
}

// cbmCommandFactory creates exec.Cmd instances; override in tests.
var cbmCommandFactory = exec.Command

// runCBM dispatches `golemic cbm <sub> [args…]` to `npx -y codebase-memory-mcp@0.9.0 cli <sub> [args…]`.
// Unknown subcommands are rejected without invoking npx (BR-C3).
func runCBM(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		fmt.Fprintf(stderr, "Usage: golemic cbm <sub> [args…]\nAllowed subcommands: %s\n", strings.Join(cbmAllowedSubs, ", "))
		return 1
	}
	sub := args[2]
	for _, allowed := range cbmAllowedSubs {
		if sub == allowed {
			return execCBMSub(sub, args[3:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "Unknown cbm subcommand %q. Allowed: %s\n", sub, strings.Join(cbmAllowedSubs, ", "))
	return 1
}

func execCBMSub(sub string, extraArgs []string, stdout, stderr io.Writer) int {
	npxArgs := append([]string{"-y", "codebase-memory-mcp@0.9.0", "cli", sub}, extraArgs...)
	cmd := cbmCommandFactory("npx", npxArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "golemic cbm: %v\n", err)
		return 1
	}
	return 0
}
