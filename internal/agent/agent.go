// Package agent executes a role as a headless subprocess (pi -p) with correct
// environment, timeout, and transcript capture. It is the invocation layer for
// dev and reviewer roles in the golemic loop.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
// killed after each stall. Each stall triggers a retry with the same session-id,
// allowing pi to resume the session; terminal after maxStallRetries.
var ErrStalled = errors.New("agent execution stalled")

// ErrThinkingLoop is returned when the agent process continuously emits output
// (e.g. thinking_delta events) but makes no tool progress for the idle threshold.
// This is a deterministic failure; the process is killed and no retry is attempted.
var ErrThinkingLoop = errors.New("agent thinking loop detected")

// ErrModelChainExhausted is the sentinel wrapped inside ModelChainExhaustedError.
var ErrModelChainExhausted = errors.New("model chain exhausted")

// ModelChainExhaustedError is returned by RunRole when every model in a configured
// fallback chain has failed with a fallback-eligible technical error.
type ModelChainExhaustedError struct {
	Role     string
	Attempts []AttemptSummary
}

func (e *ModelChainExhaustedError) Error() string {
	models := make([]string, len(e.Attempts))
	for i, a := range e.Attempts {
		models[i] = a.Model + " (" + a.Reason + ")"
	}
	return fmt.Sprintf("%s: role %q exhausted model chain: %s",
		ErrModelChainExhausted, e.Role, strings.Join(models, "; "))
}

func (e *ModelChainExhaustedError) Is(target error) bool {
	return target == ErrModelChainExhausted
}

// ---------------------------------------------------------------------------
// Command factory (injectable for tests)
// ---------------------------------------------------------------------------

// CommandFactory creates *exec.Cmd instances. Defaults to exec.Command (the
// production value: var CommandFactory = exec.Command). Override in tests to
// inject a fake binary without a real pi installation.
var CommandFactory = exec.Command

// loginShellPATHResolver returns the PATH from the developer's login shell so
// the agent subprocess can resolve toolchain binaries (e.g. golangci-lint)
// without sourcing rc files. Override in tests.
var loginShellPATHResolver = defaultLoginShellPATH

func defaultLoginShellPATH() string {
	out, err := exec.Command("sh", "-l", "-c", "echo $PATH").Output()
	if err != nil {
		return os.Getenv("PATH")
	}
	return strings.TrimSpace(string(out))
}

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
	// defaultIdleTimeout is the default idle timeout (300s).
	defaultIdleTimeout = 300 * time.Second
	// defaultMaxStallRetries is the default max number of retries on stall (2).
	defaultMaxStallRetries = 2
)

// sanitizeSessionID replaces any character not in [A-Za-z0-9._-] with '-',
// making the string safe for use as a command-line flag value and file path.
func sanitizeSessionID(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, s)
}

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
	DevToken          string        // golemic dev token, set as GOLEMIC_DEV_TOKEN
	ReviewerToken     string        // golemic reviewer token, set as GOLEMIC_REVIEWER_TOKEN
	GolemicBinaryPath string        // path to the golemic binary; its directory is prepended to PATH
	Model             string        // model identifier passed to --model
	Timeout           time.Duration // maximum wall-clock time for the subprocess
	IdleTimeout       time.Duration // idle window for stall detection; 0 means use env/default
	ToolAllowlist     []string      // tool names passed to --tools (e.g. ["read","bash","write","edit"])
	RunsDir           string        // base directory for transcript files (<RunsDir>/<RunID>/<role>.*.log)
	TurnID            int           // monotonic turn identifier, exported as GOLEMIC_TURN_ID
	Env               []string      // additional "KEY=VALUE" pairs merged into the subprocess environment
	TerminalDone      chan struct{} // closed when gm_dev_done or accepted gm_review_submit reaches a terminal result
}

// TranscriptPaths holds the absolute paths of the captured output files.
type TranscriptPaths struct {
	Stdout string // path to <RunsDir>/<RunID>/<role>.activity.jsonl
	Stderr string // path to <RunsDir>/<RunID>/<role>.stderr.log
}

