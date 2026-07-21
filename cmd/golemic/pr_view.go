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
)

// runPRView executes the pr-view subcommand:
// golemic pr-view --pr <n>
// It resolves the host repo, loads config and reviewer credentials, then
// invokes gh pr view and prints the PR context to stdout.
func runPRView(args []string, stdout, stderr io.Writer, executor preflight.Executor, loadConfig func(string) (*config.Config, error)) int {
	fs := flag.NewFlagSet("pr-view", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var prFlag int
	fs.IntVar(&prFlag, "pr", 0, "PR number (required)")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}
	if prFlag <= 0 {
		fmt.Fprintln(stderr, "--pr must be a positive integer")
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get current directory: %v\n", err)
		return 1
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "failed to resolve host repo: %v\n", err)
		return 1
	}

	cfg, err := loadConfig(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to load config: %v\n", err)
		return 1
	}

	creds, err := credentials.NewLoader(homeDir).Load(cfg.Project)
	if err != nil {
		fmt.Fprintf(stderr, "reviewer-bot token unavailable: %v\n", err)
		return 1
	}

	out, err := executor.RunWithEnvInDir(map[string]string{"GH_TOKEN": creds.ReviewerToken()}, repoRoot,
		"gh", "pr", "view", fmt.Sprintf("%d", prFlag))
	if err != nil {
		fmt.Fprintf(stderr, "Failed to view PR: %s\n", formatGHError(err))
		return 1
	}

	_, _ = fmt.Fprint(stdout, out)
	return 0
}
