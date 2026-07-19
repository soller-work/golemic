package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sliceFixture writes a minimal golemic-managed repo and dev credentials to a
// pair of temp dirs, and returns them plus a HOME snapshot the caller must
// restore via t.Cleanup.
func sliceFixture(t *testing.T, project, devToken string) (homeDir, repoRoot string) {
	t.Helper()
	homeDir = t.TempDir()
	repoRoot = t.TempDir()

	cfgDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{"project":%q,"verify_command":"go test"}`, project)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	credsDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatal(err)
	}
	credsJSON := fmt.Sprintf(`{"dev_token":%q,"reviewer_token":"ghp_reviewer_test"}`, devToken)
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"), []byte(credsJSON), 0600); err != nil {
		t.Fatal(err)
	}

	orig := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", orig) })

	// Clear ambient dev/reviewer tokens so credentials.json is authoritative.
	for _, k := range []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"} {
		orig := os.Getenv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() { _ = os.Setenv(k, orig) })
	}

	return homeDir, repoRoot
}

// sliceExec returns a fakeExecutor that satisfies repo/config plumbing and
// routes gh issue view / gh api comments to the given handlers.
func sliceExec(repoRoot, viewResp, commentsResp string) fakeExecutor { //nolint:cyclop // test routing dispatcher; branches are inherent
	handleGh := func(env map[string]string, args ...string) (string, error) {
		if env["GH_TOKEN"] == "" {
			return "", fmt.Errorf("GH_TOKEN not injected")
		}
		if len(args) >= 3 && args[0] == "issue" && args[1] == "view" {
			return viewResp, nil
		}
		if len(args) >= 2 && args[0] == "api" && strings.Contains(args[1], "comments") {
			if commentsResp == "" {
				return "", fmt.Errorf("unexpected comments call")
			}
			return commentsResp, nil
		}
		return "", fmt.Errorf("unexpected gh call: %v", args)
	}
	return fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" {
				return handleGh(env, args...)
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

// AC: --issue is required and must be a positive integer.
func TestRunSlice_MissingIssueFlag(t *testing.T) {
	homeDir, _ := sliceFixture(t, "p", "tok")
	_ = homeDir
	var stdout, stderr bytes.Buffer

	code := runSlice([]string{"golemic", "slice"}, &stdout, &stderr, fakeExecutor{})
	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--issue") {
		t.Errorf("stderr should mention --issue, got: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on error, got: %q", stdout.String())
	}
}

func TestRunSlice_ZeroIssueFlag(t *testing.T) {
	homeDir, _ := sliceFixture(t, "p", "tok")
	_ = homeDir
	var stdout, stderr bytes.Buffer

	code := runSlice([]string{"golemic", "slice", "--issue", "0"}, &stdout, &stderr, fakeExecutor{})
	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--issue") {
		t.Errorf("stderr should mention --issue, got: %q", stderr.String())
	}
}

// AC: comment marker path prints extracted JSON to stdout.
func TestRunSlice_CommentJSON(t *testing.T) {
	_, repoRoot := sliceFixture(t, "p", "ghp_dev_123")

	const marker = "<!-- golemic:slice-json v=1 -->"
	const sliceJSON = `{"schema_version":"1.1.0","title":"t"}`
	body := "**Summary**\n\n" + marker + "\n_see comment_"
	viewResp := fmt.Sprintf(`{"body":%q}`, body)
	commentBody := marker + "\n\n```json\n" + sliceJSON + "\n```"
	commentsResp := fmt.Sprintf(`[{"id":1,"body":%q}]`, commentBody)

	exec := sliceExec(repoRoot, viewResp, commentsResp)
	var stdout, stderr bytes.Buffer
	code := runSlice([]string{"golemic", "slice", "--issue", "42"}, &stdout, &stderr, exec)

	if code != 0 {
		t.Fatalf("exit: got %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != sliceJSON {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), sliceJSON)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on success, got: %q", stderr.String())
	}
}

// AC: prose-only body is returned verbatim (hand-written issue support).
func TestRunSlice_ProseFallback(t *testing.T) {
	_, repoRoot := sliceFixture(t, "p", "ghp_dev_123")

	body := "Please implement foo."
	viewResp := fmt.Sprintf(`{"body":%q}`, body)
	exec := sliceExec(repoRoot, viewResp, "")

	var stdout, stderr bytes.Buffer
	code := runSlice([]string{"golemic", "slice", "--issue", "42"}, &stdout, &stderr, exec)

	if code != 0 {
		t.Fatalf("exit: got %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != body {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), body)
	}
}

// AC: gh failure surfaces on stderr with non-zero exit.
func TestRunSlice_GHFailure(t *testing.T) {
	_, repoRoot := sliceFixture(t, "p", "ghp_dev_123")

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" {
				return "", fmt.Errorf("gh boom")
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
	var stdout, stderr bytes.Buffer
	code := runSlice([]string{"golemic", "slice", "--issue", "42"}, &stdout, &stderr, exec)

	if code != 1 {
		t.Errorf("exit: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "gh") {
		t.Errorf("stderr should surface gh error, got: %q", stderr.String())
	}
}

// AC: missing credentials file returns a clean error, no stdout output.
func TestRunSlice_MissingCredentials(t *testing.T) { //nolint:cyclop // sequential env setup + assertions; splitting hurts readability
	homeDir := t.TempDir()
	repoRoot := t.TempDir()

	cfgDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"),
		[]byte(`{"project":"p","verify_command":"go test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })
	for _, k := range []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"} {
		orig := os.Getenv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() { _ = os.Setenv(k, orig) })
	}

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	code := runSlice([]string{"golemic", "slice", "--issue", "42"}, &stdout, &stderr, exec)
	if code != 1 {
		t.Errorf("exit: got %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "dev-bot token unavailable") {
		t.Errorf("stderr should mention missing dev-bot token, got: %q", stderr.String())
	}
}
