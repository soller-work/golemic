// Package agent executes a role as a headless subprocess (pi -p) with correct
// environment, timeout, and transcript capture. It is the invocation layer for
// dev and reviewer roles in the golemic loop.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrTimeout is returned when the agent process exceeds the configured timeout
// and the entire process group has been killed.
var ErrTimeout = errors.New("agent execution timed out")

// ---------------------------------------------------------------------------
// Command factory (injectable for tests)
// ---------------------------------------------------------------------------

// CommandFactory creates *exec.Cmd instances. Defaults to exec.Command (the
// production value: var CommandFactory = exec.Command). Override in tests to
// inject a fake binary without a real pi installation.
var CommandFactory = exec.Command

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// RoleConfig holds all parameters needed to invoke an agent role as a pi
// subprocess. Every field is required; validation is performed at the top of
// RunRole.
type RoleConfig struct {
	Role              string        // "dev" or "reviewer" (informational, used for transcript filenames)
	SystemPromptFile  string        // path to the system prompt file, passed as @<file>
	UserPrompt        string        // the rendered user prompt text (last positional arg)
	WorktreeDir       string        // CWD for the subprocess
	RunID             string        // golemic run identifier, set as GOLEMIC_RUN_ID
	EventLogPath      string        // path to the JSONL event log, set as GOLEMIC_EVENT_LOG
	GHToken           string        // role-specific GitHub token, set as GH_TOKEN
	GolemicBinaryPath string        // path to the golemic binary; its directory is prepended to PATH
	Model             string        // model identifier passed to --model
	Timeout           time.Duration // maximum wall-clock time for the subprocess
	ToolAllowlist     []string      // tool names passed to --tools (e.g. ["read","bash","write","edit"])
	RunsDir           string        // base directory for transcript files (<RunsDir>/<RunID>/<role>.*.log)
}

// TranscriptPaths holds the absolute paths of the captured output files.
type TranscriptPaths struct {
	Stdout string // path to <RunsDir>/<RunID>/<role>.stdout.log
	Stderr string // path to <RunsDir>/<RunID>/<role>.stderr.log
}

// ---------------------------------------------------------------------------
// RunRole
// ---------------------------------------------------------------------------