// toolProgressState holds the incremental read position for tool-progress scanning.
type toolProgressState struct {
	offset   int64
	inFlight int // tool_execution_start count minus tool_execution_end count
}

// applyToolEvent updates state based on a single activity event type.
// Returns 1 if a tool execution completed, 0 otherwise.
func applyToolEvent(evType string, state *toolProgressState) int {
	switch evType {
	case "tool_execution_start":
		state.inFlight++
	case "tool_execution_end":
		if state.inFlight > 0 {
			state.inFlight--
		}
		return 1
	}
	return 0
}

// readToolProgress reads new complete lines from the activity JSONL at path
// starting from state.offset. It returns the number of newly completed tool
// executions and whether any tool is currently in-flight. Partial last lines
// (no trailing newline) are left for the next tick. Malformed or unrecognised
// lines are skipped without affecting in-flight state.
func readToolProgress(path string, state *toolProgressState) (newlyCompleted int, inFlight bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, state.inFlight > 0
	}
	defer f.Close() //nolint:errcheck

	if _, err := f.Seek(state.offset, io.SeekStart); err != nil {
		return 0, state.inFlight > 0
	}

	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return 0, state.inFlight > 0
	}

	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return 0, state.inFlight > 0
	}
	state.offset += int64(lastNL + 1)

	scanner := bufio.NewScanner(bytes.NewReader(data[:lastNL+1]))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		newlyCompleted += applyToolEvent(ev.Type, state)
	}

	return newlyCompleted, state.inFlight > 0
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
//   - <RunsDir>/<RunID>/<role>.activity.jsonl
//   - <RunsDir>/<RunID>/<role>.stderr.log
//
// If the process exceeds cfg.Timeout, the entire process group is killed
// (SIGKILL to -pgid) and the returned error wraps ErrTimeout. Partial output
// up to the kill point is preserved in the transcript files.
//
// Stall detection: if no tool execution completes (tool_execution_end in the
// activity.jsonl) for the idle threshold (default 300s, configurable via
// GOLEMIC_AGENT_IDLE_TIMEOUT_SEC), the process group is killed and retried (up
// to maxStallRetries times, default 2, configurable via
// GOLEMIC_AGENT_MAX_STALL_RETRIES). A tool currently in-flight
// (tool_execution_start without matching tool_execution_end) suppresses the
// stall unconditionally. Toolcall composition events (toolcall_*) do not count
// as progress. Fresh transcript files are truncated per attempt. If all
// attempts stall, returns ErrStalled. If a process exceeds wall-clock Timeout,
// that is terminal (not retried) and returns ErrTimeout.
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

	// ---- Parse model chain ----
	chain, chainErr := ParseModelChain(cfg.Model)
	if chainErr != nil {
		return 0, TranscriptPaths{}, chainErr
	}

	// ---- Prepare static values ----
	sessionID := sanitizeSessionID(cfg.RunID + "-" + cfg.Role)
	stdoutPath := filepath.Join(cfg.RunsDir, cfg.RunID, cfg.Role+".activity.jsonl")
	stderrPath := filepath.Join(cfg.RunsDir, cfg.RunID, cfg.Role+".stderr.log")
	golemicDir := filepath.Dir(cfg.GolemicBinaryPath)

	if err := os.MkdirAll(filepath.Join(cfg.RunsDir, cfg.RunID), 0755); err != nil {
		return 0, TranscriptPaths{}, fmt.Errorf("agent: failed to create transcript directory: %w", err)
	}

	paths = TranscriptPaths{Stdout: stdoutPath, Stderr: stderrPath}

	// ---- Prepare golemic-owned pi agent dir ----
	localPiDir, err := resolveLocalPiAgentDir()
	if err != nil {
		return 0, TranscriptPaths{}, err
	}
	gmExtDir := filepath.Join(cfg.WorktreeDir, ".pi", "extensions", "golemic")
	golemicPiDir, err := preparePiAgentDir(localPiDir, gmExtDir)
	if err != nil {
		return 0, TranscriptPaths{}, err
	}

	// ---- Model chain loop ----
	var chainAttempts []AttemptSummary

	for _, model := range chain {
		var attemptErr error
		exitCode, attemptErr = runModelAttempt(ctx, cfg, model, sessionID, golemicDir, golemicPiDir, stdoutPath, stderrPath)

		// Wall-clock timeout is always terminal (BR-9).
		if errors.Is(attemptErr, ErrTimeout) {
			return 0, paths, attemptErr
		}

		tr := InspectTranscript(stdoutPath)

		// Success: exit 0, no semantic failure, and no fallback-eligible signal
		// (auto_retry_end success=false means Pi exhausted its own retries).
		if attemptErr == nil && exitCode == 0 && !tr.SemanticFailed && !tr.FallbackEligible {
			return exitCode, paths, nil
		}

		// Fallback-eligible failure: record and try next model.
		if tr.FallbackEligible {
			reason := tr.Reason
			if reason == "" {
				reason = "provider error"
			}
			chainAttempts = append(chainAttempts, AttemptSummary{Model: model, Reason: reason})
			continue
		}

		// Terminal failure: propagate as-is (BR-6, BR-9).
		if attemptErr != nil {
			return 0, paths, attemptErr
		}
		// Semantic failure (stopReason:error|aborted) at exit 0 — not fallback-eligible,
		// but still a real failure; return non-zero so the runner doesn't treat it as success (BR-4).
		if tr.SemanticFailed {
			return 1, paths, nil
		}
		return exitCode, paths, nil
	}

	// All models in chain exhausted with fallback-eligible failures (BR-7).
	return 1, paths, &ModelChainExhaustedError{Role: cfg.Role, Attempts: chainAttempts}
}

