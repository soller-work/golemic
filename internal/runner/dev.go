package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/cbmbroker"
	"golemic/internal/gmbroker"
	"golemic/internal/prompt"
	"golemic/internal/telemetry"
)

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

	cbmEnabled := false
	var brokerEnv []string
	if r.cfg.CodebaseMemory.Enabled {
		cbmCacheDir := filepath.Join(golemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		projectName := fmt.Sprintf("golemic-issue-%d-dev", r.issueNum)
		sockPath := filepath.Join(runsDir, r.runID, "cbm-dev-retry.sock")
		if b, env, ok := r.startCBMForRole(devWorktreePath, cbmCacheDir, sockPath, projectName); ok {
			defer b.Shutdown()
			brokerEnv = env
			cbmEnabled = true
		}
	}
	gmSockPath := filepath.Join(runsDir, r.runID, "gm-dev-retry.sock")
	if gmb, gmEnv, ok := r.startGMForRole(gmSockPath, "dev", devWorktreePath); ok {
		defer gmb.Shutdown()
		brokerEnv = append(brokerEnv, gmEnv...)
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

	r.writeDevStarted(eventLogPath)
	activityPath := filepath.Join(runsDir, r.runID, "dev.activity.jsonl")
	stopFollow := followActivity(r.progressRenderer, "dev", activityPath)

	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}

	cfg := r.buildDevAgentConfig(systemPromptFile, model, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath, timeout, runsDir, brokerEnv)
	exitCode, paths, err := runFn(context.Background(), cfg)
	stopFollow()

	if err != nil {
		r.emitAgentWrittenEvents(eventLogPath)
		return r.handleDevAgentErrorWithLog(eventLogPath, err, endSpan)
	}

	r.writeAgentCompleted(eventLogPath, "dev", exitCode)
	r.emitAgentWrittenEvents(eventLogPath)

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

	cbmEnabled := false
	var brokerEnv []string
	if r.cfg.CodebaseMemory.Enabled {
		cbmCacheDir := filepath.Join(golemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		projectName := fmt.Sprintf("golemic-issue-%d-dev", r.issueNum)
		sockPath := filepath.Join(runsDir, r.runID, "cbm-dev.sock")
		if b, env, ok := r.startCBMForRole(devWorktreePath, cbmCacheDir, sockPath, projectName); ok {
			defer b.Shutdown()
			brokerEnv = env
			cbmEnabled = true
		}
	}
	gmSockPath := filepath.Join(runsDir, r.runID, "gm-dev.sock")
	if gmb, gmEnv, ok := r.startGMForRole(gmSockPath, "dev", devWorktreePath); ok {
		defer gmb.Shutdown()
		brokerEnv = append(brokerEnv, gmEnv...)
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

	r.writeDevStarted(eventLogPath)
	activityPath := filepath.Join(runsDir, r.runID, "dev.activity.jsonl")
	stopFollow := followActivity(r.progressRenderer, "dev", activityPath)

	// Run dev agent
	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}
	exitCode, paths, err := runFn(context.Background(), r.buildDevAgentConfig(
		systemPromptFile, model, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath, timeout, runsDir, brokerEnv,
	))
	stopFollow()

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
		if errors.Is(err, agent.ErrThinkingLoop) {
			endSpan(telemetry.StatusKilled, nil)
			fmt.Fprintf(r.stderr, "dev_failed: dev agent thinking loop\n")
			return outcomeAborted
		}
		var chainErr *agent.ModelChainExhaustedError
		if errors.As(err, &chainErr) {
			r.writeAgentCompleted(eventLogPath, "dev", 1)
			r.emitAgentWrittenEvents(eventLogPath)
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
	r.emitAgentWrittenEvents(eventLogPath)

	// Fail on non-zero exit (BR-001, BR-002)
	if exitCode != 0 {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent exited with code %d; see %s\n", exitCode, paths.Stderr)
		return outcomeDevFailed
	}

	endSpan(telemetry.StatusOK, nil)
	return outcomeSuccess
}

// startCBMForRole indexes the worktree and starts the CBM broker.
func (r *Runner) startCBMForRole(wtPath, cbmCacheDir, sockPath, projectName string) (*cbmbroker.Broker, []string, bool) {
	if !r.indexWorktree(wtPath, cbmCacheDir, projectName) {
		return nil, nil, false
	}

	b, err := r.startCBMBroker(sockPath, cbmCacheDir)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to start CBM broker: %v\n", err)
		return nil, nil, false
	}

	return b, []string{"CBM_SOCK=" + sockPath, "CBM_PROJECT=" + projectName}, true
}

func (r *Runner) buildDevAgentConfig(systemPromptFile, model, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath string, timeout time.Duration, runsDir string, brokerEnv []string) agent.RoleConfig {
	toolAllowlist := []string{"read", "bash", "write", "edit"}
	for _, e := range brokerEnv {
		if len(e) > len("GOLEMIC_GM_SOCK=") && e[:len("GOLEMIC_GM_SOCK=")] == "GOLEMIC_GM_SOCK=" {
			toolAllowlist = append(toolAllowlist, gmDevToolNames...)
			break
		}
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
		DevToken:          r.creds.DevToken(),
		ReviewerToken:     r.creds.ReviewerToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             model,
		Timeout:           timeout,
		IdleTimeout:       time.Duration(r.cfg.AgentIdleTimeoutMinutes) * time.Minute,
		ToolAllowlist:     toolAllowlist,
		RunsDir:           runsDir,
		Env:               brokerEnv,
	}
}

// startCBMBroker is a variable so tests can replace it without spawning a real npx process.
var startCBMBrokerFn = func(sockPath string, env map[string]string) (*cbmbroker.Broker, error) {
	return cbmbroker.Start(sockPath, env)
}

// gmDevToolNames are added to the dev agent tool allowlist when the GM broker is running.
var gmDevToolNames = []string{"gm_slice_get", "gm_project_check", "gm_dev_done"}

// gmReviewerToolNames are added to the reviewer agent tool allowlist when the GM broker is running.
var gmReviewerToolNames = []string{"gm_slice_get", "gm_review_submit"}

// startGMBrokerFn is a variable so tests can replace it without a real gh call.
var startGMBrokerFn = func(sockPath string, issueNum int, devToken string) (*gmbroker.Broker, error) {
	return gmbroker.Start(sockPath, issueNum, devToken)
}

// startGMForRole starts the gm_ broker on sockPath. Returns the broker, the
// GOLEMIC_GM_SOCK env entry, and true on success; logs a warning and returns
// false on failure (non-fatal: runner proceeds without the gm_ tools).
func (r *Runner) startGMForRole(sockPath, role, worktreePath string) (*gmbroker.Broker, []string, bool) {
	if r.creds == nil {
		return nil, nil, false
	}
	b, err := startGMBrokerFn(sockPath, r.issueNum, r.creds.DevToken())
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to start GM broker: %v\n", err)
		return nil, nil, false
	}
	b.SetAllowedTools(gmReviewerToolNames)
	if role == "dev" {
		b.SetAllowedTools(gmDevToolNames)
		b.ConfigureProjectCheck(gmbroker.ProjectCheckConfig{
			WorktreePath:  worktreePath,
			VerifyCommand: r.cfg.VerifyCommand,
			Env: map[string]string{
				"PATH": loginShellPATH(),
			},
		})
	}
	return b, []string{"GOLEMIC_GM_SOCK=" + sockPath}, true
}

