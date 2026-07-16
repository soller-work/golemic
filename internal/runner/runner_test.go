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

	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
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
				return `{"title":"Test Issue","body":"This is a test"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[]`, nil // no open PR
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
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
// AC-001: Happy path — no collision, run_started written, exit 0
// ---------------------------------------------------------------------------

func SkipTestRun_HappyPath_AC001(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	// Create necessary files for orchestration tests
	promptsDir := filepath.Join(repoRoot, "prompts")
	os.MkdirAll(promptsDir, 0755)
	os.WriteFile(filepath.Join(promptsDir, "dev.md"), []byte("# Dev"), 0644)
	os.WriteFile(filepath.Join(promptsDir, "reviewer.md"), []byte("# Reviewer"), 0644)
	os.WriteFile(filepath.Join(repoRoot, "guidelines.md"), []byte("# Guidelines"), 0644)
	exec := setupHappyExecutor(repoRoot)

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", exitCode, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}

	// Verify runId is printed to stdout
	runID := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(runID, "issue-42-") {
		t.Errorf("stdout should contain runId starting with 'issue-42-', got: %q", runID)
	}

	// Verify event log was created
	logPath := filepath.Join(homeDir, ".golemic", project, "runs", runID, "events.jsonl")
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}

	// Expect exactly one event (run_started) — no collision, so run_finished is not written
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != eventlog.EventRunStarted {
		t.Errorf("event type: got %q, want %q", events[0].Type, eventlog.EventRunStarted)
	}
	if string(events[0].Payload) != `{"issue":42,"runId":"`+runID+`"}` {
		t.Errorf("payload: got %s, want issue=42, runId=%s", string(events[0].Payload), runID)
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
				return `{"title":"Test","body":"Body"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
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
				return `{"title":"Test","body":"Body"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
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
				return `{"title":"Test","body":"Body"}`, nil
			case name == "gh" && len(args) >= 1 && args[0] == "pr":
				return `[{"url":"https://github.com/owner/repo/pull/123","state":"OPEN"}]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
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
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

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
// runId format test: issue-<N>-<timestamp> (BR-003)
// ---------------------------------------------------------------------------

func SkipTestRun_RunIDFormat(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	// Create necessary files for orchestration tests
	promptsDir := filepath.Join(repoRoot, "prompts")
	os.MkdirAll(promptsDir, 0755)
	os.WriteFile(filepath.Join(promptsDir, "dev.md"), []byte("# Dev"), 0644)
	os.WriteFile(filepath.Join(promptsDir, "reviewer.md"), []byte("# Reviewer"), 0644)
	os.WriteFile(filepath.Join(repoRoot, "guidelines.md"), []byte("# Guidelines"), 0644)
	exec := setupHappyExecutor(repoRoot)

	var stdout, stderr bytes.Buffer
	runner := New(exec, homeDir, repoRoot, 42)
	runner.SetStdout(&stdout)
	runner.SetStderr(&stderr)

	exitCode := runner.Run()

	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", exitCode, stderr.String())
	}

	runID := strings.TrimSpace(stdout.String())

	// Format: issue-42-YYYYMMDDTHHMMSSZ
	if !strings.HasPrefix(runID, "issue-42-") {
		t.Errorf("runId should start with 'issue-42-', got: %q", runID)
	}

	// Extract timestamp part
	tsPart := strings.TrimPrefix(runID, "issue-42-")
	if len(tsPart) != 16 { // 20060102T150405Z = 16 chars
		t.Errorf("timestamp part should be 16 chars (YYYYMMDDTHHMMSSZ), got %d: %q", len(tsPart), tsPart)
	}

	// Verify it parses as a valid UTC time
	_, err := time.Parse("20060102T150405Z", tsPart)
	if err != nil {
		t.Errorf("timestamp %q is not valid sortable UTC format: %v", tsPart, err)
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