// buildPiArgs constructs the argv for a pi --mode json subprocess for the given model.
func buildPiArgs(cfg RoleConfig, model, sessionID string) []string {
	args := []string{
		"-p",
		"--mode", "json",
		"--session-id", sessionID,
		"--append-system-prompt", "@" + cfg.SystemPromptFile,
		"--tools", strings.Join(cfg.ToolAllowlist, ","),
		"--model", model,
	}
	return append(args, cfg.UserPrompt)
}

// runModelAttempt runs a single Pi subprocess for the given model with stall-detection retries.
// It is the inner execution engine; model chain logic lives in RunRole.
// openTranscriptFiles truncates both transcript files and returns the open file handles.
func openTranscriptFiles(stdoutPath, stderrPath string) (stdout, stderr *os.File, err error) {
	stdout, err = os.OpenFile(stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return nil, nil, fmt.Errorf("agent: failed to create stdout transcript %s: %w", stdoutPath, err)
	}
	stderr, err = os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		stdout.Close() //nolint:errcheck
		return nil, nil, fmt.Errorf("agent: failed to create stderr transcript %s: %w", stderrPath, err)
	}
	return stdout, stderr, nil
}

// newPiCmd builds and configures the pi subprocess without starting it.
// shimDir, when non-empty, is prepended to PATH so its gh shim takes precedence
// over any system gh binary, blocking direct gh usage from agent bash.
func newPiCmd(cfg RoleConfig, args []string, golemicDir, golemicPiDir, shimDir string, stdoutFile, stderrFile *os.File, terminalDone chan struct{}) *exec.Cmd {
	cmd := CommandFactory("pi", args...)
	cmd.Dir = cfg.WorktreeDir
	agentPath := golemicDir + string(filepath.ListSeparator) + loginShellPATHResolver()
	if shimDir != "" {
		agentPath = shimDir + string(filepath.ListSeparator) + agentPath
	}
	env := append(
		os.Environ(),
		"GOLEMIC_RUN_ID="+cfg.RunID,
		"GOLEMIC_EVENT_LOG="+cfg.EventLogPath,
		"GOLEMIC_TURN_ID="+strconv.Itoa(cfg.TurnID),
		"GH_TOKEN="+cfg.GHToken,
		"GOLEMIC_DEV_TOKEN="+cfg.DevToken,
		"GOLEMIC_REVIEWER_TOKEN="+cfg.ReviewerToken,
		"PATH="+agentPath,
		"PI_CODING_AGENT_DIR="+golemicPiDir,
	)
	env = append(env, cfg.Env...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = &lineFilterWriter{dst: stdoutFile, onLine: func(line []byte) {
		if terminalDone != nil && terminalDoneFromLine(line) {
			select {
			case terminalDone <- struct{}{}:
			default:
			}
		}
	}}
	cmd.Stderr = stderrFile
	return cmd
}

func runModelAttempt(ctx context.Context, cfg RoleConfig, model, sessionID, golemicDir, golemicPiDir, stdoutPath, stderrPath string) (exitCode int, err error) {
	args := buildPiArgs(cfg, model, sessionID)
	idleTimeout := cfg.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = parseIdleTimeout()
	}
	maxStallRetries := parseMaxStallRetries()

	shimDir, cleanupShim := ghShimCreator()
	defer cleanupShim()

	for attempt := 0; attempt <= maxStallRetries; attempt++ {
		stdoutFile, stderrFile, fileErr := openTranscriptFiles(stdoutPath, stderrPath)
		if fileErr != nil {
			return 0, fileErr
		}
		terminalDone := make(chan struct{}, 1)
		cfg.TerminalDone = terminalDone
		cmd := newPiCmd(cfg, args, golemicDir, golemicPiDir, shimDir, stdoutFile, stderrFile, terminalDone)
		if startErr := cmd.Start(); startErr != nil {
			stdoutFile.Close() //nolint:errcheck
			stderrFile.Close() //nolint:errcheck
			return 0, fmt.Errorf("agent: failed to start pi process: %w", startErr)
		}
		exitCode, stallReason, waitErr := waitForProcess(ctx, cfg, cmd, stdoutFile, stderrFile, terminalDone, attempt, idleTimeout)
		if stallReason == "" {
			return exitCode, waitErr
		}
		if waitErr != nil {
			return 0, waitErr
		}
		// thinking_loop is deterministic — no retry.
		if stallReason == "thinking_loop" {
			return 0, fmt.Errorf("%w: role %q detected thinking loop (idle timeout %v)", ErrThinkingLoop, cfg.Role, idleTimeout)
		}
		if attempt >= maxStallRetries {
			break
		}
	}
	return 0, fmt.Errorf("%w: role %q stalled after %d attempts with idle timeout %v", ErrStalled, cfg.Role, maxStallRetries+1, idleTimeout)
}

