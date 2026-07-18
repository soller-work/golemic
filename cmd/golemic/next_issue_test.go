package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/selector"
)

// nextIssueFixture creates a temp homeDir and repoRoot with minimal config and credentials.
// Returns homeDir, repoRoot, and a cleanup function.
func nextIssueFixture(t *testing.T, project, devToken string) (homeDir, repoRoot string) {
	t.Helper()
	homeDir = t.TempDir()
	repoRoot = t.TempDir()

	// .golemic/config.json
	cfgDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{"project":%q,"verify_command":"go test"}`, project)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// ~/.golemic/<project>/credentials.json
	credsDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatal(err)
	}
	credsJSON := fmt.Sprintf(`{"dev_token":%q,"reviewer_token":"ghp_reviewer_test"}`, devToken)
	credsPath := filepath.Join(credsDir, "credentials.json")
	if err := os.WriteFile(credsPath, []byte(credsJSON), 0600); err != nil {
		t.Fatal(err)
	}

	return homeDir, repoRoot
}

// nextIssueRun calls runNextIssue with a fakeExecutor that handles the standard
// infrastructure calls (git rev-parse, git config remote) and delegates the
// gh api graphql call to the provided graphqlFunc.
func nextIssueRun( //nolint:cyclop
	t *testing.T,
	homeDir, repoRoot string,
	graphqlFunc func(env map[string]string, args []string) (string, error),
) (int, string, string) {
	t.Helper()

	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// Unset bot-token env vars so the credentials file is the authoritative source.
	for _, k := range []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"} {
		orig := os.Getenv(k)
		_ = os.Unsetenv(k)
		defer func(key, val string) { _ = os.Setenv(key, val) }(k, orig)
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "config" {
				return "https://github.com/testowner/testrepo.git\n", nil
			}
			if name == "gh" && len(args) > 0 && args[0] == "api" {
				if graphqlFunc != nil {
					return graphqlFunc(env, args)
				}
				return "", fmt.Errorf("graphqlFunc not set")
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
	// Also handle RunInDir for git config (called via executor.RunInDir)
	exec.runFunc = func(name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return repoRoot + "\n", nil
		}
		if name == "git" && len(args) > 0 && args[0] == "config" {
			return "https://github.com/testowner/testrepo.git\n", nil
		}
		return "", fmt.Errorf("not mocked: %s %v", name, args)
	}

	var stdout, stderr bytes.Buffer
	code := runNextIssue([]string{"golemic", "next-issue"}, &stdout, &stderr, exec)
	return code, stdout.String(), stderr.String()
}

// buildGraphQLResponse builds a fixture GraphQL response with the given issues.
func buildGraphQLResponse(issues []map[string]interface{}) string {
	nodes, _ := json.Marshal(issues)
	return fmt.Sprintf(`{"data":{"repository":{"issues":{"nodes":%s}}}}`, string(nodes))
}

func issueNode(number int, title string, labels []string, blockedBy, closingPRs int) map[string]interface{} {
	labelNodes := make([]map[string]string, len(labels))
	for i, l := range labels {
		labelNodes[i] = map[string]string{"name": l}
	}
	prNodes := make([]map[string]string, closingPRs)
	for i := range prNodes {
		prNodes[i] = map[string]string{"state": "OPEN"}
	}
	return map[string]interface{}{
		"number":                         number,
		"title":                          title,
		"url":                            fmt.Sprintf("https://github.com/testowner/testrepo/issues/%d", number),
		"labels":                         map[string]interface{}{"nodes": labelNodes},
		"trackedIssues":                  map[string]int{"totalCount": blockedBy},
		"closedByPullRequestsReferences": map[string]interface{}{"nodes": prNodes},
	}
}

// AC-001: Happy path — single takeable non-bug issue.
func TestNextIssue_AC001_HappyPath(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	fixture := buildGraphQLResponse([]map[string]interface{}{
		issueNode(42, "Fix something", []string{"ready-for-agent"}, 0, 0),
	})

	code, stdout, stderr := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return fixture, nil
	})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty, got: %q", stderr)
	}

	var issue selector.Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &issue); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout=%q", err, stdout)
	}
	if issue.Number != 42 {
		t.Errorf("number: got %d, want 42", issue.Number)
	}
	if !contains(issue.Labels, "ready-for-agent") {
		t.Errorf("labels should contain 'ready-for-agent', got %v", issue.Labels)
	}
}

// AC-002: Bug label wins over lower issue number.
func TestNextIssue_AC002_BugWins(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	fixture := buildGraphQLResponse([]map[string]interface{}{
		issueNode(10, "Regular issue", []string{"ready-for-agent"}, 0, 0),
		issueNode(50, "Bug issue", []string{"ready-for-agent", "bug"}, 0, 0),
	})

	code, stdout, _ := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return fixture, nil
	})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	var issue selector.Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &issue); err != nil {
		t.Fatalf("parse stdout: %v", err)
	}
	if issue.Number != 50 {
		t.Errorf("expected #50 (bug), got #%d", issue.Number)
	}
}

// AC-003: Two bug issues — lower number wins.
func TestNextIssue_AC003_BugTiebreaker(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	fixture := buildGraphQLResponse([]map[string]interface{}{
		issueNode(23, "Bug B", []string{"ready-for-agent", "bug"}, 0, 0),
		issueNode(17, "Bug A", []string{"ready-for-agent", "bug"}, 0, 0),
	})

	code, stdout, _ := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return fixture, nil
	})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	var issue selector.Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &issue); err != nil {
		t.Fatalf("parse stdout: %v", err)
	}
	if issue.Number != 17 {
		t.Errorf("expected #17, got #%d", issue.Number)
	}
}

// AC-004: Empty result — no takeable issues.
func TestNextIssue_AC004_Empty(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	fixture := buildGraphQLResponse([]map[string]interface{}{})

	code, stdout, stderr := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return fixture, nil
	})

	if code != 2 {
		t.Fatalf("exit code: got %d, want 2", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty, got: %q", stdout)
	}
	if !strings.Contains(stderr, "no takeable issue") {
		t.Errorf("stderr should contain 'no takeable issue', got: %q", stderr)
	}
}

// AC-005: Issue with in-progress label is excluded.
func TestNextIssue_AC005_InProgressExcluded(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	fixture := buildGraphQLResponse([]map[string]interface{}{
		issueNode(7, "In progress", []string{"ready-for-agent", "in-progress"}, 0, 0),
		issueNode(9, "Takeable", []string{"ready-for-agent"}, 0, 0),
	})

	code, stdout, _ := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return fixture, nil
	})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	var issue selector.Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &issue); err != nil {
		t.Fatalf("parse stdout: %v", err)
	}
	if issue.Number != 9 {
		t.Errorf("expected #9, got #%d", issue.Number)
	}
}

// AC-006: Issue with blocked_by is excluded.
func TestNextIssue_AC006_BlockedExcluded(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	fixture := buildGraphQLResponse([]map[string]interface{}{
		issueNode(12, "Blocked", []string{"ready-for-agent"}, 1, 0),
		issueNode(20, "Free", []string{"ready-for-agent"}, 0, 0),
	})

	code, stdout, _ := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return fixture, nil
	})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	var issue selector.Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &issue); err != nil {
		t.Fatalf("parse stdout: %v", err)
	}
	if issue.Number != 20 {
		t.Errorf("expected #20, got #%d", issue.Number)
	}
}

// AC-007: Issue with open closing PR is excluded.
func TestNextIssue_AC007_ClosingPRExcluded(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	fixture := buildGraphQLResponse([]map[string]interface{}{
		issueNode(15, "Has PR", []string{"ready-for-agent"}, 0, 1),
		issueNode(22, "No PR", []string{"ready-for-agent"}, 0, 0),
	})

	code, stdout, _ := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return fixture, nil
	})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	var issue selector.Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &issue); err != nil {
		t.Fatalf("parse stdout: %v", err)
	}
	if issue.Number != 22 {
		t.Errorf("expected #22, got #%d", issue.Number)
	}
}

// AC-008: gh API error propagates to exit 1.
func TestNextIssue_AC008_GhError(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	code, stdout, stderr := nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, _ []string) (string, error) {
		return "", fmt.Errorf("gh api graphql failed: HTTP 500")
	})

	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty, got: %q", stdout)
	}
	if !strings.Contains(stderr, "gh api graphql failed") {
		t.Errorf("stderr should contain 'gh api graphql failed', got: %q", stderr)
	}
}

// AC-009: Dev-bot token is injected via GH_TOKEN.
func TestNextIssue_AC009_TokenInjected(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_xxx")

	var capturedToken string
	fixture := buildGraphQLResponse([]map[string]interface{}{
		issueNode(1, "Issue", []string{"ready-for-agent"}, 0, 0),
	})

	code, _, _ := nextIssueRun(t, homeDir, repoRoot, func(env map[string]string, _ []string) (string, error) {
		capturedToken = env["GH_TOKEN"]
		return fixture, nil
	})

	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if capturedToken != "ghp_dev_xxx" {
		t.Errorf("GH_TOKEN: got %q, want %q", capturedToken, "ghp_dev_xxx")
	}
}

// AC-010: Missing credentials file fails cleanly.
func TestNextIssue_AC010_MissingCredentials(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	// Remove the credentials file.
	credsPath := filepath.Join(homeDir, ".golemic", "testproject", "credentials.json")
	if err := os.Remove(credsPath); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := nextIssueRun(t, homeDir, repoRoot, nil)

	if code != 1 {
		t.Fatalf("exit code: got %d, want 1", code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty, got: %q", stdout)
	}
	if !strings.HasPrefix(stderr, "dev-bot token unavailable:") {
		t.Errorf("stderr should start with 'dev-bot token unavailable:', got: %q", stderr)
	}
}

// AC-011: GraphQL query contains first: 50.
func TestNextIssue_AC011_FetchSizeCap(t *testing.T) {
	homeDir, repoRoot := nextIssueFixture(t, "testproject", "ghp_dev_test")

	var capturedArgs []string
	fixture := buildGraphQLResponse([]map[string]interface{}{})

	nextIssueRun(t, homeDir, repoRoot, func(_ map[string]string, args []string) (string, error) {
		capturedArgs = args
		return fixture, nil
	})

	queryStr := ""
	for _, arg := range capturedArgs {
		if strings.HasPrefix(arg, "query=") {
			queryStr = strings.TrimPrefix(arg, "query=")
		}
	}
	if !strings.Contains(queryStr, "first: 50") {
		t.Errorf("GraphQL query should contain 'first: 50', got: %q", queryStr)
	}
	if strings.Contains(queryStr, "after:") || strings.Contains(queryStr, "cursor") {
		t.Errorf("GraphQL query must not contain pagination cursor, got: %q", queryStr)
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