func loginShellPATH() string {
	out, err := exec.Command("sh", "-l", "-c", "echo $PATH").Output()
	if err != nil {
		return os.Getenv("PATH")
	}
	return strings.TrimSpace(string(out))
}

// startCBMBroker starts a CBM broker and returns it.
func (r *Runner) startCBMBroker(sockPath, cbmCacheDir string) (*cbmbroker.Broker, error) {
	return startCBMBrokerFn(sockPath, map[string]string{
		"CBM_CACHE_DIR": cbmCacheDir,
		"CBM_LOG_LEVEL": "warn",
	})
}

// indexWorktree runs codebase-memory-mcp to index wtPath with the given project name.
// Failure is logged and does not abort the run (BR-7).
func (r *Runner) indexWorktree(wtPath, cbmCacheDir, projectName string) bool {
	if err := os.MkdirAll(cbmCacheDir, 0755); err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to create CBM cache dir %s: %v\n", cbmCacheDir, err)
		return false
	}
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"CBM_CACHE_DIR": cbmCacheDir, "CBM_LOG_LEVEL": "warn"},
		wtPath,
		"npx", "-y", "codebase-memory-mcp@0.9.0", "cli", "index_repository",
		"--repo-path", wtPath, "--name", projectName, "--mode", "fast",
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: codebase-memory indexing failed (proceeding without code intelligence): %v\n", err)
		return false
	}
	return true
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
	if errors.Is(err, agent.ErrThinkingLoop) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent thinking loop\n")
		return outcomeAborted
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