// killProcessGroup sends SIGKILL to the process group, drains done, and closes transcript files.
// Returns the kill error (nil on success).
func killProcessGroup(cmd *exec.Cmd, stdoutFile, stderrFile *os.File, done <-chan error) error {
	killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	stdoutFile.Close() //nolint:errcheck
	stderrFile.Close() //nolint:errcheck
	return killErr
}

// classifyStall returns "thinking_loop" if the stream grew since lastStreamOffset
// (deterministic failure, no retry) or "hang" if frozen (transient, retry eligible).
// It also logs the diagnostic line.
func classifyStall(role string, attempt int, idleDuration, idleTimeout time.Duration, currentOffset, lastStreamOffset int64) string {
	reason := "hang"
	if currentOffset > lastStreamOffset {
		reason = "thinking_loop"
	}
	fmt.Fprintf(stallLogWriter, "agent: role %q stalled at attempt %d (no tool completion for %v >= %v, reason: %s)\n",
		role, attempt, idleDuration, idleTimeout, reason)
	return reason
}

// resolveWaitError converts a cmd.Wait error into (exitCode, err).
func resolveWaitError(waitErr error) (int, error) {
	if waitErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, fmt.Errorf("agent: process wait failed: %w", waitErr)
}

func terminalWaitResult(cfg RoleConfig, cmd *exec.Cmd, stdoutFile, stderrFile *os.File, done <-chan error, cancel context.CancelFunc) (int, string, error) {
	killErr := killProcessGroup(cmd, stdoutFile, stderrFile, done)
	cancel()
	if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		return 0, "", fmt.Errorf("agent: terminal process group kill failed for role %q: %w", cfg.Role, killErr)
	}
	return 0, "", nil
}

