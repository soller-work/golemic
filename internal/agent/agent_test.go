package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	expectedStdout := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.stdout.log")
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
	cfg.Timeout = 1200 * time.Millisecond

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
	expectedStdout := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.stdout.log")
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
	expectedStdout := filepath.Join(cfg.RunsDir, cfg.RunID, "dev.stdout.log")
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