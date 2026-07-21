package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// writeScript creates a temporary executable shell script and returns its path.
// The script is automatically cleaned up when the test ends.
func writeScript(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pi-test.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+content), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeCommandFactory returns a CommandFactory function that runs the given
// scriptPath as the "pi" binary. It also captures the full args (name + args)
// into capturedArgs for later inspection.
func fakeCommandFactory(t *testing.T, scriptPath string, capturedArgs *[]string) {
	t.Helper()
	*capturedArgs = nil
	CommandFactory = func(name string, args ...string) *exec.Cmd {
		*capturedArgs = append([]string{name}, args...)
		cmd := exec.Command(scriptPath, args...)
		return cmd
	}
	// P3-4: Restore CommandFactory on test teardown to avoid cross-test pollution.
	t.Cleanup(func() { CommandFactory = exec.Command })
}

// attemptAwareFactory returns a CommandFactory that runs different scripts per invocation.
// Invocation 0 runs script0, invocation 1 runs script1, etc. Returns *invocations for later assertion.
func attemptAwareFactory(t *testing.T, scripts []string) *int {
	t.Helper()
	invocations := 0
	CommandFactory = func(name string, args ...string) *exec.Cmd {
		idx := invocations
		invocations++
		if idx >= len(scripts) {
			t.Fatalf("unexpected invocation %d (only %d scripts configured)", idx+1, len(scripts))
		}
		cmd := exec.Command(scripts[idx], args...)
		return cmd
	}
	t.Cleanup(func() { CommandFactory = exec.Command })
	return &invocations
}

// captureEnvScript returns a shell script that prints args and selected env vars
// to stdout. stderr is left empty.
func captureEnvScript() string {
	return `echo "ARGS: $@"
echo "GOLEMIC_RUN_ID: ${GOLEMIC_RUN_ID}"
echo "GOLEMIC_EVENT_LOG: ${GOLEMIC_EVENT_LOG}"
echo "GH_TOKEN: ${GH_TOKEN}"
echo "PATH: ${PATH}"
`
}

// Default valid config for most tests.
// Creates a real temp worktree dir and a real temp system prompt file on disk
// so that P2-2 (os.Stat) and P2-3 (IsDir) validations pass.
func defaultRoleConfig(t *testing.T, role string) RoleConfig {
	t.Helper()
	worktreeDir := t.TempDir()
	promptDir := t.TempDir()
	systemPromptFile := filepath.Join(promptDir, role+".md")
	if err := os.WriteFile(systemPromptFile, []byte("system prompt content"), 0644); err != nil {
		t.Fatal(err)
	}
	return RoleConfig{
		Role:              role,
		SystemPromptFile:  systemPromptFile,
		UserPrompt:        "Implement the feature",
		WorktreeDir:       worktreeDir,
		RunID:             "test-run-001",
		EventLogPath:      filepath.Join(t.TempDir(), "events.jsonl"),
		GHToken:           "ghp_test_token_" + role,
		GolemicBinaryPath: "/usr/local/bin/golemic",
		Model:             "z-ai/glm-4.6",
		Timeout:           30 * time.Second,
		ToolAllowlist:     []string{"read", "bash", "write", "edit"},
		RunsDir:           t.TempDir(),
	}
}

// ---------------------------------------------------------------------------
// AC-001: Dev role gets correct command args and env vars
// ---------------------------------------------------------------------------

