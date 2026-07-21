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
	"golemic/internal/worktree"
)

// cbmDevTools are the read-only graph tools granted to the dev agent when codebase-memory is enabled.
var cbmDevTools = []string{
	"search_graph", "trace_call_path", "query_graph",
	"get_architecture", "get_graph_schema", "get_code_snippet", "search_code",
}

// runDevRetryAgent runs the dev agent in the existing worktree to address reviewer findings.
// findings must be non-empty (enforced by RenderDevRetry). findingsJSON may be empty.
func (r *Runner) runDevRetryAgent(golemicDir, eventLogPath string, timeout time.Duration, findings, findingsJSON, parentSpanID string, round int) string {
	golemicBinaryPath, _ := os.Executable()
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	systemPromptFile, model, cleanupPrompt, err := r.resolveAgentFile("dev")
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err)
		return outcomeDevFailed
	}
	defer cleanupPrompt()

	cbmEnabled := r.cfg.CodebaseMemory.Enabled
	if cbmEnabled {
		cbmCacheDir := filepath.Join(golemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		r.indexWorktree(devWorktreePath, cbmCacheDir)
	}

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
		cbmEnabled,
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: %v\n", err)
		return outcomeReviewFailed
	}

	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanAgentTurn,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "role": "dev", "round": round, "model": model})

	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}

	cfg := r.buildDevAgentConfig(systemPromptFile, model, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath, timeout, runsDir, r.cfg.CodebaseMemory.Enabled)
	exitCode, paths, err := runFn(context.Background(), cfg)

	if err != nil {
		return r.handleDevAgentErrorWithLog(eventLogPath, err, endSpan)
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
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	systemPromptFile, model, cleanupPrompt, err := r.resolveAgentFile("dev")
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err)
		return outcomeDevFailed
	}
	defer cleanupPrompt()

	cbmEnabled := r.cfg.CodebaseMemory.Enabled
	if cbmEnabled {
		cbmCacheDir := filepath.Join(golemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		r.indexWorktree(devWorktreePath, cbmCacheDir)
		if err := worktree.WriteMCPFiles(devWorktreePath, cbmCacheDir); err != nil {
			fmt.Fprintf(r.stderr, "Warning: failed to write CBM MCP files for dev worktree: %v\n", err)
			cbmEnabled = false
		}
	}

	// Render dev prompt
	userPrompt, err := prompt.RenderDev(
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
		},
		r.branchName,
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, ".golemic", "guidelines", "dev.md"),
		cbmEnabled,
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render dev prompt: %v\n", err)
		return outcomeDevFailed
	}

	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanAgentTurn,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "role": "dev", "round": round, "model": model})

	// Run dev agent
	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}
	devTools := []string{"read", "bash", "write", "edit"}
	if cbmEnabled {
		devTools = append(devTools, cbmDevTools...)
	}
	exitCode, paths, err := runFn(context.Background(), agent.RoleConfig{
		Role:              "dev",
		SystemPromptFile:  systemPromptFile,
		UserPrompt:        userPrompt,
		WorktreeDir:       devWorktreePath,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		TurnID:            r.turnCounter,
		GHToken:           r.creds.DevToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             model,
		Timeout:           timeout,
		ToolAllowlist:     devTools,
		RunsDir:           runsDir,
		Approve:           cbmEnabled,
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
		var chainErr *agent.ModelChainExhaustedError
		if errors.As(err, &chainErr) {
			r.writeAgentCompleted(eventLogPath, "dev", 1)
			endSpan(telemetry.StatusError, nil)
			fmt.Fprintf(r.stderr, "dev_failed: %v\n", err)
			if prNum, prErr := r.getPRNumber(eventLogPath); prErr == nil {
				r.postModelChainExhaustedComment(prNum, chainErr)
			}
			return outcomeDevFailed
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
func (r *Runner) buildDevAgentConfig(systemPromptFile, model, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath string, timeout time.Duration, runsDir string, cbmEnabled bool) agent.RoleConfig {
	tools := []string{"read", "bash", "write", "edit"}
	if cbmEnabled {
		tools = append(tools, cbmDevTools...)
	}
	return agent.RoleConfig{
		Role:              "dev",
		SystemPromptFile:  systemPromptFile,
		UserPrompt:        userPrompt,
		WorktreeDir:       devWorktreePath,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		TurnID:            r.turnCounter,
		GHToken:           r.creds.DevToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             model,
		Timeout:           timeout,
		ToolAllowlist:     tools,
		RunsDir:           runsDir,
		Approve:           cbmEnabled,
	}
}

// indexWorktree runs codebase-memory-mcp to index wtPath into cbmCacheDir.
// Failure is logged and does not abort the run (BR-7).
func (r *Runner) indexWorktree(wtPath, cbmCacheDir string) {
	if err := os.MkdirAll(cbmCacheDir, 0755); err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to create CBM cache dir %s: %v\n", cbmCacheDir, err)
		return
	}
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"CBM_CACHE_DIR": cbmCacheDir, "CBM_LOG_LEVEL": "warn"},
		wtPath,
		"npx", "-y", "codebase-memory-mcp@0.9.0", "cli", "index_repository",
		"--repo-path", wtPath, "--mode", "fast",
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: codebase-memory indexing failed (proceeding without code intelligence): %v\n", err)
	}
}

// handleDevAgentErrorWithLog processes agent errors, writes agent_completed for chain
// exhaustion, and returns the outcome. eventLogPath may be empty for cases where no
// pr comment is needed.
func (r *Runner) handleDevAgentErrorWithLog(eventLogPath string, err error, endSpan func(string, map[string]any)) string {
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
	var chainErr *agent.ModelChainExhaustedError
	if errors.As(err, &chainErr) {
		if eventLogPath != "" {
			r.writeAgentCompleted(eventLogPath, "dev", 1)
			if prNum, prErr := r.getPRNumber(eventLogPath); prErr == nil {
				r.postModelChainExhaustedComment(prNum, chainErr)
			}
		}
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err)
		return outcomeDevFailed
	}
	endSpan(telemetry.StatusError, nil)
	fmt.Fprintf(r.stderr, "dev_failed: agent failed: %v\n", err)
	return outcomeDevFailed
}
