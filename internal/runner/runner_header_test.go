package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/config"
)

// buildHeaderRunner creates a Runner with all fields set as they would be after
// a successful issue load (PS-004), using the given homeDir.
func buildHeaderRunner(t *testing.T, homeDir string) *Runner {
	t.Helper()
	project := "hdr-project"
	runID := "issue-7-20260717T120000Z"
	r := &Runner{
		homeDir:    homeDir,
		project:    project,
		runID:      runID,
		branchName: "golemic/issue-7",
		issueNum:   7,
		cfg: &config.Config{
			Models:         config.Models{Dev: "model-dev", Reviewer: "model-rev"},
			TimeoutMinutes: 45,
		},
		issue: &issueData{Number: 7, Title: "Add header feature"},
	}
	return r
}

// TestWriteRunHeader_AllFieldsPresent covers AC-001: all RM-001 fields appear on stderr.
func TestWriteRunHeader_AllFieldsPresent(t *testing.T) {
	homeDir := t.TempDir()
	r := buildHeaderRunner(t, homeDir)

	var buf bytes.Buffer
	r.writeRunHeader(&buf)
	out := buf.String()

	checks := []struct {
		label string
		want  string
	}{
		{"run ID", r.runID},
		{"issue number", "#7"},
		{"issue title", "Add header feature"},
		{"project", "hdr-project"},
		{"dev model", "model-dev"},
		{"reviewer model", "model-rev"},
		{"branch", "golemic/issue-7"},
		{"timeout", "45m0s"},
		{"event log", filepath.Join(homeDir, ".golemic", "hdr-project", "runs", r.runID, "events.jsonl")},
		{"dev stdout log", filepath.Join(homeDir, ".golemic", "hdr-project", "runs", r.runID, "dev.stdout.log")},
		{"dev stderr log", filepath.Join(homeDir, ".golemic", "hdr-project", "runs", r.runID, "dev.stderr.log")},
		{"reviewer stdout log", filepath.Join(homeDir, ".golemic", "hdr-project", "runs", r.runID, "reviewer.stdout.log")},
		{"reviewer stderr log", filepath.Join(homeDir, ".golemic", "hdr-project", "runs", r.runID, "reviewer.stderr.log")},
		{"dev worktree", filepath.Join(homeDir, ".golemic", "hdr-project", "worktrees", "issue-7")},
		{"reviewer worktree", filepath.Join(homeDir, ".golemic", "hdr-project", "worktrees", "issue-7-review")},
		{"tail tip", fmt.Sprintf("tail -f %s", filepath.Join(homeDir, ".golemic", "hdr-project", "runs", r.runID, "events.jsonl"))},
	}

	for _, c := range checks {
		if !strings.Contains(out, c.want) {
			t.Errorf("header missing %s (%q); full output:\n%s", c.label, c.want, out)
		}
	}
}

// TestWriteRunHeader_AbsolutePaths verifies BR-004: all paths are absolute.
func TestWriteRunHeader_AbsolutePaths(t *testing.T) {
	homeDir := t.TempDir()
	r := buildHeaderRunner(t, homeDir)

	var buf bytes.Buffer
	r.writeRunHeader(&buf)
	out := buf.String()

	// Every token that looks like an absolute-path candidate must actually be absolute.
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			// Only check tokens that begin with the path separator (i.e. look like absolute paths).
			if strings.HasPrefix(field, string(os.PathSeparator)) && !filepath.IsAbs(field) {
				t.Errorf("non-absolute path in header line %q: %q", line, field)
			}
		}
	}
}

// TestWriteRunHeader_NoANSI verifies BR-004: no ANSI escape sequences.
func TestWriteRunHeader_NoANSI(t *testing.T) {
	homeDir := t.TempDir()
	r := buildHeaderRunner(t, homeDir)

	var buf bytes.Buffer
	r.writeRunHeader(&buf)
	out := buf.String()

	if strings.Contains(out, "\x1b[") {
		t.Errorf("header contains ANSI escape sequences")
	}
}

// TestWriteRunHeader_TrailingBlankLine verifies BR-004: block ends with a trailing blank line.
func TestWriteRunHeader_TrailingBlankLine(t *testing.T) {
	homeDir := t.TempDir()
	r := buildHeaderRunner(t, homeDir)

	var buf bytes.Buffer
	r.writeRunHeader(&buf)
	out := buf.String()

	if !strings.HasSuffix(out, "\n\n") {
		t.Errorf("header does not end with a trailing blank line; ends with: %q", out[max(0, len(out)-10):])
	}
}

// TestWriteRunHeader_TimeoutSeconds verifies that cfg.TimeoutSeconds takes precedence.
func TestWriteRunHeader_TimeoutSeconds(t *testing.T) {
	homeDir := t.TempDir()
	r := buildHeaderRunner(t, homeDir)
	r.cfg.TimeoutSeconds = 90
	r.cfg.TimeoutMinutes = 30

	var buf bytes.Buffer
	r.writeRunHeader(&buf)
	out := buf.String()

	want := (90 * time.Second).String()
	if !strings.Contains(out, want) {
		t.Errorf("expected timeout %q in header, got:\n%s", want, out)
	}
}

