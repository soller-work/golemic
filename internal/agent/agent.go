// Package agent executes a role as a headless subprocess (pi -p) with correct
// environment, timeout, and transcript capture. It is the invocation layer for
// dev and reviewer roles in the golemic loop.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// ErrStalled is returned when the agent process produces no output for the idle
// threshold over the max number of retries and the entire process group has been
// killed after each stall. Stalls are transient; wall-clock timeouts are terminal.
var ErrStalled = errors.New("agent execution stalled")

// ---------------------------------------------------------------------------
// Command factory (injectable for tests)
// ---------------------------------------------------------------------------

// CommandFactory creates *exec.Cmd instances. Defaults to exec.Command (the
// production value: var CommandFactory = exec.Command). Override in tests to
// inject a fake binary without a real pi installation.
var CommandFactory = exec.Command

// ---------------------------------------------------------------------------
// Stall detection configuration
// ---------------------------------------------------------------------------

// pollInterval is the interval for checking transcript growth (30s in production).
// Override in tests via direct assignment (mirrors CommandFactory seam).
var pollInterval = 30 * time.Second

// stallLogWriter receives the per-killed-attempt stall diagnostic. Defaults to
// os.Stderr; override in tests to capture and assert the emitted line.
var stallLogWriter io.Writer = os.Stderr

const (
	// defaultIdleTimeout is the default idle timeout (90s).
	defaultIdleTimeout = 90 * time.Second
	// defaultMaxStallRetries is the default max number of retries on stall (2).
	defaultMaxStallRetries = 2
)

// parseIdleTimeout reads GOLEMIC_AGENT_IDLE_TIMEOUT_SEC from environment.
// Returns the timeout duration, or defaultIdleTimeout if absent or invalid.
// If the parsed value is <= 0 or < pollInterval, returns defaultIdleTimeout.
func parseIdleTimeout() time.Duration {
	envVal := os.Getenv("GOLEMIC_AGENT_IDLE_TIMEOUT_SEC")
	if envVal == "" {
		return defaultIdleTimeout
	}
	sec, err := strconv.Atoi(envVal)
	if err != nil || sec <= 0 || time.Duration(sec)*time.Second < pollInterval {
		return defaultIdleTimeout
	}
	return time.Duration(sec) * time.Second
}

// parseMaxStallRetries reads GOLEMIC_AGENT_MAX_STALL_RETRIES from environment.
// Returns the max retries count, or defaultMaxStallRetries if absent or invalid.
func parseMaxStallRetries() int {
	envVal := os.Getenv("GOLEMIC_AGENT_MAX_STALL_RETRIES")
	if envVal == "" {
		return defaultMaxStallRetries
	}
	count, err := strconv.Atoi(envVal)
	if err != nil || count < 0 {
		return defaultMaxStallRetries
	}
	return count
}

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
	TurnID            int           // monotonic turn identifier, exported as GOLEMIC_TURN_ID
}

// TranscriptPaths holds the absolute paths of the captured output files.
type TranscriptPaths struct {
	Stdout string // path to <RunsDir>/<RunID>/<role>.stdout.log
	Stderr string // path to <RunsDir>/<RunID>/<role>.stderr.log
}

