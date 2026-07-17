package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// setupCleanExecutor returns a fakeExecutor that succeeds on all git/gh calls
// needed by cleanArtifacts for the given issueNum, recording each call.
//
// localBranchExists and remoteBranchExists control what the branch-check
// commands return. prURL, if non-empty, causes gh pr list to return one OPEN PR.
func setupCleanExecutor(issueNum int, localBranchExists, remoteBranchExists bool, prURL string) *fakeExecutor { //nolint:gocognit,cyclop // switch table plus two bool branches; straightforward test helper
	branch := fmt.Sprintf("golemic/issue-%d", issueNum)
	localOut := ""
	if localBranchExists {
		localOut = "  " + branch + "\n"
	}
	remoteOut := ""
	if remoteBranchExists {
		remoteOut = "abc123\trefs/heads/" + branch + "\n"
	}
	prListOut := "[]"
	if prURL != "" {
		prListOut = fmt.Sprintf(`[{"url":%q,"state":"OPEN"}]`, prURL)
	}

	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name != "git" {
				return "", fmt.Errorf("unexpected Run call: %s %v", name, args)
			}
			sub := args[0]
			switch sub {
			case "worktree":
				return "", nil
			case "branch":
				if len(args) >= 2 && args[1] == "--list" {
					return localOut, nil
				}
				return "", nil // git branch -D
			case "ls-remote":
				return remoteOut, nil
			case "push":
				return "", nil
			}
			return "", fmt.Errorf("unexpected git sub: %s %v", sub, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name != "gh" {
				return "", fmt.Errorf("unexpected RunWithEnv call: %s %v", name, args)
			}
			switch args[0] {
			case "pr":
				if args[1] == "list" {
					return prListOut, nil
				}
				if args[1] == "close" {
					return "", nil
				}
			}
			return "", fmt.Errorf("unexpected gh args: %v", args)
		},
	}
}

// newCleanRunner builds a Runner with clean=true for testing cleanArtifacts
// directly. It sets repoRoot, project, issueNum, branchName, and credentials
// from a temp home dir.
func newCleanRunner(t *testing.T, exec *fakeExecutor, issueNum int) (*Runner, string) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)
	creds := loadTestCreds(t, homeDir, project)

	r := &Runner{
		executor:   exec,
		homeDir:    homeDir,
		project:    project,
		repoRoot:   repoRoot,
		issueNum:   issueNum,
		branchName: fmt.Sprintf("golemic/issue-%d", issueNum),
		creds:      creds,
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
	}
	golemicDir := filepath.Join(homeDir, ".golemic", project)
	return r, golemicDir
}

