package main

import (
	"encoding/json"
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
func runNextIssue(_ []string, stdout, stderr io.Writer, executor preflight.Executor) int {
	repoSlug, devToken, ok := resolveNextIssueContext(executor, stderr)
	if !ok {
		return 1
	}

	issue, err := selector.NextIssue(executor, repoSlug, devToken)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	if issue == nil {
		fmt.Fprintln(stderr, "no takeable issue")
		return 2
	}

	data, err := json.Marshal(issue)
	if err != nil {
		fmt.Fprintf(stderr, "gh api graphql failed: marshal error: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}

// resolveNextIssueContext loads home dir, repo root, config, credentials, and remote
// repo slug needed by runNextIssue. Returns the repo slug and dev token on success.
func resolveNextIssueContext(executor preflight.Executor, stderr io.Writer) (repoSlug, devToken string, ok bool) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "dev-bot token unavailable: failed to get home directory: %v\n", err)
		return "", "", false
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "config load error: failed to get working directory: %v\n", err)
		return "", "", false
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "config load error: %v\n", err)
		return "", "", false
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "config load error: %v\n", err)
		return "", "", false
	}

	creds, err := credentials.NewLoader(homeDir).Load(cfg.Project)
	if err != nil {
		fmt.Fprintf(stderr, "dev-bot token unavailable: %v\n", err)
		return "", "", false
	}

	remoteURLOut, err := executor.RunInDir(repoRoot, "git", "config", "--get", "remote.origin.url")
	if err != nil {
		fmt.Fprintf(stderr, "config load error: failed to read remote.origin.url: %v\n", err)
		return "", "", false
	}

	slug, err := parseGitHubSlug(remoteURLOut)
	if err != nil {
		fmt.Fprintf(stderr, "config load error: %v\n", err)
		return "", "", false
	}

	return slug, creds.DevToken(), true
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
