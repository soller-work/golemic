package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/preflight"
	"golemic/internal/repo"
	"golemic/internal/slice"
)

// runSlice implements `golemic slice --issue N`: prints the authoritative task
// specification for issue N to stdout. See internal/slice for the extraction
// contract (comment JSON → inline JSON → prose fallback).
func runSlice(args []string, stdout, stderr io.Writer, executor preflight.Executor) int {
	fs := flag.NewFlagSet("slice", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var issueNum int
	fs.IntVar(&issueNum, "issue", 0, "Issue number (required)")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}
	if issueNum <= 0 {
		fmt.Fprintln(stderr, "missing required flag: --issue") //nolint:errcheck
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err) //nolint:errcheck
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get working directory: %v\n", err) //nolint:errcheck
		return 1
	}
	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "config load error: %v\n", err) //nolint:errcheck
		return 1
	}
	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "config load error: %v\n", err) //nolint:errcheck
		return 1
	}
	creds, err := credentials.NewLoader(homeDir).Load(cfg.Project)
	if err != nil {
		fmt.Fprintf(stderr, "dev-bot token unavailable: %v\n", err) //nolint:errcheck
		return 1
	}

	out, err := slice.Extract(executor, repoRoot, creds.DevToken(), issueNum)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err) //nolint:errcheck
		return 1
	}
	fmt.Fprintln(stdout, out) //nolint:errcheck
	return 0
}
