package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/loop"
	"golemic/internal/telemetry"
)

// ---------------------------------------------------------------------------
// ineligible issue → TERMINAL_SKIPPED, run_finished: skipped, exit 0
// ---------------------------------------------------------------------------

func TestStepPrepare_IneligibleIssue_TerminalSkipped_AC2(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := buildSkipExec(repoRoot, ghIssueJSON("CLOSED", "ready-for-agent"))

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 99)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()

	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0 (skipped = exit 0)", exitCode)
	}
	if !strings.Contains(stderr.String(), "skipped:") {
		t.Errorf("missing skip line in stderr: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "ready-for-agent") {
		t.Errorf("missing ready-for-agent warning in stderr: %q", stderr.String())
	}
	payload := readLastRunFinishedPayload(t, homeDir, project)
	if payload != `{"outcome":"skipped"}` {
		t.Errorf("run_finished payload: got %s, want skipped", payload)
	}
}

// ---------------------------------------------------------------------------
// collision → TERMINAL_ABORTED, run_finished: aborted, exit 1
// ---------------------------------------------------------------------------

// buildLocalBranchCollisionExec returns an executor where the given branch
// appears as an existing local branch (collision) and the issue returns OPEN.
func buildLocalBranchCollisionExec(repoRoot, branch string) *fakeExecutor { //nolint:cyclop
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name != "git" {
				return "", fmt.Errorf("unexpected: %s %v", name, args)
			}
			var subcmd string
			if len(args) >= 3 && args[0] == "-C" {
				subcmd = args[2]
			} else if len(args) >= 1 {
				subcmd = args[0]
			}
			switch subcmd {
			case "rev-parse":
				return repoRoot + "\n", nil
			case "branch":
				if len(args) >= 2 && args[1] == "--list" {
					return "  " + branch + "\n", nil
				}
				return "", nil
			case "ls-remote":
				return "", nil
			}
			return "", fmt.Errorf("not mocked: git %v", args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 1 && args[0] == "issue" {
				return ghIssueJSON("OPEN"), nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

func TestStepPrepare_Collision_TerminalAborted_AC3(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	branch := fmt.Sprintf("golemic/issue-%d", 77)
	exec := buildLocalBranchCollisionExec(repoRoot, branch)

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 77)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1 (collision = aborted)", exitCode)
	}
	if !strings.Contains(stderr.String(), branch) {
		t.Errorf("collision message missing branch name %q: %q", branch, stderr.String())
	}
	assertRunFinishedAborted(t, homeDir, project)
}

// ---------------------------------------------------------------------------
// worktree-create failure → sets WorktreeCreateFailed flag
// ---------------------------------------------------------------------------

// buildWorktreeFailExec returns an executor that fails git worktree add.
func buildWorktreeFailExec() *fakeExecutor { //nolint:cyclop
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name != "git" {
				return "", fmt.Errorf("unexpected: %s %v", name, args)
			}
			var subcmd string
			if len(args) >= 3 && args[0] == "-C" {
				subcmd = args[2]
			} else if len(args) >= 1 {
				subcmd = args[0]
			}
			switch subcmd {
			case "rev-parse":
				return "abc123\n", nil
			case "fetch":
				return "", nil
			case "worktree":
				return "", fmt.Errorf("GIT_WORKTREE_FAILED: injected error")
			case "branch", "ls-remote":
				return "", nil
			}
			return "", fmt.Errorf("not mocked: git %v", args)
		},
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 1 && args[0] == "pr" {
				return "[]", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

func TestStepPrepare_WorktreeCreateFail_SetsFlag_AC4(t *testing.T) {
	logPath := newLogPath(t)
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	prepareLogFile(t, logPath)

	writer, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close() //nolint:errcheck

	homeDir := t.TempDir()
	creds := loadTestCreds(t, homeDir, "testproj")

	var stderrBuf bytes.Buffer
	r := &Runner{
		executor:   buildWorktreeFailExec(),
		homeDir:    homeDir,
		issueNum:   5,
		repoRoot:   "/fake/repo",
		project:    "testproj",
		branchName: "golemic/issue-5",
		issue:      &issueData{Number: 5, Title: "T", State: "OPEN"},
		creds:      creds,
		sink:       telemetry.NoopSink{},
		stderr:     &stderrBuf,
	}

	ctx := &RunContext{
		GolemicDir:   filepath.Join(homeDir, ".golemic", "testproj"),
		EventLogPath: logPath,
		Timeout:      time.Minute,
		Round:        1,
		MaxRounds:    3,
		Writer:       writer,
		DevMode:      DevModeInitial,
	}

	event := r.stepPrepare(ctx)
	if event != loop.EventPrepareFailed {
		t.Fatalf("stepPrepare: got event %q, want PREPARE_FAILED", event)
	}
	if !ctx.WorktreeCreateFailed {
		t.Error("ctx.WorktreeCreateFailed must be true after worktree creation failure")
	}
	if !strings.Contains(stderrBuf.String(), "Failed to create dev worktree") {
		t.Errorf("expected 'Failed to create dev worktree' in stderr, got: %q", stderrBuf.String())
	}
	// Verify the guarded edge routes to TERMINAL_DEV_FAILED (outcome dev_failed, exit 1)
	outcome, exitCode := terminalOutcome(loop.StepTerminalDevFailed)
	if outcome != outcomeDevFailed || exitCode != 1 {
		t.Errorf("terminalOutcome(TERMINAL_DEV_FAILED): got (%q, %d), want (%q, 1)", outcome, exitCode, outcomeDevFailed)
	}
}

// prepareLogFile writes a run_started event to logPath (file must already have dir).
func prepareLogFile(t *testing.T, logPath string) {
	t.Helper()
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Write(eventlog.Event{Type: eventlog.EventRunStarted, Ts: "2024-01-01T00:00:00Z", RunID: "test"})
	w.Close() //nolint:errcheck
}
