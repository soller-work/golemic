package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/preflight"
	"golemic/internal/repo"
	"golemic/internal/selector"
)

// runNextIssue implements the `golemic next-issue` subcommand.
// Exit 0 + JSON stdout on hit; exit 2 + empty stdout on no takeable issue; exit 1 on error.
func runNextIssue(_ []string, stdout, stderr io.Writer, executor preflight.Executor) int { //nolint:cyclop
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "dev-bot token unavailable: failed to get home directory: %v\n", err) //nolint:errcheck
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "config load error: failed to get working directory: %v\n", err) //nolint:errcheck
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

	remoteURLOut, err := executor.RunInDir(repoRoot, "git", "config", "--get", "remote.origin.url")
	if err != nil {
		fmt.Fprintf(stderr, "config load error: failed to read remote.origin.url: %v\n", err) //nolint:errcheck
		return 1
	}

	repoSlug, err := parseGitHubSlug(remoteURLOut)
	if err != nil {
		fmt.Fprintf(stderr, "config load error: %v\n", err) //nolint:errcheck
		return 1
	}

	issue, err := selector.NextIssue(executor, repoSlug, creds.DevToken())
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) {
			fmt.Fprintf(stderr, "%v\n", err) //nolint:errcheck
		} else {
			fmt.Fprintf(stderr, "%v\n", err) //nolint:errcheck
		}
		return 1
	}

	if issue == nil {
		fmt.Fprintln(stderr, "no takeable issue") //nolint:errcheck
		return 2
	}

	data, err := json.Marshal(issue)
	if err != nil {
		fmt.Fprintf(stderr, "gh api graphql failed: marshal error: %v\n", err) //nolint:errcheck
		return 1
	}
	fmt.Fprintln(stdout, string(data)) //nolint:errcheck
	return 0
}

// parseGitHubSlug extracts "owner/repo" from a GitHub HTTPS remote URL.
func parseGitHubSlug(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimSuffix(rawURL, ".git")
	rawURL = strings.TrimRight(rawURL, "/")

	const prefix = "https://github.com/"
	if !strings.HasPrefix(rawURL, prefix) {
		return "", fmt.Errorf("remote origin URL is not a github.com HTTPS URL: %q", rawURL)
	}
	slug := strings.TrimPrefix(rawURL, prefix)
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("cannot parse owner/repo from remote origin URL: %q", rawURL)
	}
	return parts[0] + "/" + parts[1], nil
}
