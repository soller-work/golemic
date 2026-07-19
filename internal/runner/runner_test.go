package runner

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/prompt"
)

// ---------------------------------------------------------------------------
// fakeExecutor with call recording
// ---------------------------------------------------------------------------

type callRecord struct {
	name string
	args []string
	env  map[string]string
}

type fakeExecutor struct {
	runFunc        func(name string, args ...string) (string, error)
	runWithEnvFunc func(env map[string]string, name string, args ...string) (string, error)
	calls          []callRecord
}

func (f *fakeExecutor) Run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, callRecord{name: name, args: args})
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (f *fakeExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, callRecord{name: name, args: args, env: env})
	if f.runWithEnvFunc != nil {
		return f.runWithEnvFunc(env, name, args...)
	}
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (f *fakeExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, callRecord{name: name, args: args})
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (f *fakeExecutor) RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, callRecord{name: name, args: args, env: env})
	if f.runWithEnvFunc != nil {
		return f.runWithEnvFunc(env, name, args...)
	}
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

// ---------------------------------------------------------------------------
// realGitExecutor runs git commands using os/exec for integration-style tests.
// ---------------------------------------------------------------------------

type realGitExecutor struct {
	cwd string // working directory for git commands
}

func (e *realGitExecutor) Run(name string, args ...string) (string, error) {
	if name != "git" {
		return "", fmt.Errorf("not git: %s", name)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = e.cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (e *realGitExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	if name != "git" {
		return "", fmt.Errorf("not git: %s", name)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = e.cwd
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (e *realGitExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	return e.Run(name, args...)
}

func (e *realGitExecutor) RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error) {
	return e.RunWithEnv(env, name, args...)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// setupHappyExecutor creates a fakeExecutor that responds to all commands needed
// for a successful run (no collisions).
func setupHappyExecutor(repoRoot string) *fakeExecutor {
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			// Determine the git subcommand (may be after -C <dir>)
			var subcmd string
			if len(args) >= 3 && args[0] == "-C" {
				subcmd = args[2]
			} else if len(args) >= 1 {
				subcmd = args[0]
			}
			switch subcmd {
			case "rev-parse":
				if len(args) >= 3 && args[0] == "-C" {
					return "abc123\n", nil
				}
				return repoRoot + "\n", nil
			case "branch":
				return "", nil // no local branch
			case "ls-remote":
				return "", nil // no remote branch
			case "fetch":
				return "", nil // successful fetch
			case "worktree":
				return "", nil // successful worktree operation
			case "config":
				return "", nil // successful config
			case "status":
				return "", nil // clean status
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "issue":
				return `{"title":"Test Issue","labels":[],"state":"OPEN"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[]`, nil // no open PR
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

// ---------------------------------------------------------------------------
// Preflighter stubs
// ---------------------------------------------------------------------------

// passingPreflighter always returns all-OK results. Used to inject a no-op gate
// so runner unit tests don't need to mock gh/pi/git preflight commands.
type passingPreflighter struct{}

func (passingPreflighter) Check() preflight.Results {
	return preflight.Results{{Name: "stub", Ok: true}}
}

// failingPreflighter returns a single failing result for gate tests.
type failingPreflighter struct{ detail string }

func (f failingPreflighter) Check() preflight.Results {
	return preflight.Results{{Name: "gh installiert", Ok: false, Details: f.detail}}
}

// setupRunnerTest creates temp directories and writes a minimal valid config and
// credentials. Returns the homeDir, repoRoot, and project name.
func setupRunnerTest(t *testing.T) (homeDir, repoRoot, project string) {
	t.Helper()
	homeDir = t.TempDir()
	repoRoot = t.TempDir()
	project = "test-project"

	// Create .golemic/config.json
	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := fmt.Sprintf(`{"project": "%s", "verify_command": "go test"}`, project)
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create ~/.golemic/<project>/credentials.json (must be 0600)
	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := `{"dev_token": "ghp_dev_test_token", "reviewer_token": "ghp_rev_test_token"}`
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}

	return homeDir, repoRoot, project
}

// gitInit initializes a git repo at dir and creates an initial commit.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	mustRun(t, dir, "git", "init")
	mustRun(t, dir, "git", "config", "user.email", "test@test.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")
	mustRun(t, dir, "git", "commit", "--allow-empty", "-m", "init")
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v in %s failed: %v\n%s", name, args, dir, err, string(out))
	}
}

// ---------------------------------------------------------------------------
// AC-002: Worktree collision aborts with cleanup command
// ---------------------------------------------------------------------------

func TestRun_WorktreeCollision_AC002(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	// Pre-create worktree directory to simulate collision
	worktreeDir := filepath.Join(homeDir, ".golemic", project, "worktrees", "issue-42")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetPreflighter(passingPreflighter{})
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "Worktree exists at") {
		t.Errorf("stderr missing 'Worktree exists at', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "git worktree remove") {
		t.Errorf("stderr missing 'git worktree remove' command, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, worktreeDir) {
		t.Errorf("stderr should contain worktree path, got: %q", errMsg)
	}

	// Verify run_started was written (even on collision)
	runID := strings.TrimSpace(stdout.String())
	if runID == "" {
		// Find runID from event log directory
		runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
		entries, err := os.ReadDir(runsDir)
		if err == nil && len(entries) > 0 {
			runID = entries[0].Name()
		}
	}
	assertRunFinishedAborted(t, homeDir, project)
}

// assertRunFinishedAborted reads the event log for a run in the given project's runs
// directory and asserts that exactly 2 events exist: run_started followed by
// run_finished with outcome aborted.
func assertRunFinishedAborted(t *testing.T, homeDir, project string) {
	t.Helper()
	runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		t.Fatalf("failed to read runs dir %s: %v", runsDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 run directory, got %d", len(entries))
	}
	runID := entries[0].Name()
	logPath := filepath.Join(runsDir, runID, "events.jsonl")

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatalf("failed to read event log: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events (run_started + run_finished), got %d", len(events))
	}
	if events[0].Type != eventlog.EventRunStarted {
		t.Errorf("first event type: got %q, want %q", events[0].Type, eventlog.EventRunStarted)
	}
	if events[1].Type != eventlog.EventRunFinished {
		t.Errorf("second event type: got %q, want %q", events[1].Type, eventlog.EventRunFinished)
	}
	if string(events[1].Payload) != `{"outcome":"aborted"}` {
		t.Errorf("run_finished payload: got %s, want %q", string(events[1].Payload), `{"outcome":"aborted"}`)
	}
}

// ---------------------------------------------------------------------------
// AC-003: Local branch collision aborts with cleanup commands
// ---------------------------------------------------------------------------

func TestRun_LocalBranchCollision_AC003(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "git" && len(args) >= 1 && args[0] == "branch":
				return "  golemic/issue-42\n", nil // local branch exists
			case name == "git" && len(args) >= 1 && args[0] == "ls-remote":
				// Should not be reached because local branch collision is reported first
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "issue":
				return `{"title":"Test","labels":[],"state":"OPEN"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetPreflighter(passingPreflighter{})
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "Branch golemic/issue-42 exists locally") {
		t.Errorf("stderr should mention local branch, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "git branch -D golemic/issue-42") {
		t.Errorf("stderr should include git branch -D command, got: %q", errMsg)
	}

	assertRunFinishedAborted(t, homeDir, project)
}

// ---------------------------------------------------------------------------
// AC-003: Remote branch collision aborts with cleanup commands
// ---------------------------------------------------------------------------

func TestRun_RemoteBranchCollision_AC003(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "git" && len(args) >= 1 && args[0] == "branch":
				return "", nil // no local branch
			case name == "git" && len(args) >= 1 && args[0] == "ls-remote":
				return "abc123\trefs/heads/golemic/issue-42\n", nil // remote branch exists
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "issue":
				return `{"title":"Test","labels":[],"state":"OPEN"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetPreflighter(passingPreflighter{})
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "Branch golemic/issue-42 exists on origin") {
		t.Errorf("stderr should mention remote branch, got: %q", errMsg)
	}
	assertRunFinishedAborted(t, homeDir, project)
}

// ---------------------------------------------------------------------------
// AC-004: Open PR collision aborts with PR URL
// ---------------------------------------------------------------------------

func TestRun_OpenPRCollision_AC004(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "git" && len(args) >= 1 && args[0] == "branch":
				return "", nil // no local branch
			case name == "git" && len(args) >= 1 && args[0] == "ls-remote":
				return "", nil // no remote branch
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "issue":
				return `{"title":"Test","labels":[],"state":"OPEN"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[{"url":"https://github.com/owner/repo/pull/123","state":"OPEN"}]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetPreflighter(passingPreflighter{})
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "Open PR https://github.com/owner/repo/pull/123 exists") {
		t.Errorf("stderr should mention PR URL, got: %q", errMsg)
	}
	assertRunFinishedAborted(t, homeDir, project)
}

// ---------------------------------------------------------------------------
// AC-005: Missing config aborts before GitHub access
// ---------------------------------------------------------------------------

func TestRun_MissingConfig_AC005(t *testing.T) {
	homeDir := t.TempDir()
	repoRoot := t.TempDir()
	// Do NOT create config.json

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetPreflighter(passingPreflighter{})
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "Failed to load config") {
		t.Errorf("stderr should mention config failure, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "config file not found") {
		t.Errorf("stderr should mention 'config file not found', got: %q", errMsg)
	}

	// Verify no gh or git commands were called beyond git rev-parse
	for _, call := range exec.calls {
		if call.name == "gh" {
			t.Errorf("gh command was called despite config failure: %s %v", call.name, call.args)
		}
		if call.name == "git" && (len(call.args) < 1 || call.args[0] != "rev-parse") {
			t.Errorf("non-rev-parse git command was called despite config failure: %s %v", call.name, call.args)
		}
	}
}

// ---------------------------------------------------------------------------
// Missing credentials aborts before GitHub access (also AC-005)
// ---------------------------------------------------------------------------

func TestRun_MissingCredentials_AC005(t *testing.T) {
	homeDir := t.TempDir()
	repoRoot := t.TempDir()

	// Create config but NOT credentials
	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"project": "test-project", "verify_command": "go test"}`
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("should not be called: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetPreflighter(passingPreflighter{})
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)
	runner.SetLookupEnv(func(string) (string, bool) { return "", false })

	exitCode := runner.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "Failed to load credentials") {
		t.Errorf("stderr should mention credentials failure, got: %q", errMsg)
	}

	// Verify no gh commands were called
	for _, call := range exec.calls {
		if call.name == "gh" {
			t.Errorf("gh command was called despite credentials failure: %s %v", call.name, call.args)
		}
	}
}

// ---------------------------------------------------------------------------
// Issue load failure handled gracefully
// ---------------------------------------------------------------------------

func TestRun_IssueLoadFailure(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "rev-parse":
				return repoRoot + "\n", nil
			case name == "git" && len(args) >= 1 && args[0] == "branch":
				return "", nil
			case name == "git" && len(args) >= 1 && args[0] == "ls-remote":
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "issue":
				return "", &preflight.ErrExit{ExitCode: 1, Stderr: "issue not found"}
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 99999)
	runner.SetPreflighter(passingPreflighter{})
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "Failed to load issue 99999") {
		t.Errorf("stderr should mention issue load failure, got: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// Host repo resolution: inside tools/golemic
// ---------------------------------------------------------------------------

func TestResolveHostRepo_InsideToolsGolemic(t *testing.T) {
	hostRoot := t.TempDir()
	golemicDir := filepath.Join(hostRoot, "tools", "golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Init git in host root
	gitInit(t, hostRoot)

	// Use realGitExecutor with cwd = golemicDir to simulate running from tools/golemic
	exec := &realGitExecutor{cwd: golemicDir}
	root, err := resolveHostRepo(exec, golemicDir)
	if err != nil {
		t.Fatalf("resolveHostRepo failed: %v", err)
	}

	// Resolve symlinks (macOS /var → /private/var)
	expected, _ := filepath.EvalSymlinks(hostRoot)
	if root != expected {
		t.Errorf("expected host root %q, got %q", expected, root)
	}
}

func TestResolveHostRepo_NormalCwd(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)

	exec := &realGitExecutor{cwd: repoRoot}
	root, err := resolveHostRepo(exec, repoRoot)
	if err != nil {
		t.Fatalf("resolveHostRepo failed: %v", err)
	}

	// Resolve symlinks (macOS /var → /private/var)
	expected, _ := filepath.EvalSymlinks(repoRoot)
	if root != expected {
		t.Errorf("expected root %q, got %q", expected, root)
	}
}

func TestResolveHostRepo_NotAGitRepo(t *testing.T) {
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "", fmt.Errorf("fatal: not a git repository")
		},
	}

	_, err := resolveHostRepo(exec, "/tmp")
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Errorf("error should mention 'not in a git repository', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unit-level: Worktree collision via checkWorktreeCollision
// ---------------------------------------------------------------------------

func TestCheckWorktreeCollision_Exists(t *testing.T) {
	homeDir := t.TempDir()
	project := "test-project"

	// Create the worktree directory
	wtDir := filepath.Join(homeDir, ".golemic", project, "worktrees", "issue-42")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{
		homeDir:  homeDir,
		project:  project,
		issueNum: 42,
	}

	collision := runner.checkWorktreeCollision()
	if collision == nil {
		t.Fatal("expected collision, got nil")
	}
	if !strings.Contains(collision.Message, "Worktree exists at") {
		t.Errorf("message should mention 'Worktree exists at', got: %s", collision.Message)
	}
	if !strings.Contains(collision.Message, wtDir) {
		t.Errorf("message should contain path %q, got: %s", wtDir, collision.Message)
	}
	if !strings.Contains(collision.Message, "git worktree remove") {
		t.Errorf("message should contain 'git worktree remove', got: %s", collision.Message)
	}
}

func TestCheckWorktreeCollision_NotExists(t *testing.T) {
	homeDir := t.TempDir()

	runner := &Runner{
		homeDir:  homeDir,
		project:  "test-project",
		issueNum: 42,
	}

	collision := runner.checkWorktreeCollision()
	if collision != nil {
		t.Fatalf("expected no collision, got: %s", collision.Message)
	}
}

// ---------------------------------------------------------------------------
// Unit-level: Branch collision via checkBranchCollision
// ---------------------------------------------------------------------------

func TestCheckBranchCollision_Local(t *testing.T) {
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "branch":
				return "  golemic/issue-42\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	runner := &Runner{
		executor:   exec,
		branchName: "golemic/issue-42",
	}

	collision, err := runner.checkBranchCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision == nil {
		t.Fatal("expected collision, got nil")
	}
	if !strings.Contains(collision.Message, "Branch golemic/issue-42 exists locally") {
		t.Errorf("message should mention local branch, got: %s", collision.Message)
	}
	if !strings.Contains(collision.Message, "git branch -D golemic/issue-42") {
		t.Errorf("message should have delete command, got: %s", collision.Message)
	}
}

func TestCheckBranchCollision_Remote(t *testing.T) {
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "branch":
				return "", nil
			case name == "git" && len(args) >= 1 && args[0] == "ls-remote":
				return "abc123\trefs/heads/golemic/issue-42\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	runner := &Runner{
		executor:   exec,
		branchName: "golemic/issue-42",
	}

	collision, err := runner.checkBranchCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision == nil {
		t.Fatal("expected collision, got nil")
	}
	if !strings.Contains(collision.Message, "Branch golemic/issue-42 exists on origin") {
		t.Errorf("message should mention remote branch, got: %s", collision.Message)
	}
	if !strings.Contains(collision.Message, "git push origin --delete golemic/issue-42") {
		t.Errorf("message should have remote delete command, got: %s", collision.Message)
	}
}

func TestCheckBranchCollision_None(t *testing.T) {
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "branch":
				return "", nil
			case name == "git" && len(args) >= 1 && args[0] == "ls-remote":
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	runner := &Runner{
		executor:   exec,
		branchName: "golemic/issue-42",
	}

	collision, err := runner.checkBranchCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision != nil {
		t.Fatalf("expected no collision, got: %s", collision.Message)
	}
}

// ---------------------------------------------------------------------------
// Branch collision with git error (fail-closed)
// ---------------------------------------------------------------------------

func TestCheckBranchCollision_GitError(t *testing.T) {
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "", fmt.Errorf("git command failed")
		},
	}

	runner := &Runner{
		executor:   exec,
		branchName: "golemic/issue-42",
	}

	collision, err := runner.checkBranchCollision()
	if err == nil {
		t.Fatal("expected error from git failure, got nil")
	}
	if collision != nil {
		t.Errorf("expected nil collision on git error, got: %s", collision.Message)
	}
	if !strings.Contains(err.Error(), "failed to check git state") {
		t.Errorf("error should contain 'failed to check git state', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unit-level: PR collision via checkPRCollision
// ---------------------------------------------------------------------------

func TestCheckPRCollision_OpenPR(t *testing.T) {
	// We need a valid *credentials.Credentials for the dev token.
	// Use the loader to create one from a temp file.
	homeDir := t.TempDir()
	project := "pr-test"

	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := `{"dev_token": "ghp_dev_pr_test", "reviewer_token": "ghp_rev_pr_test"}`
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[{"url":"https://github.com/owner/repo/pull/42","state":"OPEN"}]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	runner := &Runner{
		executor:   exec,
		creds:      creds,
		branchName: "golemic/issue-42",
	}

	collision, err := runner.checkPRCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision == nil {
		t.Fatal("expected collision, got nil")
	}
	if !strings.Contains(collision.Message, "Open PR https://github.com/owner/repo/pull/42 exists") {
		t.Errorf("message should mention PR URL, got: %s", collision.Message)
	}
	if !strings.Contains(collision.Message, "close it first") {
		t.Errorf("message should contain 'close it first', got: %s", collision.Message)
	}
}

func TestCheckPRCollision_NoPR(t *testing.T) {
	homeDir := t.TempDir()
	project := "pr-test"

	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := `{"dev_token": "ghp_dev_pr_test", "reviewer_token": "ghp_rev_pr_test"}`
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	runner := &Runner{
		executor:   exec,
		creds:      creds,
		branchName: "golemic/issue-42",
	}

	collision, err := runner.checkPRCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision != nil {
		t.Fatalf("expected no collision, got: %s", collision.Message)
	}
}

func TestCheckPRCollision_ClosedPR(t *testing.T) {
	homeDir := t.TempDir()
	project := "pr-test-closed"

	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := `{"dev_token": "ghp_dev_pr_test", "reviewer_token": "ghp_rev_pr_test"}`
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[{"url":"https://github.com/owner/repo/pull/42","state":"CLOSED"}]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	runner := &Runner{
		executor:   exec,
		creds:      creds,
		branchName: "golemic/issue-42",
	}

	collision, err := runner.checkPRCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision != nil {
		t.Fatalf("expected no collision for closed PR, got: %s", collision.Message)
	}
}

// TestCheckPRCollision_GhFails verifies that gh command failure is treated as
// a fatal error (fail-closed per P2-2).
func TestCheckPRCollision_GhFails(t *testing.T) {
	homeDir := t.TempDir()
	project := "pr-test-fail"

	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := `{"dev_token": "ghp_dev_pr_test", "reviewer_token": "ghp_rev_pr_test"}`
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatal(err)
	}

	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("gh not available")
		},
	}

	runner := &Runner{
		executor:   exec,
		creds:      creds,
		branchName: "golemic/issue-42",
	}

	// gh failure should be treated as a fatal error (fail-closed per P2-2)
	collision, err := runner.checkPRCollision()
	if err == nil {
		t.Fatal("expected error from gh failure, got nil")
	}
	if collision != nil {
		t.Errorf("expected nil collision on gh error, got: %s", collision.Message)
	}
	if !strings.Contains(err.Error(), "failed to check PR state") {
		t.Errorf("error should contain 'failed to check PR state', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC-001: Failing Preflighter stub → exit 1, no run dir/event log/GitHub access
// ---------------------------------------------------------------------------

func TestRun_PreflightGate_FailClosed_AC001(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(failingPreflighter{detail: "gh not found"})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}

	errMsg := stderr.String()
	if !strings.Contains(errMsg, "FAILED: ") {
		t.Errorf("stderr missing 'FAILED: ', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "failed") {
		t.Errorf("stderr missing final 'failed', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "gh not found") {
		t.Errorf("stderr missing detail 'gh not found', got: %q", errMsg)
	}

	// No run directory / event log created
	runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
	if _, err := os.Stat(runsDir); !os.IsNotExist(err) {
		t.Error("runs directory must not be created when preflight fails")
	}

	// No GitHub access: gh issue/pr calls must not have been made
	for _, call := range exec.calls {
		if call.name == "gh" {
			t.Errorf("gh command must not be called when preflight fails: %s %v", call.name, call.args)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-003: Passing Preflighter stub → run proceeds (run_started + run dir created)
// ---------------------------------------------------------------------------

func TestRun_PreflightGate_PassProceedsNormally_AC003(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	// We don't care about the final outcome (orchestration will fail without
	// full agent setup), just that the run proceeded past the gate and created
	// a run directory / event log with run_started.
	r.Run()

	// Verify a run directory was created
	runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		t.Fatalf("runs dir should exist after passing preflight: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("at least one run directory should exist after passing preflight")
	}

	// Verify run_started event was written
	runID := entries[0].Name()
	logPath := filepath.Join(runsDir, runID, "events.jsonl")
	var reader eventlog.Reader
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatalf("failed to read event log: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("event log should contain at least run_started")
	}
	if events[0].Type != eventlog.EventRunStarted {
		t.Errorf("first event should be run_started, got: %q", events[0].Type)
	}
}

// ---------------------------------------------------------------------------
// AC-001/AC-002/AC-003: Role-specific guidelines path selection
// ---------------------------------------------------------------------------

// TestDevGuidelinesPath_AC001 verifies that the dev role reads its guidelines
// from .golemic/guidelines/dev.md and not from the reviewer file.
func TestDevGuidelinesPath_AC001(t *testing.T) {
	_, repoRoot, _ := setupRunnerTest(t)
	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("DEV-MARKER"), 0644)      //nolint:errcheck
	os.WriteFile(filepath.Join(guidelinesDir, "reviewer.md"), []byte("REV-MARKER"), 0644) //nolint:errcheck

	devPath := filepath.Join(repoRoot, ".golemic", "guidelines", "dev.md")
	userPrompt, err := prompt.RenderDev(
		prompt.Issue{Number: 1, Title: "t"},
		"golemic-dev-1",
		"go test",
		devPath,
	)
	if err != nil {
		t.Fatalf("RenderDev failed: %v", err)
	}
	if !strings.Contains(userPrompt, "DEV-MARKER") {
		t.Error("dev prompt does not contain DEV-MARKER")
	}
	if strings.Contains(userPrompt, "REV-MARKER") {
		t.Error("dev prompt must not contain REV-MARKER")
	}
}

// TestReviewerGuidelinesPath_AC002 verifies that the reviewer role reads its
// guidelines from .golemic/guidelines/reviewer.md and not from the dev file.
func TestReviewerGuidelinesPath_AC002(t *testing.T) {
	_, repoRoot, _ := setupRunnerTest(t)
	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("DEV-MARKER"), 0644)      //nolint:errcheck
	os.WriteFile(filepath.Join(guidelinesDir, "reviewer.md"), []byte("REV-MARKER"), 0644) //nolint:errcheck

	reviewerPath := filepath.Join(repoRoot, ".golemic", "guidelines", "reviewer.md")
	userPrompt, err := prompt.RenderReviewer(
		99,
		prompt.Issue{Number: 1, Title: "t"},
		"go test",
		reviewerPath,
	)
	if err != nil {
		t.Fatalf("RenderReviewer failed: %v", err)
	}
	if !strings.Contains(userPrompt, "REV-MARKER") {
		t.Error("reviewer prompt does not contain REV-MARKER")
	}
	if strings.Contains(userPrompt, "DEV-MARKER") {
		t.Error("reviewer prompt must not contain DEV-MARKER")
	}
}

// TestRunDevAgent_MissingGuidelines_AC003 verifies that runDevAgent fails
// closed when .golemic/guidelines/dev.md is absent, reporting the expected path.
func TestRunDevAgent_MissingGuidelines_AC003(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)

	r := New(nil, homeDir, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = project
	r.issue = &issueData{Number: 42, Title: "t"}
	r.cfg = &config.Config{VerifyCommand: "go test"}
	r.branchName = "golemic-dev-42"

	var stderr bytes.Buffer
	r.SetStderr(&stderr)

	outcome := r.runDevAgent(filepath.Join(repoRoot, ".golemic"), "/tmp/events.jsonl", 5*time.Minute, "", 1)

	if outcome != outcomeDevFailed {
		t.Errorf("expected %q, got %q", outcomeDevFailed, outcome)
	}
	expectedPath := filepath.Join(repoRoot, ".golemic", "guidelines", "dev.md")
	if !strings.Contains(stderr.String(), expectedPath) {
		t.Errorf("stderr should contain %q, got: %s", expectedPath, stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC-001: SystemPromptFile is resolved from binary dir, not repoRoot
// AC-002: Missing system prompt next to binary → fail-closed with binary-dir path in error
// ---------------------------------------------------------------------------

// setupDevRunner builds a minimal Runner for runDevAgent unit tests with valid
// guidelines and credentials but no prompts/ anywhere inside repoRoot.
func setupDevRunner(t *testing.T) (r *Runner, golemicDir string, stderr *bytes.Buffer) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)

	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	runner := New(nil, homeDir, repoRoot, 42)
	runner.repoRoot = repoRoot
	runner.project = project
	runner.creds = creds
	runner.runID = "issue-42-20240101T000000Z"
	runner.issue = &issueData{Number: 42, Title: "t"}
	runner.cfg = &config.Config{VerifyCommand: "go test", Models: config.Models{Dev: "claude-3-5-sonnet-20241022"}}
	runner.branchName = "golemic-dev-42"

	var buf bytes.Buffer
	runner.SetStderr(&buf)
	return runner, filepath.Join(repoRoot, ".golemic"), &buf
}

// TestRunDevAgent_SystemPromptFromBinaryDir_AC001 verifies that when
// prompts/dev.md exists next to the test binary, the runner passes system-prompt
// validation and fails for a different reason (worktree/CLI absent) — proving it
// never looks in repoRoot.
func TestRunDevAgent_SystemPromptFromBinaryDir_AC001(t *testing.T) {
	execPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	binaryDir := filepath.Dir(execPath)
	promptsDir := filepath.Join(binaryDir, "prompts")
	devPromptPath := filepath.Join(promptsDir, "dev.md")

	if _, statErr := os.Stat(devPromptPath); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(promptsDir, 0755); mkErr != nil {
			t.Fatalf("MkdirAll prompts: %v", mkErr)
		}
		if wErr := os.WriteFile(devPromptPath, []byte("# Dev"), 0644); wErr != nil {
			t.Fatalf("WriteFile dev.md: %v", wErr)
		}
		t.Cleanup(func() { os.Remove(devPromptPath) }) //nolint:errcheck
	}

	r, golemicDir, stderr := setupDevRunner(t)
	r.runDevAgent(golemicDir, "/tmp/events.jsonl", 5*time.Minute, "", 1)

	// System prompt was found: error must NOT mention the system prompt path
	if strings.Contains(stderr.String(), "systemPromptFile") {
		t.Errorf("system prompt validation should pass when prompts/ is next to binary, got: %s", stderr.String())
	}
	// repoRoot must not appear in prompts context
	if strings.Contains(stderr.String(), filepath.Join(r.repoRoot, "prompts")) {
		t.Errorf("system prompt must not reference repoRoot/prompts, got: %s", stderr.String())
	}
}

// TestRunDevAgent_MissingSystemPromptInBinaryDir_AC002 verifies fail-closed
// behaviour when prompts/dev.md is absent from the binary directory: the runner
// returns outcomeDevFailed and the error names the binary-dir path.
func TestRunDevAgent_MissingSystemPromptInBinaryDir_AC002(t *testing.T) {
	execPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	binaryDir := filepath.Dir(execPath)
	devPromptPath := filepath.Join(binaryDir, "prompts", "dev.md")

	if _, statErr := os.Stat(devPromptPath); statErr == nil {
		t.Skip("prompts/dev.md exists next to test binary; cannot test missing-prompt path without removing a real file")
	}

	r, golemicDir, stderr := setupDevRunner(t)
	outcome := r.runDevAgent(golemicDir, "/tmp/events.jsonl", 5*time.Minute, "", 1)

	if outcome != outcomeDevFailed {
		t.Errorf("expected %q, got %q", outcomeDevFailed, outcome)
	}
	if !strings.Contains(stderr.String(), devPromptPath) {
		t.Errorf("stderr should contain expected path %q, got: %s", devPromptPath, stderr.String())
	}
	if strings.Contains(stderr.String(), filepath.Join(r.repoRoot, "prompts")) {
		t.Errorf("stderr must not reference repoRoot/prompts, got: %s", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// dirCapturingExecutor records dirs used by RunInDir / RunWithEnvInDir
// ---------------------------------------------------------------------------

type dirRecord struct {
	dir  string
	name string
	args []string
	env  map[string]string
}

type dirCapturingExecutor struct {
	dirCalls []dirRecord
	runFunc  func(name string, args ...string) (string, error)
}

func (d *dirCapturingExecutor) Run(name string, args ...string) (string, error) {
	if d.runFunc != nil {
		return d.runFunc(name, args...)
	}
	return "", nil
}

func (d *dirCapturingExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	if d.runFunc != nil {
		return d.runFunc(name, args...)
	}
	return "", nil
}

func (d *dirCapturingExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	d.dirCalls = append(d.dirCalls, dirRecord{dir: dir, name: name, args: args})
	if d.runFunc != nil {
		return d.runFunc(name, args...)
	}
	return "", nil
}

func (d *dirCapturingExecutor) RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error) {
	d.dirCalls = append(d.dirCalls, dirRecord{dir: dir, name: name, args: args, env: env})
	if d.runFunc != nil {
		return d.runFunc(name, args...)
	}
	return "", nil
}

// loadTestCreds creates credentials in homeDir and returns a *credentials.Credentials.
func loadTestCreds(t *testing.T, homeDir, project string) *credentials.Credentials {
	t.Helper()
	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := `{"dev_token": "ghp_dev_pin_test", "reviewer_token": "ghp_rev_pin_test"}`
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}
	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	return creds
}

// ---------------------------------------------------------------------------
// AC-001: checkBranchCollision and checkPRCollision pin calls to repoRoot
// ---------------------------------------------------------------------------

func TestCheckBranchCollision_PinnedToRepoRoot_AC001(t *testing.T) {
	repoRoot := "/fake/host/repo"
	exec := &dirCapturingExecutor{
		runFunc: func(name string, args ...string) (string, error) { return "", nil },
	}
	r := &Runner{executor: exec, repoRoot: repoRoot, branchName: "golemic/issue-7"}

	_, err := r.checkBranchCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(exec.dirCalls) < 2 {
		t.Fatalf("expected at least 2 RunInDir calls, got %d", len(exec.dirCalls))
	}
	for _, c := range exec.dirCalls {
		if c.dir != repoRoot {
			t.Errorf("call %s %v used dir %q, want %q", c.name, c.args, c.dir, repoRoot)
		}
	}
	// Verify the two git subcommands
	if exec.dirCalls[0].args[0] != "branch" {
		t.Errorf("first call should be 'git branch', got args %v", exec.dirCalls[0].args)
	}
	if exec.dirCalls[1].args[0] != "ls-remote" {
		t.Errorf("second call should be 'git ls-remote', got args %v", exec.dirCalls[1].args)
	}
}

func TestCheckPRCollision_PinnedToRepoRoot_AC001(t *testing.T) {
	repoRoot := "/fake/host/repo"
	homeDir := t.TempDir()
	creds := loadTestCreds(t, homeDir, "pin-test")

	exec := &dirCapturingExecutor{
		runFunc: func(name string, args ...string) (string, error) { return "[]", nil },
	}
	r := &Runner{executor: exec, repoRoot: repoRoot, branchName: "golemic/issue-7", creds: creds}

	_, err := r.checkPRCollision()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(exec.dirCalls) != 1 {
		t.Fatalf("expected 1 RunWithEnvInDir call, got %d", len(exec.dirCalls))
	}
	if exec.dirCalls[0].dir != repoRoot {
		t.Errorf("gh pr list used dir %q, want %q", exec.dirCalls[0].dir, repoRoot)
	}
	if exec.dirCalls[0].name != "gh" {
		t.Errorf("expected 'gh', got %q", exec.dirCalls[0].name)
	}
}

// ---------------------------------------------------------------------------
// AC-002: loadIssue pins gh issue view to repoRoot
// ---------------------------------------------------------------------------

func TestLoadIssue_PinnedToRepoRoot_AC002(t *testing.T) {
	repoRoot := "/fake/host/repo"
	homeDir := t.TempDir()
	creds := loadTestCreds(t, homeDir, "pin-test")

	exec := &dirCapturingExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return `{"title":"T","body":"B","state":"OPEN","labels":[]}`, nil
		},
	}
	r := &Runner{executor: exec, repoRoot: repoRoot, issueNum: 5, creds: creds}

	_, err := r.loadIssue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(exec.dirCalls) != 1 {
		t.Fatalf("expected 1 RunWithEnvInDir call, got %d", len(exec.dirCalls))
	}
	if exec.dirCalls[0].dir != repoRoot {
		t.Errorf("gh issue view used dir %q, want %q", exec.dirCalls[0].dir, repoRoot)
	}
	if exec.dirCalls[0].name != "gh" {
		t.Errorf("expected 'gh', got %q", exec.dirCalls[0].name)
	}
}