// transcriptByteSize returns the combined byte size of the stdout and stderr
// transcripts and the most recent modification time across them. Using the open
// file handles avoids TOCTOU races during execution. The mod time lets the caller
// anchor the idle window to the actual last write rather than the poll time.
func transcriptByteSize(stdoutFile, stderrFile *os.File) (int64, time.Time) {
	var size int64
	var modTime time.Time
	if fi, err := stdoutFile.Stat(); err == nil {
		size += fi.Size()
		modTime = fi.ModTime()
	}
	if fi, err := stderrFile.Stat(); err == nil {
		size += fi.Size()
		if fi.ModTime().After(modTime) {
			modTime = fi.ModTime()
		}
	}
	return size, modTime
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
// Stall detection: if the combined transcript byte size doesn't grow for the
// idle threshold (default 90s, configurable via GOLEMIC_AGENT_IDLE_TIMEOUT_SEC),
// the process group is killed and the attempt is retried (up to maxStallRetries
// times, default 2, configurable via GOLEMIC_AGENT_MAX_STALL_RETRIES). Fresh
// transcript files are truncated per attempt. If all attempts stall, returns
// ErrStalled. If a producing-but-slow process exceeds wall-clock Timeout, that
// is terminal (not retried) and returns ErrTimeout.
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

	// ---- Prepare static values ----
	args := []string{
		"-p",
		"--append-system-prompt", "@" + cfg.SystemPromptFile,
		"--tools", strings.Join(cfg.ToolAllowlist, ","),
		"--model", cfg.Model,
		cfg.UserPrompt,
	}

	stdoutPath := filepath.Join(cfg.RunsDir, cfg.RunID, cfg.Role+".stdout.log")
	stderrPath := filepath.Join(cfg.RunsDir, cfg.RunID, cfg.Role+".stderr.log")
	golemicDir := filepath.Dir(cfg.GolemicBinaryPath)

	if err := os.MkdirAll(filepath.Join(cfg.RunsDir, cfg.RunID), 0755); err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to create transcript directory: %w", err)
	}

	paths = TranscriptPaths{Stdout: stdoutPath, Stderr: stderrPath}

	// ---- Retry loop with stall detection ----
	idleTimeout := parseIdleTimeout()
	maxStallRetries := parseMaxStallRetries()

	for attempt := 0; attempt <= maxStallRetries; attempt++ {
		// Truncate transcript files fresh for each attempt
		stdoutFile, err := os.OpenFile(stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
		if err != nil {
			return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to create stdout transcript %s: %w", stdoutPath, err)
		}
		stderrFile, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
		if err != nil {
			stdoutFile.Close() //nolint:errcheck
			return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to create stderr transcript %s: %w", stderrPath, err)
		}

		cmd := CommandFactory("pi", args...)
		cmd.Dir = cfg.WorktreeDir
		cmd.Env = append(
			os.Environ(),
			"GOLEMIC_RUN_ID="+cfg.RunID,
			"GOLEMIC_EVENT_LOG="+cfg.EventLogPath,
			"GOLEMIC_TURN_ID="+strconv.Itoa(cfg.TurnID),
			"GH_TOKEN="+cfg.GHToken,
			"PATH="+golemicDir+string(filepath.ListSeparator)+os.Getenv("PATH"),
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdout = stdoutFile
		cmd.Stderr = stderrFile

		if err := cmd.Start(); err != nil {
			stdoutFile.Close()  //nolint:errcheck
			stderrFile.Close()  //nolint:errcheck
			return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to start pi process: %w", err)
		}

		// ---- Wait with timeout and stall detection ----
		timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()

		pollTicker := time.NewTicker(pollInterval)

		lastGrowth := time.Now() // Track from process start
		var lastByteSize int64 = 0
		stalled := false
		var waitErr error

		for {
			select {
			case <-timeoutCtx.Done():
				// Wall-clock timeout (terminal, not retried)
				pollTicker.Stop()
				killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
				stdoutFile.Close()  //nolint:errcheck
				stderrFile.Close()  //nolint:errcheck
				cancel()
				if killErr != nil {
					return 0, paths, fmt.Errorf("%w: role %q timed out after %v, kill failed: %w",
						ErrTimeout, cfg.Role, cfg.Timeout, killErr)
				}
				return 0, paths, fmt.Errorf("%w: role %q timed out after %v", ErrTimeout, cfg.Role, cfg.Timeout)

			case <-pollTicker.C:
				// Check transcript growth for stall detection
				currentSize, lastWrite := transcriptByteSize(stdoutFile, stderrFile)
				if currentSize > lastByteSize {
					// Growth detected; anchor the idle window to the actual last
					// write, not the poll time, so detection is not delayed by up
					// to one poll interval.
					lastByteSize = currentSize
					if lastWrite.After(lastGrowth) {
						lastGrowth = lastWrite
					}
				}
				// Declare stall when no growth for idleTimeout duration
				if time.Since(lastGrowth) >= idleTimeout {
					stalled = true
					idleDuration := time.Since(lastGrowth)
					// Log stall detection (P2-2)
					fmt.Fprintf(stallLogWriter, "agent: role %q stalled at attempt %d (no output for %v >= %v)\n",
						cfg.Role, attempt, idleDuration, idleTimeout)
					// Kill the process group
					killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					select {
					case <-done:
					case <-time.After(5 * time.Second):
					}
					// Close files before retry (P2-1 fix)
					stdoutFile.Close()  //nolint:errcheck
					stderrFile.Close()  //nolint:errcheck
					if killErr != nil {
						pollTicker.Stop()
						cancel()
						return 0, paths, fmt.Errorf("agent: stalled process group kill failed for role %q: %w", cfg.Role, killErr)
					}
					break
				}

			case waitErr = <-done:
				// Process finished (either success or non-zero exit)
				pollTicker.Stop()
				stdoutFile.Close()  //nolint:errcheck
				stderrFile.Close()  //nolint:errcheck
				cancel()

				if waitErr == nil {
					return 0, paths, nil
				}

				// exec.ExitError is expected for non-zero exit codes — return exit code
				var exitErr *exec.ExitError
				if errors.As(waitErr, &exitErr) {
					return exitErr.ExitCode(), paths, nil
				}

				// Other errors (e.g. I/O on the pipes) are unexpected
				return 0, paths, fmt.Errorf("agent: process wait failed: %w", waitErr)
			}

			if stalled {
				break
			}
		}

		pollTicker.Stop()
		cancel()

		// If not stalled, break out of retry loop (success or terminal error already returned above)
		if !stalled {
			break
		}

		// Stalled: retry if attempts remaining
		if attempt >= maxStallRetries {
			break
		}
		// Continue to next attempt
	}

	// All attempts stalled; return ErrStalled
	return 0, paths, fmt.Errorf("%w: role %q stalled after %d attempts with idle timeout %v", ErrStalled, cfg.Role, maxStallRetries+1, idleTimeout)
}
