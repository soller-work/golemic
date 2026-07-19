package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/eventlog"
)

// ghIssueJSON builds a minimal gh issue view JSON response.
func ghIssueJSON(state string, labelNames ...string) string {
	labels := "[]"
	if len(labelNames) > 0 {
		parts := make([]string, len(labelNames))
		for i, n := range labelNames {
			parts[i] = fmt.Sprintf(`{"name":%q}`, n)
		}
		labels = "[" + strings.Join(parts, ",") + "]"
	}
	return fmt.Sprintf(`{"title":"T","labels":%s,"state":%q}`, labels, state)
}

// buildSkipExec returns a fakeExecutor whose gh issue view returns resp and
// whose git commands simulate a clean repo (no collisions).
func buildSkipExec(repoRoot, resp string) *fakeExecutor {
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			var subcmd string
			if len(args) >= 3 && args[0] == "-C" {
				subcmd = args[2]
			} else if len(args) >= 1 {
				subcmd = args[0]
			}
			switch subcmd {
			case "rev-parse":
				return repoRoot + "\n", nil
			case "branch", "ls-remote":
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 1 && args[0] == "issue" {
				return resp, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

// readLastRunFinishedPayload finds the single run directory under homeDir/.golemic/project/runs
// and returns the payload string of the last run_finished event.
func readLastRunFinishedPayload(t *testing.T, homeDir, project string) string {
	t.Helper()
	runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no run directory found under %s", runsDir)
	}
	logPath := filepath.Join(runsDir, entries[0].Name(), "events.jsonl")
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventRunFinished {
			return string(events[i].Payload)
		}
	}
	t.Fatal("no run_finished event found")
	return ""
}

// ---------------------------------------------------------------------------
// TestLoadIssue_ParsesState — loadIssue populates State from gh response.
// ---------------------------------------------------------------------------

func TestLoadIssue_ParsesState(t *testing.T) {
	for _, tc := range []struct{ state string }{{"OPEN"}, {"CLOSED"}} {
		t.Run(tc.state, func(t *testing.T) {
			homeDir := t.TempDir()
			creds := loadTestCreds(t, homeDir, "parse-state")
			exec := &dirCapturingExecutor{
				runFunc: func(name string, args ...string) (string, error) {
					return ghIssueJSON(tc.state), nil
				},
			}
			r := &Runner{executor: exec, repoRoot: "/fake", issueNum: 1, creds: creds}
			data, err := r.loadIssue()
			if err != nil {
				t.Fatalf("loadIssue: %v", err)
			}
			if data.State != tc.state {
				t.Errorf("State: got %q, want %q", data.State, tc.state)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestRunner_SkipsWhenIssueClosed_WithReadyLabel — AC-002
// ---------------------------------------------------------------------------

func TestRunner_SkipsWhenIssueClosed_WithReadyLabel(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := buildSkipExec(repoRoot, ghIssueJSON("CLOSED", "ready-for-agent"))

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 44)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()

	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}

	errOut := stderr.String()
	if !strings.Contains(errOut, `skipped: issue #44 has state="CLOSED" (expected OPEN)`) {
		t.Errorf("missing skip line in stderr: %q", errOut)
	}
	if !strings.Contains(errOut, `ready-for-agent`) {
		t.Errorf("missing ready-for-agent warning in stderr: %q", errOut)
	}

	payload := readLastRunFinishedPayload(t, homeDir, project)
	if payload != `{"outcome":"skipped"}` {
		t.Errorf("run_finished payload: got %s", payload)
	}

	// Guard must fire before collision check: no git branch/ls-remote calls made.
	for _, c := range exec.calls {
		if c.name == "git" && len(c.args) >= 1 && (c.args[0] == "branch" || c.args[0] == "ls-remote") {
			t.Errorf("collision check ran after skip guard: git %v", c.args)
		}
	}
}

// ---------------------------------------------------------------------------
// TestRunner_SkipsWhenIssueClosed_WithoutReadyLabel — AC-003
// ---------------------------------------------------------------------------

func TestRunner_SkipsWhenIssueClosed_WithoutReadyLabel(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := buildSkipExec(repoRoot, ghIssueJSON("CLOSED"))

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 44)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()

	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}

	errOut := stderr.String()
	if !strings.Contains(errOut, "skipped:") {
		t.Errorf("missing skip line in stderr: %q", errOut)
	}
	if strings.Contains(errOut, "ready-for-agent") {
		t.Errorf("label warning must not appear when label absent, stderr: %q", errOut)
	}

	payload := readLastRunFinishedPayload(t, homeDir, project)
	if payload != `{"outcome":"skipped"}` {
		t.Errorf("run_finished payload: got %s", payload)
	}
}

// ---------------------------------------------------------------------------
// TestRunner_SkipsWhenIssueStateUnknown — AC-004 (fail-closed on empty state)
// ---------------------------------------------------------------------------

func TestRunner_SkipsWhenIssueStateUnknown(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := buildSkipExec(repoRoot, ghIssueJSON(""))

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 7)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()

	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}

	if !strings.Contains(stderr.String(), "skipped:") {
		t.Errorf("missing skip line in stderr: %q", stderr.String())
	}

	payload := readLastRunFinishedPayload(t, homeDir, project)
	if payload != `{"outcome":"skipped"}` {
		t.Errorf("run_finished payload: got %s", payload)
	}
}

// ---------------------------------------------------------------------------
// TestRunner_ProceedsWhenIssueOpen — AC-001 regression guard
// ---------------------------------------------------------------------------

func TestRunner_ProceedsWhenIssueOpen(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	exec := buildSkipExec(repoRoot, ghIssueJSON("OPEN", "ready-for-agent"))

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 5)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	r.Run()

	// The runner must NOT have produced a "skipped" outcome.
	if strings.Contains(stderr.String(), "skipped:") {
		t.Errorf("OPEN issue must not trigger skip, stderr: %q", stderr.String())
	}

	// Collision check must have been reached: at least one git branch call.
	sawBranch := false
	for _, c := range exec.calls {
		if c.name == "git" && len(c.args) >= 1 && c.args[0] == "branch" {
			sawBranch = true
			break
		}
	}
	if !sawBranch {
		t.Error("collision check (git branch) was not reached for OPEN issue")
	}
}