func TestRunRole_DevArgsAndEnv_AC001(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.ToolAllowlist = []string{"read", "bash", "write", "edit"}

	var capturedArgs []string
	scriptPath := writeScript(t, captureEnvScript())
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}

	// ---- Verify command args (BR-001) ----
	// P3-1: Remove unused dead variable.
	// capturedArgs[0] = "pi" (the name passed to factory)
	// capturedArgs[1:] = the args — verified via transcript content below.
	if len(capturedArgs) < 6 {
		t.Fatalf("expected at least 6 captured args, got %d: %v", len(capturedArgs), capturedArgs)
	}

	// We need to verify the args structure: -p, --append-system-prompt @<file>, --tools <allowlist>, --model <model>, <userPrompt>
	// But the script just echoes them, so we can read the transcript.
	stdout, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout transcript: %v", err)
	}
	stdoutStr := string(stdout)

	// Check for -p flag
	if !strings.Contains(stdoutStr, "-p") {
		t.Errorf("stdout transcript should contain '-p', got: %s", stdoutStr)
	}

	// Check for --append-system-prompt — the @<file> path is dynamic (temp file).
	// We verify the --append-system-prompt flag and the @ prefix, not the exact path.
	if !strings.Contains(stdoutStr, "--append-system-prompt") {
		t.Errorf("stdout transcript should contain '--append-system-prompt', got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "@") {
		t.Errorf("stdout transcript should contain '@' prefix for system prompt, got: %s", stdoutStr)
	}

	// Check for --tools with dev allowlist
	if !strings.Contains(stdoutStr, "--tools") {
		t.Errorf("stdout transcript should contain '--tools', got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "read,bash,write,edit") {
		t.Errorf("stdout transcript should contain dev tool allowlist, got: %s", stdoutStr)
	}

	// Check for --model
	if !strings.Contains(stdoutStr, "--model") {
		t.Errorf("stdout transcript should contain '--model', got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "z-ai/glm-4.6") {
		t.Errorf("stdout transcript should contain model name, got: %s", stdoutStr)
	}

	// Check for user prompt as last positional arg
	if !strings.Contains(stdoutStr, "Implement the feature") {
		t.Errorf("stdout transcript should contain user prompt, got: %s", stdoutStr)
	}

	// ---- Verify environment variables (BR-002) ----
	if !strings.Contains(stdoutStr, "GOLEMIC_RUN_ID: test-run-001") {
		t.Errorf("stdout transcript should contain GOLEMIC_RUN_ID, got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "GOLEMIC_EVENT_LOG:") {
		t.Errorf("stdout transcript should contain GOLEMIC_EVENT_LOG, got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "GH_TOKEN: ghp_test_token_dev") {
		t.Errorf("stdout transcript should contain dev GH_TOKEN, got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "PATH:") {
		t.Errorf("stdout transcript should contain PATH, got: %s", stdoutStr)
	}
	// PATH should have /usr/local/bin prepended
	if !strings.Contains(stdoutStr, "/usr/local/bin:") {
		t.Errorf("stdout transcript should contain golemic binary dir prepended in PATH, got: %s", stdoutStr)
	}

	// Verify transcript files exist
	if _, err := os.Stat(paths.Stdout); err != nil {
		t.Errorf("stdout transcript file should exist: %v", err)
	}
	if _, err := os.Stat(paths.Stderr); err != nil {
		t.Errorf("stderr transcript file should exist: %v", err)
	}

	// Verify transcript paths match expected pattern
	expectedStdout := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.activity.jsonl")
	expectedStderr := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.stderr.log")
	if paths.Stdout != expectedStdout {
		t.Errorf("stdout path: got %q, want %q", paths.Stdout, expectedStdout)
	}
	if paths.Stderr != expectedStderr {
		t.Errorf("stderr path: got %q, want %q", paths.Stderr, expectedStderr)
	}
}

// ---------------------------------------------------------------------------
// AC-004: Reviewer tool allowlist is read,bash (no write,edit)
// ---------------------------------------------------------------------------

func TestRunRole_ReviewerToolAllowlist_AC004(t *testing.T) {
	cfg := defaultRoleConfig(t, "reviewer")
	cfg.ToolAllowlist = []string{"read", "bash"}

	var capturedArgs []string
	scriptPath := writeScript(t, captureEnvScript())
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}

	stdout, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout transcript: %v", err)
	}
	stdoutStr := string(stdout)

	// Verify the --tools argument contains read,bash (not write,edit)
	if !strings.Contains(stdoutStr, "--tools") {
		t.Errorf("stdout transcript should contain '--tools', got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "read,bash") {
		t.Errorf("stdout transcript should contain 'read,bash', got: %s", stdoutStr)
	}
	if strings.Contains(stdoutStr, "write") {
		t.Errorf("stdout transcript should NOT contain 'write' for reviewer, got: %s", stdoutStr)
	}
	if strings.Contains(stdoutStr, "edit") {
		t.Errorf("stdout transcript should NOT contain 'edit' for reviewer, got: %s", stdoutStr)
	}

	// P3-2: Verify all 4 env vars for reviewer role.
	if !strings.Contains(stdoutStr, "GOLEMIC_RUN_ID: test-run-001") {
		t.Errorf("stdout transcript should contain GOLEMIC_RUN_ID, got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "GOLEMIC_EVENT_LOG:") {
		t.Errorf("stdout transcript should contain GOLEMIC_EVENT_LOG, got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "GH_TOKEN: ghp_test_token_reviewer") {
		t.Errorf("stdout transcript should contain reviewer GH_TOKEN, got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "PATH:") {
		t.Errorf("stdout transcript should contain PATH, got: %s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "/usr/local/bin:") {
		t.Errorf("stdout transcript should contain golemic binary dir prepended in PATH, got: %s", stdoutStr)
	}
}

// ---------------------------------------------------------------------------
// AC-002: Timeout kills process group; error wraps ErrTimeout; partial
// transcripts exist
// ---------------------------------------------------------------------------

func TestRunRole_Timeout_AC002(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 3 * time.Second

	markerDir := t.TempDir()
	markerFile := filepath.Join(markerDir, "output.txt")

	// Script that writes to a marker file (direct file I/O, no stdio buffering)
	// before sleeping forever. The marker serves as proof of partial execution.
	sleepForeverScript := fmt.Sprintf(
		"printf 'before_sleep\\n' > %s\nwhile true; do sleep 3600; done",
		markerFile,
	)
	scriptPath := writeScript(t, sleepForeverScript)

	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	// ---- Verify timeout error wraps ErrTimeout ----
	if err == nil {
		t.Fatal("expected error from timeout, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should contain 'timed out', got: %v", err)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("error should wrap ErrTimeout, got: %v", err)
	}

	// ---- Verify exit code is 0 on timeout (informational) ----
	if exitCode != 0 {
		t.Errorf("exit code on timeout: got %d, want 0", exitCode)
	}

	// ---- Verify transcript files exist (partial output) ----
	if _, statErr := os.Stat(paths.Stdout); statErr != nil {
		t.Errorf("stdout transcript should exist after timeout: %v", statErr)
	}
	if _, statErr := os.Stat(paths.Stderr); statErr != nil {
		t.Errorf("stderr transcript should exist after timeout: %v", statErr)
	}

	// P3-3: Verify partial output via marker file (avoids shell stdio buffering).
	markerBytes, readErr := os.ReadFile(markerFile)
	if readErr != nil {
		t.Errorf("marker file %q should exist after timeout (partial execution): %v", markerFile, readErr)
	} else if !strings.Contains(string(markerBytes), "before_sleep") {
		t.Errorf("marker file should contain 'before_sleep', got: %q", string(markerBytes))
	}

	// ---- Verify transcript paths are correct ----
	expectedStdout := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.activity.jsonl")
	expectedStderr := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.stderr.log")
	if paths.Stdout != expectedStdout {
		t.Errorf("stdout path: got %q, want %q", paths.Stdout, expectedStdout)
	}
	if paths.Stderr != expectedStderr {
		t.Errorf("stderr path: got %q, want %q", paths.Stderr, expectedStderr)
	}
}

// ---------------------------------------------------------------------------
// AC-003: Transcript files written to correct paths with correct content
// ---------------------------------------------------------------------------

func TestRunRole_Transcripts_AC003(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")

	// Script that writes specific content to stdout and stderr, then exits with code 42
	scriptContent := `echo "hello stdout"
>&2 echo "hello stderr"
exit 42`
	scriptPath := writeScript(t, scriptContent)
	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	// No error expected (exit 42 is non-zero but not a startup failure)
	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}
	if exitCode != 42 {
		t.Errorf("exit code: got %d, want 42", exitCode)
	}

	// ---- Verify transcript files exist at correct paths ----
	expectedStdout := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.activity.jsonl")
	expectedStderr := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.stderr.log")

	if paths.Stdout != expectedStdout {
		t.Errorf("stdout path: got %q, want %q", paths.Stdout, expectedStdout)
	}
	if paths.Stderr != expectedStderr {
		t.Errorf("stderr path: got %q, want %q", paths.Stderr, expectedStderr)
	}

	// ---- Verify transcript content ----
	stdoutBytes, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout transcript: %v", err)
	}
	if string(stdoutBytes) != "hello stdout\n" {
		t.Errorf("stdout content: got %q, want %q", string(stdoutBytes), "hello stdout\n")
	}

	stderrBytes, err := os.ReadFile(paths.Stderr)
	if err != nil {
		t.Fatalf("failed to read stderr transcript: %v", err)
	}
	if string(stderrBytes) != "hello stderr\n" {
		t.Errorf("stderr content: got %q, want %q", string(stderrBytes), "hello stderr\n")
	}

	// Verify stderr contains the expected error message (exit 42)
	// Note: The error message from the shell is not captured via cmd.Stderr
	// because we redirect stderr to the file. The exit code is captured by
	// cmd.Wait() returning an ExitError.
}

// ---------------------------------------------------------------------------
// Validation: role must be "dev" or "reviewer"
// ---------------------------------------------------------------------------

func TestRunRole_Validation_InvalidRole(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Role = "admin"

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
	if !strings.Contains(err.Error(), "role must be 'dev' or 'reviewer'") {
		t.Errorf("error should mention valid roles, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty role
// ---------------------------------------------------------------------------

func TestRunRole_Validation_EmptyRole(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Role = ""

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for empty role, got nil")
	}
	if !strings.Contains(err.Error(), "role must be 'dev' or 'reviewer'") {
		t.Errorf("error should mention invalid role, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty user prompt
// ---------------------------------------------------------------------------

func TestRunRole_Validation_EmptyUserPrompt(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.UserPrompt = ""

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for empty user prompt, got nil")
	}
	if !strings.Contains(err.Error(), "userPrompt must not be empty") {
		t.Errorf("error should mention empty user prompt, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty worktree dir
// ---------------------------------------------------------------------------

func TestRunRole_Validation_EmptyWorktreeDir(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.WorktreeDir = ""

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for empty worktree dir, got nil")
	}
	if !strings.Contains(err.Error(), "worktreeDir") {
		t.Errorf("error should mention worktreeDir, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty GH token
// ---------------------------------------------------------------------------

func TestRunRole_Validation_EmptyGHToken(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.GHToken = ""

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for empty GH token, got nil")
	}
	if !strings.Contains(err.Error(), "ghToken must not be empty") {
		t.Errorf("error should mention empty ghToken, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation: zero/negative timeout
// ---------------------------------------------------------------------------

func TestRunRole_Validation_ZeroTimeout(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 0

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for zero timeout, got nil")
	}
	if !strings.Contains(err.Error(), "timeout must be positive") {
		t.Errorf("error should mention positive timeout, got: %v", err)
	}
}

func TestRunRole_Validation_NegativeTimeout(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = -1 * time.Second

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for negative timeout, got nil")
	}
	if !strings.Contains(err.Error(), "timeout must be positive") {
		t.Errorf("error should mention positive timeout, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty runs dir
// ---------------------------------------------------------------------------

func TestRunRole_Validation_EmptyRunsDir(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.RunsDir = ""

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for empty runsDir, got nil")
	}
	if !strings.Contains(err.Error(), "runsDir must not be empty") {
		t.Errorf("error should mention empty runsDir, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty run ID
// ---------------------------------------------------------------------------

func TestRunRole_Validation_EmptyRunID(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.RunID = ""

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for empty runID, got nil")
	}
	if !strings.Contains(err.Error(), "runID must not be empty") {
		t.Errorf("error should mention empty runID, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GH_TOKEN is never in command-line args (security check)
// ---------------------------------------------------------------------------

func TestRunRole_GHTokenNotInArgs(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.GHToken = "ghp_super_secret_12345"

	var capturedArgs []string
	scriptPath := writeScript(t, captureEnvScript())
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)
	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}

	// Check that no arg contains the token value
	for _, arg := range capturedArgs {
		if strings.Contains(arg, "ghp_super_secret_12345") {
			t.Errorf("GH_TOKEN value found in command-line arg: %q", arg)
		}
	}
}

// ---------------------------------------------------------------------------
// GH_TOKEN is never in transcript paths (security check)
// ---------------------------------------------------------------------------

func TestRunRole_GHTokenNotInTranscriptPaths(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.GHToken = "ghp_super_secret_12345"

	var capturedArgs []string
	scriptPath := writeScript(t, captureEnvScript())
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	_, paths, err := RunRole(ctx, cfg)
	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}

	if strings.Contains(paths.Stdout, "ghp_super_secret_12345") {
		t.Errorf("GH_TOKEN found in stdout transcript path: %s", paths.Stdout)
	}
	if strings.Contains(paths.Stderr, "ghp_super_secret_12345") {
		t.Errorf("GH_TOKEN found in stderr transcript path: %s", paths.Stderr)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation propagates (not just timeout)
// ---------------------------------------------------------------------------

func TestRunRole_ContextCancelled(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 30 * time.Second // longer than the test

	sleepForeverScript := `while true; do sleep 3600; done`
	scriptPath := writeScript(t, sleepForeverScript)
	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, _, err := RunRole(ctx, cfg)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	// Should wrap ErrTimeout since context cancellation triggers the same path
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("error should wrap ErrTimeout, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// P2-6: Process group kill — verify background child processes are actually
// killed (not just the parent script).
// ---------------------------------------------------------------------------

func TestRunRole_ProcessGroupKilled(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 1200 * time.Millisecond // generous enough to ensure script executes before kill

	pidDir := t.TempDir()
	pidFile := filepath.Join(pidDir, "child.pid")

	// Script that forks a background child, writes the child's PID to a known
	// file, then blocks via wait. Both parent and child must be killed by the
	// process-group kill on timeout.
	scriptContent := fmt.Sprintf(
		"sleep 60 >/dev/null 2>&1 &\nprintf '%%d\\n' $! > %s\nwait",
		pidFile,
	)
	scriptPath := writeScript(t, scriptContent)

	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	start := time.Now()
	_, _, err := RunRole(ctx, cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("error should wrap ErrTimeout, got: %v", err)
	}

	// The process should be killed within a reasonable time after the timeout.
	// Allow some overhead for OS scheduling + kill + reap.
	if elapsed > 5*time.Second {
		t.Errorf("process took too long to die: %v (expected < 5s)", elapsed)
	}

	// P2-6: Verify background child process is actually dead.
	pidBytes, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("failed to read child PID file %s: %v — process may have been killed before writing PID", pidFile, readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("failed to parse child PID from %q: %v", string(pidBytes), parseErr)
	}

	// Kill(pid, 0) checks if the process exists without sending a signal.
	// ESRCH means "no such process" — the child was successfully killed.
	// SIGKILL delivery and teardown of the group's background child are
	// asynchronous with respect to the parent's reap, so poll briefly rather
	// than asserting the child is gone the instant RunRole returns.
	var killErr error
	for deadline := time.Now().Add(2 * time.Second); ; {
		killErr = syscall.Kill(pid, 0)
		if errors.Is(killErr, syscall.ESRCH) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if killErr == nil {
		t.Errorf("background child process (PID %d) is still alive after timeout (kill -0 did not return ESRCH)", pid)
	} else if !errors.Is(killErr, syscall.ESRCH) {
		t.Errorf("unexpected error checking child process PID %d: %v (want ESRCH)", pid, killErr)
	}
}

// ---------------------------------------------------------------------------
// Exit code is returned without semantic interpretation (BR-006)
// ---------------------------------------------------------------------------

func TestRunRole_ExitCodeReturned(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")

	// Script that exits with code 7 (arbitrary non-zero)
	scriptContent := `exit 7`
	scriptPath := writeScript(t, scriptContent)
	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, _, err := RunRole(ctx, cfg)

	if err != nil {
		t.Fatalf("RunRole should not error on non-zero exit, got: %v", err)
	}
	if exitCode != 7 {
		t.Errorf("exit code: got %d, want 7", exitCode)
	}
}

// ---------------------------------------------------------------------------
// CWD is set to WorktreeDir (BR-001)
// ---------------------------------------------------------------------------

func TestRunRole_WorktreeDirAsCWD(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.WorktreeDir = t.TempDir()

	// Create a marker file in the worktree dir
	markerFile := filepath.Join(cfg.WorktreeDir, "marker.txt")
	if err := os.WriteFile(markerFile, []byte("marker"), 0644); err != nil {
		t.Fatal(err)
	}

	// Script that verifies the CWD by checking if the marker file exists
	scriptContent := `if [ -f marker.txt ]; then echo "CWD_OK"; else echo "CWD_MISMATCH"; fi`
	scriptPath := writeScript(t, scriptContent)
	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	_, paths, err := RunRole(ctx, cfg)
	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}

	stdout, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout transcript: %v", err)
	}
	if !strings.Contains(string(stdout), "CWD_OK") {
		t.Errorf("CWD should be set to worktreeDir, got stdout: %s", string(stdout))
	}
}

// ---------------------------------------------------------------------------
// Restore CommandFactory after tests
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	// Run tests with the default CommandFactory
	code := m.Run()
	// Restore to avoid side effects
	CommandFactory = exec.Command
	os.Exit(code)
}



// argContains checks if args contains flag followed by want value.
func argContains(args []string, flag, want string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// AC-6: Default idle timeout is 300s
// ---------------------------------------------------------------------------

func TestParseIdleTimeout_DefaultIs300s_AC6(t *testing.T) {
	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "")
	got := parseIdleTimeout()
	want := 300 * time.Second
	if got != want {
		t.Errorf("default idle timeout: got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// AC-1: pi launched with --mode json and deterministic --session-id
// ---------------------------------------------------------------------------

func TestRunRole_ModeJsonAndSessionID_AC1(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.RunID = "test-run-123"
	cfg.TurnID = 5

	var capturedArgs []string
	scriptPath := writeScript(t, captureEnvScript())
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, _, err := RunRole(ctx, cfg)

	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}

	// AC-1: Verify --mode json and --session-id are present
	expectedSessionID := sanitizeSessionID("test-run-123-dev-5")
	if !argContains(capturedArgs, "--mode", "json") {
		t.Errorf("args missing '--mode json', got: %v", capturedArgs)
	}
	if !argContains(capturedArgs, "--session-id", expectedSessionID) {
		t.Errorf("args missing '--session-id %s', got: %v", expectedSessionID, capturedArgs)
	}
}

// ---------------------------------------------------------------------------
// AC-3: Silent pi killed→resumed with identical session-id, eventually ErrStalled
// ---------------------------------------------------------------------------

func TestRunRole_StallRetryWithSameSessionID_AC3(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.RunID = "run-retry-test"
	cfg.TurnID = 7
	cfg.Timeout = 5 * time.Second

	sleepForeverScript := `while true; do sleep 3600; done`
	scriptPath := writeScript(t, sleepForeverScript)

	// Capture all invocations and their args
	var allInvocations [][]string
	var mu sync.Mutex
	invocationCount := 0
	CommandFactory = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		allInvocations = append(allInvocations, append([]string{name}, args...))
		invocationCount++
		mu.Unlock()
		cmd := exec.Command(scriptPath, args...)
		return cmd
	}
	t.Cleanup(func() { CommandFactory = exec.Command })

	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "1")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "2")

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)

	if err == nil {
		t.Fatal("expected ErrStalled, got nil")
	}
	if !errors.Is(err, ErrStalled) {
		t.Errorf("error should wrap ErrStalled, got: %v", err)
	}

	// AC-3: Assert 3 total invocations (1 initial + 2 retries)
	if invocationCount != 3 {
		t.Errorf("invocation count: got %d, want 3 (1 initial + 2 retries)", invocationCount)
	}

	// AC-3: Extract and verify all session-ids are identical
	expectedSessionID := sanitizeSessionID("run-retry-test-dev-7")
	for idx, invocation := range allInvocations {
		var sessionID string
		for i := 0; i < len(invocation)-1; i++ {
			if invocation[i] == "--session-id" {
				sessionID = invocation[i+1]
				break
			}
		}
		if sessionID != expectedSessionID {
			t.Errorf("invocation %d session-id: got %q, want %q", idx, sessionID, expectedSessionID)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-2: Fake pi with growing output never killed for stalling
// ---------------------------------------------------------------------------

func TestRunRole_SteadyOutputNotKilled_AC2(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 5 * time.Second

	// Emit output every 200ms (< 1s idle timeout), run for ~2s
	steadyScript := `
for i in 1 2 3 4 5 6 7 8 9 10; do
  echo "output $i"
  sleep 0.2
done
exit 0
`
	scriptPath := writeScript(t, steadyScript)
	invocations := attemptAwareFactory(t, []string{scriptPath})

	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "1")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "2")

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	// AC-2: Should complete successfully without stalling
	if err != nil {
		t.Fatalf("steady output should not trigger stall, got error: %v", err)
	}

	if exitCode != 0 {
		t.Errorf("exit code: got %d, want 0", exitCode)
	}

	// AC-2: Should be invoked exactly once (no stalls, no retries)
	if *invocations != 1 {
		t.Errorf("subprocess invocation count: got %d, want 1 (no stalls)", *invocations)
	}

	// AC-2: Verify output is present
	stdoutBytes, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	if !strings.Contains(string(stdoutBytes), "output 1") {
		t.Errorf("stdout should contain output, got: %q", string(stdoutBytes))
	}
}

// ---------------------------------------------------------------------------
// AC-4: Reviewer role has correct transcript paths
// ---------------------------------------------------------------------------

func TestRunRole_ReviewerActivityJsonl_AC4(t *testing.T) {
	cfg := defaultRoleConfig(t, "reviewer")
	cfg.ToolAllowlist = []string{"read", "bash"}

	var capturedArgs []string
	scriptPath := writeScript(t, captureEnvScript())
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}

	// AC-4: Verify stdout (activity) transcript path ends in .activity.jsonl
	if !strings.HasSuffix(paths.Stdout, "reviewer.activity.jsonl") {
		t.Errorf("stdout path should end in 'reviewer.activity.jsonl', got: %q", paths.Stdout)
	}

	// AC-4: Verify stderr transcript path ends in .stderr.log
	if !strings.HasSuffix(paths.Stderr, "reviewer.stderr.log") {
		t.Errorf("stderr path should end in 'reviewer.stderr.log', got: %q", paths.Stderr)
	}

	// AC-4: Verify files exist
	if _, err := os.Stat(paths.Stdout); err != nil {
		t.Errorf("stdout transcript should exist: %v", err)
	}
	if _, err := os.Stat(paths.Stderr); err != nil {
		t.Errorf("stderr transcript should exist: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stall detection tests (AC-1 through AC-7)
// ---------------------------------------------------------------------------

// AC-1: subprocess stalls (no output) → RunRole kills it, retries, detects stall
func TestRunRole_StallDetection_AC1(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 5 * time.Second

	sleepForeverScript := `while true; do sleep 3600; done`
	scriptPath := writeScript(t, sleepForeverScript)

	// Attempt-aware factory: all invocations run the same stall script
	invocations := attemptAwareFactory(t, []string{scriptPath, scriptPath})

	// Lower poll interval for fast test
	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	// Capture the per-killed-attempt stall diagnostic.
	var stallLog bytes.Buffer
	origWriter := stallLogWriter
	stallLogWriter = &stallLog
	t.Cleanup(func() { stallLogWriter = origWriter })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "1")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "1")

	ctx := context.Background()
	_, paths, err := RunRole(ctx, cfg)

	if err == nil {
		t.Fatal("expected error from stall detection, got nil")
	}
	if !errors.Is(err, ErrStalled) {
		t.Errorf("error should wrap ErrStalled, got: %v", err)
	}

	// AC-1: Assert subprocess invoked exactly 1 + maxStallRetries = 2 times
	if *invocations != 2 {
		t.Errorf("subprocess invocation count: got %d, want 2 (1 initial + 1 retry)", *invocations)
	}

	// AC-1: Assert one stall diagnostic per killed attempt, each naming the role,
	// the attempt index, and the idle threshold.
	logLines := strings.Count(stallLog.String(), "stalled at attempt")
	if logLines != 2 {
		t.Errorf("stall log: got %d 'stalled at attempt' lines, want 2\n%s", logLines, stallLog.String())
	}
	for _, want := range []string{`role "dev"`, "stalled at attempt 0", "stalled at attempt 1"} {
		if !strings.Contains(stallLog.String(), want) {
			t.Errorf("stall log should contain %q, got:\n%s", want, stallLog.String())
		}
	}

	if _, err := os.Stat(paths.Stdout); err != nil {
		t.Errorf("stdout transcript should exist: %v", err)
	}
}

// AC-1 (regression): a subprocess that writes once immediately then hangs must be
// detected as stalled at ~idleTimeout — anchored to the actual last write, not the
// poll time. With Timeout set between idleTimeout and idleTimeout+pollInterval, the
// pre-fix behavior (anchoring to poll time) would return ErrTimeout; the fix returns
// ErrStalled.
func TestRunRole_StallAnchoredToLastWrite_AC1b(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	// idle=2s, poll=1.5s. The write lands well inside the first poll window, so the
	// fix (anchor to the actual write ~t0) detects the stall at the ~3.0s poll; the
	// pre-fix behaviour (anchor to the first poll at ~1.5s) would only detect it at
	// the ~4.5s poll. A 3.75s wall-clock timeout (750ms margin either side, robust
	// under -race) therefore yields ErrStalled with the fix but ErrTimeout without it.
	cfg.Timeout = 3750 * time.Millisecond

	writeOnceThenHang := `printf 'hello'
while true; do sleep 3600; done`
	scriptPath := writeScript(t, writeOnceThenHang)
	invocations := attemptAwareFactory(t, []string{scriptPath})

	origPoll := pollInterval
	pollInterval = 1500 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "2")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "0")

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)

	if !errors.Is(err, ErrStalled) {
		t.Errorf("expected ErrStalled (idle window anchored to last write), got: %v", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("should not be classified as wall-clock timeout: %v", err)
	}
	if *invocations != 1 {
		t.Errorf("subprocess invocation count: got %d, want 1", *invocations)
	}
}

// AC-2: all attempts stall → returns ErrStalled (not ErrTimeout)
func TestRunRole_AllAttemptsStall_AC2(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 5 * time.Second

	sleepForeverScript := `while true; do sleep 3600; done`
	scriptPath := writeScript(t, sleepForeverScript)
	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "1")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "0")

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)

	if err == nil {
		t.Fatal("expected ErrStalled, got nil")
	}
	if !errors.Is(err, ErrStalled) {
		t.Errorf("error should wrap ErrStalled, got: %v", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("error should NOT wrap ErrTimeout: %v", err)
	}
}

// AC-3: first attempt stalls, retry produces output and succeeds
func TestRunRole_StallThenSuccess_AC3(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 5 * time.Second

	// Attempt 1: write a distinctive marker to stdout (the transcript), then hang so it stalls.
	stallScript := `printf 'MARKER_ATTEMPT_ONE'
while true; do sleep 3600; done`
	// Attempt 2: write a different marker to stdout, emit success, exit 0.
	successScript := `printf 'MARKER_ATTEMPT_TWO'
echo "success"
exit 0`

	stallPath := writeScript(t, stallScript)
	successPath := writeScript(t, successScript)

	// Attempt-aware factory: attempt 1 stalls, attempt 2 succeeds
	invocations := attemptAwareFactory(t, []string{stallPath, successPath})

	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "1")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "1")

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	if err != nil {
		t.Fatalf("RunRole should succeed after retry, got error: %v", err)
	}

	if exitCode != 0 {
		t.Errorf("exit code: got %d, want 0", exitCode)
	}

	// AC-3: Assert invoked exactly 2 times (1 stall + 1 success)
	if *invocations != 2 {
		t.Errorf("subprocess invocation count: got %d, want 2 (1 stall + 1 success)", *invocations)
	}

	stdoutBytes, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	if !strings.Contains(string(stdoutBytes), "MARKER_ATTEMPT_TWO") {
		t.Errorf("stdout should contain attempt-2 marker, got: %q", string(stdoutBytes))
	}

	// AC-3: BR-4 truncation — final transcript must contain ONLY attempt 2's output,
	// proving attempt 1's transcript was truncated on retry.
	if strings.Contains(string(stdoutBytes), "MARKER_ATTEMPT_ONE") {
		t.Errorf("stdout should NOT contain attempt-1 marker (must be truncated on retry), got: %q", string(stdoutBytes))
	}
}

// AC-4: steady output (emitting within idle window), never stalls → completes normally
func TestRunRole_SteadyOutput_AC4(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 5 * time.Second

	// Emit every 300ms (< 1s idle timeout), run for ~2s total (spans idle window)
	// This ensures the idle clock keeps resetting and stall never triggers.
	steadyScript := `
for i in 1 2 3 4 5 6 7; do
  echo "output $i"
  sleep 0.3
done
exit 0
`
	scriptPath := writeScript(t, steadyScript)
	invocations := attemptAwareFactory(t, []string{scriptPath})

	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "1")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "1")

	ctx := context.Background()
	exitCode, paths, err := RunRole(ctx, cfg)

	if err != nil {
		t.Fatalf("steady output should not trigger stall, got error: %v", err)
	}

	if exitCode != 0 {
		t.Errorf("exit code: got %d, want 0", exitCode)
	}

	// AC-4: Assert invoked exactly once (no stalls, no retries)
	if *invocations != 1 {
		t.Errorf("subprocess invocation count: got %d, want 1 (no stalls)", *invocations)
	}

	stdoutBytes, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	if !strings.Contains(string(stdoutBytes), "output 1") {
		t.Errorf("stdout should contain output, got: %q", string(stdoutBytes))
	}
}

// AC-5: produces output but exceeds wall-clock timeout → ErrTimeout (terminal, not retried)
func TestRunRole_TimeoutNotRetried_AC5(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 2 * time.Second

	// Output immediately, then sleep forever
	timeoutScript := `echo "started" && while true; do sleep 3600; done`
	scriptPath := writeScript(t, timeoutScript)
	invocations := attemptAwareFactory(t, []string{scriptPath})

	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	// Long idle timeout so stall detection doesn't fire
	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "90")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "2")

	ctx := context.Background()
	_, paths, err := RunRole(ctx, cfg)

	if err == nil {
		t.Fatal("expected ErrTimeout, got nil")
	}

	if !errors.Is(err, ErrTimeout) {
		t.Errorf("error should wrap ErrTimeout, got: %v", err)
	}
	// Should NOT be stalled (wall-clock timeout fires first)
	if errors.Is(err, ErrStalled) {
		t.Errorf("timeout should not be retried as stall: %v", err)
	}

	// AC-5: Assert invoked exactly once (wall-clock timeout terminal, not retried)
	if *invocations != 1 {
		t.Errorf("subprocess invocation count: got %d, want 1 (timeout is terminal)", *invocations)
	}

	stdoutBytes, err := os.ReadFile(paths.Stdout)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}
	if !strings.Contains(string(stdoutBytes), "started") {
		t.Errorf("stdout should contain 'started', got: %q", string(stdoutBytes))
	}
}

// AC-7: custom env vars (GOLEMIC_AGENT_MAX_STALL_RETRIES) are parsed and honored
func TestRunRole_CustomEnvVars_AC7(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 5 * time.Second

	sleepScript := `while true; do sleep 3600; done`
	scriptPath := writeScript(t, sleepScript)

	// Spec AC-7: MAX_STALL_RETRIES=1 means exactly 2 total attempts (1 initial + 1 retry)
	invocations := attemptAwareFactory(t, []string{scriptPath, scriptPath})

	origPoll := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = origPoll })

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "1")
	t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "1") // spec value: exactly 2 total attempts

	ctx := context.Background()
	_, _, err := RunRole(ctx, cfg)

	if err == nil {
		t.Fatal("expected ErrStalled, got nil")
	}
	if !errors.Is(err, ErrStalled) {
		t.Errorf("error should wrap ErrStalled, got: %v", err)
	}

	// AC-7: Assert invocation count matches 1 + GOLEMIC_AGENT_MAX_STALL_RETRIES
	expectedInvocations := 1 + 1 // 1 initial + 1 retry (per spec AC-7)
	if *invocations != expectedInvocations {
		t.Errorf("subprocess invocation count: got %d, want %d (1 initial + 1 configured retry)", *invocations, expectedInvocations)
	}
}

// Test env var parsing
func TestParseIdleTimeout_Invalid(t *testing.T) {
	origPoll := pollInterval
	t.Cleanup(func() { pollInterval = origPoll })
	pollInterval = 20 * time.Millisecond

	tests := []struct {
		name     string
		envValue string
		want     time.Duration
	}{
		{"empty", "", defaultIdleTimeout},
		{"negative", "-1", defaultIdleTimeout},
		{"zero", "0", defaultIdleTimeout},
		{"non-numeric", "abc", defaultIdleTimeout},
		{"below poll interval", "0.01", defaultIdleTimeout},
		{"valid (>= poll)", "1", 1 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", tt.envValue)
			} else {
				t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "")
			}
			got := parseIdleTimeout()
			if got != tt.want {
				t.Errorf("parseIdleTimeout(): got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseMaxStallRetries_Invalid(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     int
	}{
		{"empty", "", defaultMaxStallRetries},
		{"negative", "-1", defaultMaxStallRetries},
		{"non-numeric", "abc", defaultMaxStallRetries},
		{"valid", "5", 5},
		{"zero", "0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", tt.envValue)
			} else {
				t.Setenv("GOLEMIC_AGENT_MAX_STALL_RETRIES", "")
			}
			got := parseMaxStallRetries()
			if got != tt.want {
				t.Errorf("parseMaxStallRetries(): got %d, want %d", got, tt.want)
			}
		})
	}
}

// TestParseIdleTimeout_ProductionBoundary validates the production boundary case:
// with pollInterval=30s and IDLE_TIMEOUT_SEC=30, should accept (not default).
func TestParseIdleTimeout_ProductionBoundary(t *testing.T) {
	origPoll := pollInterval
	t.Cleanup(func() { pollInterval = origPoll })
	pollInterval = 30 * time.Second

	t.Setenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC", "30")
	got := parseIdleTimeout()
	want := 30 * time.Second
	if got != want {
		t.Errorf("parseIdleTimeout with production boundary (30s == 30s poll interval): got %v, want %v", got, want)
	}
}