// RunRole invokes the pi CLI as a subprocess with the given configuration.
//
// The command line is:
//
//	pi -p --append-system-prompt @<systemPromptFile> --tools <allowlist> --model <model> "<userPrompt>"
//
// The subprocess runs with:
//   - CWD = WorktreeDir
//   - Environment: GOLEMIC_RUN_ID, GOLEMIC_EVENT_LOG, GH_TOKEN, PATH (golemic
//     binary dir prepended)
//   - Process group set (Setpgid) so the entire group can be killed on timeout
//
// stdout and stderr are captured to:
//   - <RunsDir>/<RunID>/<role>.stdout.log
//   - <RunsDir>/<RunID>/<role>.stderr.log
//
// If the process exceeds cfg.Timeout, the entire process group is killed
// (SIGKILL to -pgid) and the returned error wraps ErrTimeout. Partial output
// up to the kill point is preserved in the transcript files.
//
// The exit code is returned as-is (informational, not semantically interpreted).
func RunRole(ctx context.Context, cfg RoleConfig) (exitCode int, paths TranscriptPaths, err error) {
	// ---- Validation ----
	// P2-1: Role must be exactly "dev" or "reviewer" (per IF-001).
	if cfg.Role != "dev" && cfg.Role != "reviewer" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: role must be 'dev' or 'reviewer', got %q", cfg.Role)
	}
	if cfg.UserPrompt == "" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: userPrompt must not be empty")
	}
	if cfg.GHToken == "" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: ghToken must not be empty")
	}
	// P3-5: defense-in-depth — ensure paths needed for env construction are set.
	if cfg.EventLogPath == "" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: eventLogPath must not be empty")
	}
	if cfg.GolemicBinaryPath == "" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: golemicBinaryPath must not be empty")
	}
	if cfg.RunsDir == "" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: runsDir must not be empty")
	}
	// P2-8: Prevent RunID path traversal.
	if cfg.RunID == "" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: runID must not be empty")
	}
	if filepath.Base(cfg.RunID) != cfg.RunID {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: runID must not contain path separators, got %q", cfg.RunID)
	}
	// P2-5: Model must not be empty.
	if cfg.Model == "" {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: model must not be empty")
	}
	// P2-4: Tool allowlist must not be empty.
	if len(cfg.ToolAllowlist) == 0 {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: toolAllowlist must not be empty")
	}
	if cfg.Timeout <= 0 {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: timeout must be positive, got %v", cfg.Timeout)
	}
	// P2-2: System prompt file must exist on disk.
	if _, err := os.Stat(cfg.SystemPromptFile); err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: systemPromptFile %q: %w", cfg.SystemPromptFile, err)
	}
	// P2-3: Worktree directory must exist and be a directory.
	if fi, err := os.Stat(cfg.WorktreeDir); err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: worktreeDir %q: %w", cfg.WorktreeDir, err)
	} else if !fi.IsDir() {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: worktreeDir %q is not a directory", cfg.WorktreeDir)
	}

	// ---- Build command ----
	args := []string{
		"-p",
		"--append-system-prompt", "@" + cfg.SystemPromptFile,
		"--tools", strings.Join(cfg.ToolAllowlist, ","),
		"--model", cfg.Model,
		cfg.UserPrompt,
	}
	cmd := CommandFactory("pi", args...)
	cmd.Dir = cfg.WorktreeDir

	// ---- Environment ----
	golemicDir := filepath.Dir(cfg.GolemicBinaryPath)
	cmd.Env = append(
		os.Environ(),
		"GOLEMIC_RUN_ID="+cfg.RunID,
		"GOLEMIC_EVENT_LOG="+cfg.EventLogPath,
		"GH_TOKEN="+cfg.GHToken,
		"PATH="+golemicDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
	)

	// ---- Process group (for timeout kill) ----
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// ---- Transcript files ----
	stdoutPath := filepath.Join(cfg.RunsDir, cfg.RunID, cfg.Role+".stdout.log")
	stderrPath := filepath.Join(cfg.RunsDir, cfg.RunID, cfg.Role+".stderr.log")

	if err := os.MkdirAll(filepath.Join(cfg.RunsDir, cfg.RunID), 0755); err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to create transcript directory: %w", err)
	}

	// P3-6: Use O_NOFOLLOW to refuse opening transcript files through symlinks.
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to create stdout transcript %s: %w", stdoutPath, err)
	}
	defer stdoutFile.Close()

	stderrFile, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to create stderr transcript %s: %w", stderrPath, err)
	}
	defer stderrFile.Close()

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	paths = TranscriptPaths{Stdout: stdoutPath, Stderr: stderrPath}

	// ---- Start ----
	if err := cmd.Start(); err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to start pi process: %w", err)
	}

	// ---- Wait with timeout ----
	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-timeoutCtx.Done():
		// Kill the entire process group (not just the parent).
		// Negative PID means the process group whose leader is cmd.Process.Pid.
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		// Reap the child to avoid zombies. If kill failed, use a secondary
		// timeout to avoid blocking on <-done indefinitely.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		if killErr != nil {
			return 0, paths, fmt.Errorf("%w: role %q timed out after %v, kill failed: %w",
				ErrTimeout, cfg.Role, cfg.Timeout, killErr)
		}
		return 0, paths, fmt.Errorf("%w: role %q timed out after %v", ErrTimeout, cfg.Role, cfg.Timeout)

	case waitErr := <-done:
		if waitErr == nil {
			return 0, paths, nil
		}
		// exec.ExitError is expected for non-zero exit codes — return exit code.
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return exitErr.ExitCode(), paths, nil
		}
		// Other errors (e.g. I/O on the pipes) are unexpected.
		return 0, paths, fmt.Errorf("agent: process wait failed: %w", waitErr)
	}
}