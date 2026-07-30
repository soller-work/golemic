// Command report parses go test -json output and files deduplicated e2e-failure
// issues in the golemic repository for each failed scenario.
//
// Usage:
//
//	report <go-test-json-file>
//
// Required env:
//
//	GOLEMIC_TICKET_TOKEN  GitHub token with issues:write on the target repo
//
// Optional env:
//
//	GOLEMIC_TARGET_REPO   owner/repo to file issues in (default: soller-work/golemic)
//	GITHUB_SERVER_URL     for the workflow run link (set automatically by GHA)
//	GITHUB_REPOSITORY     for the workflow run link (set automatically by GHA)
//	GITHUB_RUN_ID         for the workflow run link (set automatically by GHA)
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	defaultTargetRepo = "soller-work/golemic"
	// Keep in sync with internal/runner.maxCILogBytes.
	maxLogBytes = 8000
)

func main() {
	code, err := runMain(os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	if code != 0 {
		os.Exit(code)
	}
}

func runMain(args []string, out io.Writer) (int, error) {
	if len(args) != 1 {
		return 2, fmt.Errorf("usage: report <go-test-json-file>")
	}
	inputPath := args[0]

	token := os.Getenv("GOLEMIC_TICKET_TOKEN")
	if token == "" {
		return 2, fmt.Errorf("GOLEMIC_TICKET_TOKEN environment variable is required")
	}

	repo := os.Getenv("GOLEMIC_TARGET_REPO")
	if repo == "" {
		repo = defaultTargetRepo
	}

	f, err := os.Open(inputPath)
	if err != nil {
		return 2, fmt.Errorf("open %s: %w", inputPath, err)
	}
	defer func() { _ = f.Close() }()

	scenarios, err := parseFailures(f)
	if err != nil {
		return 1, fmt.Errorf("parse failures: %w", err)
	}

	client := newGitHubClient(token, repo)
	runLink := buildRunLink()
	secrets := collectSecrets()

	if err := report(scenarios, client, runLink, secrets, out); err != nil {
		return 1, err
	}
	return 0, nil
}

// report deduplicates failures against open e2e-failure issues and creates
// one new issue per unseen fingerprint. It exits 0 even when issues are filed;
// only API-level errors cause a non-zero return.
func report(scenarios []failedScenario, client issueClient, runLink string, secrets []string, out io.Writer) error {
	existing, err := client.listFingerprints()
	if err != nil {
		return fmt.Errorf("list existing issues: %w", err)
	}

	created, suppressed := 0, 0
	for _, s := range scenarios {
		if existing[s.Fingerprint] {
			_, _ = fmt.Fprintf(out, "suppressed (dup): %s [%s] fp=%s\n", s.Name, s.Class, s.Fingerprint)
			suppressed++
			continue
		}
		body := buildIssueBody(s, runLink, secrets)
		title := fmt.Sprintf("[E2E] %s failed (%s)", s.Name, s.Class)
		if err := client.createIssue(title, body); err != nil {
			return fmt.Errorf("create issue for %s: %w", s.Name, err)
		}
		_, _ = fmt.Fprintf(out, "created: %s [%s] fp=%s\n", s.Name, s.Class, s.Fingerprint)
		created++
	}

	_, _ = fmt.Fprintf(out, "summary: %d failed scenario(s), %d ticket(s) created, %d suppressed by dedup\n",
		len(scenarios), created, suppressed)
	return nil
}

func buildIssueBody(s failedScenario, runLink string, secrets []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scenario `%s` failed with class `%s` in the nightly E2E suite.\n\n", s.Name, s.Class)
	fmt.Fprintf(&sb, "%s%s\n\n", fingerprintPrefix, s.Fingerprint)
	if runLink != "" {
		fmt.Fprintf(&sb, "**Workflow run:** %s\n\n", runLink)
	}
	sb.WriteString("**Logs:**\n```\n")
	logs := strings.Join(s.Output, "")
	logs = trimLog(logs, maxLogBytes)
	logs = redactSecrets(logs, secrets)
	sb.WriteString(logs)
	sb.WriteString("\n```\n")
	return sb.String()
}

func trimLog(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return "...[trimmed]\n" + s[len(s)-maxBytes:]
}

func redactSecrets(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	return s
}

func collectSecrets() []string {
	return []string{
		os.Getenv("GOLEMIC_TICKET_TOKEN"),
		os.Getenv("GOLEMIC_DEV_TOKEN"),
		os.Getenv("GOLEMIC_REVIEWER_TOKEN"),
	}
}

func buildRunLink() string {
	serverURL := os.Getenv("GITHUB_SERVER_URL")
	if serverURL == "" {
		serverURL = "https://github.com"
	}
	ghRepo := os.Getenv("GITHUB_REPOSITORY")
	runID := os.Getenv("GITHUB_RUN_ID")
	if ghRepo == "" || runID == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%s", serverURL, ghRepo, runID)
}
