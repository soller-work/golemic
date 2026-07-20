package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golemic/internal/agent"
	"golemic/internal/prompt"
	"golemic/internal/telemetry"
)

// runDevRetryAgent runs the dev agent in the existing worktree to address reviewer findings.
// findings must be non-empty (enforced by RenderDevRetry). findingsJSON may be empty.
func (r *Runner) runDevRetryAgent(golemicDir, eventLogPath string, timeout time.Duration, findings, findingsJSON, parentSpanID string, round int) string {
	golemicBinaryPath, _ := os.Executable()
	binaryDir := filepath.Dir(golemicBinaryPath)
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	userPrompt, err := prompt.RenderDevRetry(
		findings,
		findingsJSON,
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
		},
		r.branchName,
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, ".golemic", "guidelines", "dev.md"),
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: %v\n", err)
		return outcomeReviewFailed
	}

	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanAgentTurn,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "role": "dev", "round": round, "model": r.cfg.Models.Dev})

	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}

	cfg := r.buildDevAgentConfig(binaryDir, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath, timeout, runsDir)
	exitCode, paths, err := runFn(context.Background(), cfg)

	if err != nil {
		return r.handleDevAgentError(err, endSpan)
	}

	r.writeAgentCompleted(eventLogPath, "dev", exitCode)

	if exitCode != 0 {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent exited with code %d; see %s\n", exitCode, paths.Stderr)
		return outcomeDevFailed
	}

	endSpan(telemetry.StatusOK, nil)
	return outcomeSuccess
}
func (r *Runner) runDevAgent(golemicDir, eventLogPath string, timeout time.Duration, parentSpanID string, round int) string {
	golemicBinaryPath, _ := os.Executable()
	binaryDir := filepath.Dir(golemicBinaryPath)
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	// Render dev prompt
	userPrompt, err := prompt.RenderDev(
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
		},
		r.branchName,
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, ".golemic", "guidelines", "dev.md"),
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render dev prompt: %v\n", err)
		return outcomeDevFailed
	}

	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanAgentTurn,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "role": "dev", "round": round, "model": r.cfg.Models.Dev})

	// Run dev agent
	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}
	exitCode, paths, err := runFn(context.Background(), agent.RoleConfig{
		Role:              "dev",
		SystemPromptFile:  filepath.Join(binaryDir, "prompts", "dev.md"),
		UserPrompt:        userPrompt,
		WorktreeDir:       devWorktreePath,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		TurnID:            r.turnCounter,
		GHToken:           r.creds.DevToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             r.cfg.Models.Dev,
		Timeout:           timeout,
		ToolAllowlist:     []string{"read", "bash", "write", "edit"},
		RunsDir:           runsDir,
	})

	if err != nil {
		if errors.Is(err, agent.ErrTimeout) {
			endSpan(telemetry.StatusKilled, nil)
			fmt.Fprintf(r.stderr, "dev_failed: dev agent exceeded timeout\n")
			return outcomeTimeout
		}
		if errors.Is(err, agent.ErrStalled) {
			endSpan(telemetry.StatusKilled, nil)
			fmt.Fprintf(r.stderr, "dev_failed: dev agent stalled\n")
			return outcomeStalled
		}
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: agent failed: %v\n", err)
		return outcomeDevFailed
	}

	// Record agent exit code in event log (BR-004)
	r.writeAgentCompleted(eventLogPath, "dev", exitCode)

	// Fail on non-zero exit (BR-001, BR-002)
	if exitCode != 0 {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent exited with code %d; see %s\n", exitCode, paths.Stderr)
		return outcomeDevFailed
	}

	endSpan(telemetry.StatusOK, nil)
	return outcomeSuccess
}

// buildDevAgentConfig creates the agent.RoleConfig for the dev retry agent.
func (r *Runner) buildDevAgentConfig(binaryDir, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath string, timeout time.Duration, runsDir string) agent.RoleConfig {
	return agent.RoleConfig{
		Role:              "dev",
		SystemPromptFile:  filepath.Join(binaryDir, "prompts", "dev.md"),
		UserPrompt:        userPrompt,
		WorktreeDir:       devWorktreePath,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		TurnID:            r.turnCounter,
		GHToken:           r.creds.DevToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             r.cfg.Models.Dev,
		Timeout:           timeout,
		ToolAllowlist:     []string{"read", "bash", "write", "edit"},
		RunsDir:           runsDir,
	}
}

// handleDevAgentError processes agent errors and returns the outcome.
func (r *Runner) handleDevAgentError(err error, endSpan func(string, map[string]any)) string {
	if errors.Is(err, agent.ErrTimeout) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent exceeded timeout\n")
		return outcomeTimeout
	}
	if errors.Is(err, agent.ErrStalled) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent stalled\n")
		return outcomeStalled
	}
	endSpan(telemetry.StatusError, nil)
	fmt.Fprintf(r.stderr, "dev_failed: agent failed: %v\n", err)
	return outcomeDevFailed
}