// mkWorktreeDir creates the worktree directory on disk so os.Stat sees it.
func mkWorktreeDir(t *testing.T, golemicDir string, issueNum int, reviewer bool) {
	t.Helper()
	name := fmt.Sprintf("issue-%d", issueNum)
	if reviewer {
		name = fmt.Sprintf("issue-%d-review", issueNum)
	}
	path := filepath.Join(golemicDir, "worktrees", name)
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// AC-001: full cleanup — all artifacts exist, all removed
// ---------------------------------------------------------------------------

func TestCleanArtifacts_AllArtifactsExist_AC001(t *testing.T) { //nolint:cyclop // multiple sequential artifact-presence assertions; splitting adds no clarity
	prURL := "https://github.com/owner/repo/pull/42"
	exec := setupCleanExecutor(42, true, true, prURL)

	r, golemicDir := newCleanRunner(t, exec, 42)
	mkWorktreeDir(t, golemicDir, 42, false)
	mkWorktreeDir(t, golemicDir, 42, true)

	var stderr bytes.Buffer
	r.stderr = &stderr

	if err := r.cleanArtifacts(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := stderr.String()
	if !strings.Contains(out, "removed dev worktree") {
		t.Errorf("stderr missing dev worktree removal report, got: %q", out)
	}
	if !strings.Contains(out, "removed reviewer worktree") {
		t.Errorf("stderr missing reviewer worktree removal report, got: %q", out)
	}
	if !strings.Contains(out, "deleted local branch golemic/issue-42") {
		t.Errorf("stderr missing local branch removal report, got: %q", out)
	}
	if !strings.Contains(out, "deleted remote branch golemic/issue-42") {
		t.Errorf("stderr missing remote branch removal report, got: %q", out)
	}
	if !strings.Contains(out, "closed PR "+prURL) {
		t.Errorf("stderr missing PR close report, got: %q", out)
	}

	// Verify git worktree remove was called for both worktrees
	var worktreeRemoveCalls int
	for _, c := range exec.calls {
		if c.name == "git" && len(c.args) >= 2 && c.args[0] == "worktree" && c.args[1] == "remove" {
			worktreeRemoveCalls++
		}
	}
	if worktreeRemoveCalls != 2 {
		t.Errorf("expected 2 git worktree remove calls, got %d", worktreeRemoveCalls)
	}
}

// ---------------------------------------------------------------------------
// AC-002: idempotent — no artifacts exist, no errors
// ---------------------------------------------------------------------------

func TestCleanArtifacts_NoArtifacts_Idempotent_AC002(t *testing.T) {
	// No worktree dirs created, no local/remote branch, no PR
	exec := setupCleanExecutor(42, false, false, "")

	r, _ := newCleanRunner(t, exec, 42)

	var stderr bytes.Buffer
	r.stderr = &stderr

	if err := r.cleanArtifacts(); err != nil {
		t.Fatalf("expected no error for absent artifacts, got: %v", err)
	}

	out := stderr.String()
	// No removal lines expected
	if strings.Contains(out, "clean:") {
		t.Errorf("expected no cleanup output when nothing exists, got: %q", out)
	}

	// git worktree remove must NOT have been called
	for _, c := range exec.calls {
		if c.name == "git" && len(c.args) >= 2 && c.args[0] == "worktree" && c.args[1] == "remove" {
			t.Errorf("git worktree remove must not be called when worktree absent: %v", c.args)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-003: without --clean flag, collision still aborts the run
// ---------------------------------------------------------------------------

func TestRun_WithoutCleanFlag_CollisionAborts_AC003(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	// Pre-create worktree directory to trigger collision
	worktreeDir := filepath.Join(homeDir, ".golemic", project, "worktrees", "issue-42")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)
	// clean is false by default — do NOT call r.SetClean(true)

	exitCode := r.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "Worktree exists at") {
		t.Errorf("expected collision message, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC-004: cleanup is scoped to target issue — commands never reference other issues
// ---------------------------------------------------------------------------

func TestCleanArtifacts_ScopedToTargetIssue_AC004(t *testing.T) {
	otherIssue := 99
	exec := setupCleanExecutor(42, true, true, "https://github.com/owner/repo/pull/42")

	r, golemicDir := newCleanRunner(t, exec, 42)
	mkWorktreeDir(t, golemicDir, 42, false)
	mkWorktreeDir(t, golemicDir, 42, true)

	// Also create issue-99 worktrees on disk (must not be touched)
	mkWorktreeDir(t, golemicDir, otherIssue, false)
	mkWorktreeDir(t, golemicDir, otherIssue, true)

	if err := r.cleanArtifacts(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no command references issue-99
	for _, c := range exec.calls {
		for _, arg := range c.args {
			if strings.Contains(arg, "issue-99") || strings.Contains(arg, "issue-99-review") {
				t.Errorf("command %s %v references issue 99 — must be scoped to issue 42 only", c.name, c.args)
			}
		}
	}

	// Verify issue-99 worktrees are still on disk
	otherDevPath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", otherIssue))
	if _, err := os.Stat(otherDevPath); os.IsNotExist(err) {
		t.Errorf("issue-99 dev worktree was removed — must not be touched")
	}
	otherRevPath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", otherIssue))
	if _, err := os.Stat(otherRevPath); os.IsNotExist(err) {
		t.Errorf("issue-99 reviewer worktree was removed — must not be touched")
	}
}

// ---------------------------------------------------------------------------
// AC-005: cleanup failure aborts before dev phase starts
// ---------------------------------------------------------------------------

func TestRun_CleanFailure_AbortsBeforeDevPhase_AC005(t *testing.T) { //nolint:gocognit,cyclop,funlen // inline executor with multiple gh/git branches; complexity is in the test fixture, not the logic
	homeDir, repoRoot, project := setupRunnerTest(t)
	prURL := "https://github.com/owner/repo/pull/42"

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			// Happy path for everything git needs for pre-collision setup
			if name == "git" {
				sub := ""
				if len(args) >= 3 && args[0] == "-C" {
					sub = args[2]
				} else if len(args) >= 1 {
					sub = args[0]
				}
				switch sub {
				case "rev-parse":
					return repoRoot + "\n", nil
				case "branch":
					if len(args) >= 2 && args[len(args)-2] == "--list" {
						return "", nil // no local branch
					}
					return "", nil
				case "ls-remote":
					return "", nil
				case "worktree":
					return "", nil
				}
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" {
				switch args[0] {
				case "issue":
					return `{"title":"Test","body":"Body"}`, nil
				case "pr":
					if args[1] == "list" {
						return fmt.Sprintf(`[{"url":%q,"state":"OPEN"}]`, prURL), nil
					}
					if args[1] == "close" {
						return "", fmt.Errorf("permission denied")
					}
				}
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)
	r.SetClean(true)

	exitCode := r.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "clean failed") {
		t.Errorf("stderr should contain 'clean failed', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, prURL) {
		t.Errorf("stderr should name the PR URL, got: %q", errMsg)
	}

	// Verify dev phase was not entered: git fetch (worktree.Create) must not appear
	for _, c := range exec.calls {
		if c.name == "git" {
			sub := ""
			if len(c.args) >= 3 && c.args[0] == "-C" {
				sub = c.args[2]
			} else if len(c.args) >= 1 {
				sub = c.args[0]
			}
			if sub == "fetch" {
				t.Errorf("git fetch called — dev phase must not start after clean failure: %v", c.args)
			}
		}
	}

	// run_finished with aborted must be in the event log
	assertRunFinishedAborted(t, homeDir, project)
}