// TestRun_HeaderOnStderr_AC001 covers AC-001 at the Run() level: header reaches
// stderr after a successful issue load (collision aborts so no real git ops needed).
func TestRun_HeaderOnStderr_AC001(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	// Pre-create worktree dir to trigger a collision abort (AC-003 scenario).
	project := "test-project"
	worktreeDir := filepath.Join(homeDir, ".golemic", project, "worktrees", "issue-42")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()
	if exitCode != 1 {
		t.Fatalf("expected exit 1 (collision), got %d", exitCode)
	}

	errOut := stderr.String()
	if !strings.Contains(errOut, "Run ID:") {
		t.Errorf("header missing 'Run ID:' in stderr:\n%s", errOut)
	}
	if !strings.Contains(errOut, "Issue:") {
		t.Errorf("header missing 'Issue:' in stderr:\n%s", errOut)
	}
	if !strings.Contains(errOut, "events.jsonl") {
		t.Errorf("header missing event log path in stderr:\n%s", errOut)
	}
	// Collision message should also be present (AC-003)
	if !strings.Contains(errOut, "Worktree exists at") {
		t.Errorf("collision message missing from stderr:\n%s", errOut)
	}
}

// TestRun_HeaderNotOnStdout_AC002 covers AC-002: stdout must not contain header content.
func TestRun_HeaderNotOnStdout_AC002(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	// Use a collision to get a quick abort without full orchestration.
	project := "test-project"
	worktreeDir := filepath.Join(homeDir, ".golemic", project, "worktrees", "issue-42")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	r.Run()

	stdoutStr := stdout.String()
	for _, label := range []string{"Run ID:", "Issue:", "Event log:", "Dev logs:", "Reviewer logs:", "Dev worktree:", "Rev worktree:"} {
		if strings.Contains(stdoutStr, label) {
			t.Errorf("header label %q found in stdout; stdout: %q", label, stdoutStr)
		}
	}
}

// TestRun_NoHeaderOnFailureBeforeIssueLoad_AC004 covers AC-004: no header when
// failure occurs before issue load (config not found).
func TestRun_NoHeaderOnFailureBeforeIssueLoad_AC004(t *testing.T) {
	homeDir := t.TempDir()
	repoRoot := t.TempDir()
	// No config.json created — config load will fail.

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) >= 1 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()
	if exitCode != 1 {
		t.Fatalf("expected exit 1, got %d", exitCode)
	}

	errOut := stderr.String()
	for _, label := range []string{"Run ID:", "Issue:", "Event log:", "Dev logs:", "Reviewer logs:"} {
		if strings.Contains(errOut, label) {
			t.Errorf("header label %q found in stderr before issue load; stderr: %q", label, errOut)
		}
	}
	if !strings.Contains(errOut, "Failed to load config") {
		t.Errorf("expected config error in stderr, got: %q", errOut)
	}
}

// setupCollisionRun creates a Runner with a worktree pre-existing to cause a
// collision abort, giving tests a quick exit point without full orchestration.
func setupCollisionRun(t *testing.T, quiet bool) (*bytes.Buffer, *bytes.Buffer, int) {
	t.Helper()
	homeDir, repoRoot, _ := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	project := "test-project"
	worktreeDir := filepath.Join(homeDir, ".golemic", project, "worktrees", "issue-42")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)
	r.SetQuiet(quiet)

	return &stdout, &stderr, r.Run()
}

// TestRun_QuietSuppressesHeader_AC001 covers AC-001: --quiet suppresses the header.
func TestRun_QuietSuppressesHeader_AC001(t *testing.T) {
	_, stderr, exitCode := setupCollisionRun(t, true)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 (collision), got %d", exitCode)
	}
	errOut := stderr.String()
	for _, label := range []string{"Run ID:", "Issue:", "Event log:", "Dev logs:", "Reviewer logs:"} {
		if strings.Contains(errOut, label) {
			t.Errorf("header label %q found in stderr under --quiet; stderr: %q", label, errOut)
		}
	}
	// Collision message must still appear (BR-003)
	if !strings.Contains(errOut, "Worktree exists at") {
		t.Errorf("collision message missing from stderr under --quiet; stderr: %q", errOut)
	}
}

// TestRun_NoQuietRendersHeader_AC002 covers AC-002: without --quiet the header appears.
func TestRun_NoQuietRendersHeader_AC002(t *testing.T) {
	_, stderr, exitCode := setupCollisionRun(t, false)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 (collision), got %d", exitCode)
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "Run ID:") {
		t.Errorf("header missing 'Run ID:' in stderr without --quiet:\n%s", errOut)
	}
}

// TestRun_QuietStdoutUnchanged_AC003 covers AC-003: stdout carries only the runID line.
func TestRun_QuietStdoutUnchanged_AC003(t *testing.T) {
	stdout, _, exitCode := setupCollisionRun(t, true)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 (collision), got %d", exitCode)
	}
	stdoutStr := stdout.String()
	if !strings.HasPrefix(stdoutStr, "runs/") {
		t.Errorf("expected stdout to start with 'runs/'; got: %q", stdoutStr)
	}
	for _, label := range []string{"Run ID:", "Issue:", "Event log:"} {
		if strings.Contains(stdoutStr, label) {
			t.Errorf("header label %q found in stdout under --quiet", label)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