func timeoutWaitResult(cfg RoleConfig, cmd *exec.Cmd, stdoutFile, stderrFile *os.File, done <-chan error, cancel context.CancelFunc) (int, string, error) {
	killErr := killProcessGroup(cmd, stdoutFile, stderrFile, done)
	cancel()
	if killErr != nil {
		return 0, "", fmt.Errorf("%w: role %q timed out after %v, kill failed: %w",
			ErrTimeout, cfg.Role, cfg.Timeout, killErr)
	}
	return 0, "", fmt.Errorf("%w: role %q timed out after %v", ErrTimeout, cfg.Role, cfg.Timeout)
}

func handlePollTick(cfg RoleConfig, cmd *exec.Cmd, stdoutFile, stderrFile *os.File, done <-chan error, pollTicker *time.Ticker, cancel context.CancelFunc, toolState *toolProgressState, lastProgress *time.Time, lastStreamOffset *int64, attempt int, idleTimeout time.Duration) (int, string, error) {
	newlyCompleted, inFlight := readToolProgress(stdoutFile.Name(), toolState)
	if newlyCompleted > 0 {
		*lastProgress = time.Now()
		*lastStreamOffset = toolState.offset
	}
	if !inFlight && time.Since(*lastProgress) >= idleTimeout {
		reason := classifyStall(cfg.Role, attempt, time.Since(*lastProgress), idleTimeout, toolState.offset, *lastStreamOffset)
		pollErr := killProcessGroup(cmd, stdoutFile, stderrFile, done)
		pollTicker.Stop()
		cancel()
		if pollErr != nil {
			return 0, "", fmt.Errorf("agent: stalled process group kill failed for role %q: %w", cfg.Role, pollErr)
		}
		return 0, reason, nil
	}
	return 0, "", nil
}

func doneWaitResult(waitErr error, stdoutFile, stderrFile *os.File, cancel context.CancelFunc) (int, string, error) {
	stdoutFile.Close() //nolint:errcheck
	stderrFile.Close() //nolint:errcheck
	cancel()
	code, resolveErr := resolveWaitError(waitErr)
	return code, "", resolveErr
}

// waitForProcess runs the timeout/stall/wait loop after cmd has been started.
// It closes stdoutFile and stderrFile in all code paths.
// Returns (exitCode, stallReason, err): stallReason is "" on non-stall exit,
// "hang" when the stream was frozen (retry eligible), "thinking_loop" when the
// stream kept growing without tool progress (terminal, no retry).
func waitForProcess(ctx context.Context, cfg RoleConfig, cmd *exec.Cmd, stdoutFile, stderrFile *os.File, terminalDone <-chan struct{}, attempt int, idleTimeout time.Duration) (exitCode int, reason string, err error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	pollTicker := time.NewTicker(pollInterval)
	lastProgress := time.Now()
	var toolState toolProgressState
	// lastStreamOffset tracks stream offset when last tool completed, to detect
	// whether the activity file grew since then (thinking_loop) or froze (hang).
	lastStreamOffset := toolState.offset

	for {
		select {
		case <-terminalDone:
			pollTicker.Stop()
			return terminalWaitResult(cfg, cmd, stdoutFile, stderrFile, done, cancel)

		case <-timeoutCtx.Done():
			pollTicker.Stop()
			return timeoutWaitResult(cfg, cmd, stdoutFile, stderrFile, done, cancel)

		case <-pollTicker.C:
			exitCode, reason, err := handlePollTick(cfg, cmd, stdoutFile, stderrFile, done, pollTicker, cancel, &toolState, &lastProgress, &lastStreamOffset, attempt, idleTimeout)
			if err != nil || reason != "" {
				return exitCode, reason, err
			}

		case err := <-done:
			pollTicker.Stop()
			return doneWaitResult(err, stdoutFile, stderrFile, cancel)
		}
	}
}
