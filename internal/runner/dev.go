package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/cbmbroker"
	"golemic/internal/eventlog"
	"golemic/internal/gmbroker"
	"golemic/internal/loop"
	"golemic/internal/telemetry"
)

// runDevAgentWithPrompt executes one dev agent invocation with the given pre-rendered
// prompt. It handles broker setup, agent execution, gate validation, and side effects.
//
// Returns (event, gateRejectionReason):
//   - (loop.EventDevDone, ""): gate passed, commit/push/open-PR (or push) done
//   - (loop.EventDevGateRejected, reason): §10 gate rejected by broker
//   - (other event, ""): non-gate failure
func (r *Runner) runDevAgentWithPrompt(golemicDir, eventLogPath, systemPromptFile, model, userPrompt string, cbmEnabled bool, timeout time.Duration, parentSpanID string, round, attempt int) (loop.EventKey, string) {
	golemicBinaryPath, _ := os.Executable()
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	// CBM broker: only start for attempt 0 to avoid expensive re-indexing on gate retries.
	var brokerEnv []string
	var cbmCfg gmbroker.CBMConfig
	if cbmEnabled && attempt == 0 {
		cbmCacheDir := filepath.Join(golemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		projectName := fmt.Sprintf("golemic-issue-%d-dev", r.issueNum)
		cbmSockPath := filepath.Join(runsDir, r.runID, fmt.Sprintf("cbm-dev-r%d-a%d.sock", round, attempt))
		if b, env, ok := r.startCBMForRole(devWorktreePath, cbmCacheDir, cbmSockPath, projectName); ok {
			defer b.Shutdown()
			brokerEnv = env
			cbmCfg = gmbroker.CBMConfig{SockPath: cbmSockPath, Project: projectName}
		}
	}

	// GM broker: unique socket per attempt.
	var gmb *gmbroker.Broker
	gmSockPath := filepath.Join(runsDir, r.runID, fmt.Sprintf("gm-dev-r%d-a%d.sock", round, attempt))
	devInvocationID := fmt.Sprintf("%s/dev/round-%d/attempt-%d", r.runID, round, attempt)
	if b, gmEnv, ok := r.startGMForRole(gmSockPath, "dev", devWorktreePath); ok {
		gmb = b
		gmb.SetInvocationIdentity(r.runID, devInvocationID)
		defer gmb.Shutdown()
		brokerEnv = append(brokerEnv, gmEnv...)
		brokerEnv = append(brokerEnv, "GOLEMIC_RUN_ID="+r.runID, "GOLEMIC_INVOCATION_ID="+devInvocationID, "GOLEMIC_ROLE=dev")
		if cbmCfg.SockPath != "" {
			gmb.ConfigureCBM(cbmCfg)
			gmb.SetAllowedTools(append(gmDevToolNames, gmCodeToolNames...))
		}
	} else {
		fmt.Fprintf(r.stderr, "dev_failed: GM broker unavailable for dev invocation\n")
		return loop.EventDevFailed, ""
	}

	cfg := r.buildDevAgentConfig(systemPromptFile, model, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath, timeout, runsDir, round, attempt, brokerEnv)
	return r.executeDevAgentCfg(gmb, devWorktreePath, eventLogPath, cfg, parentSpanID, round, attempt)
}

func (r *Runner) executeDevAgentCfg(gmb *gmbroker.Broker, devWorktreePath, eventLogPath string, cfg agent.RoleConfig, parentSpanID string, round, attempt int) (loop.EventKey, string) {
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")
	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanAgentTurn,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "role": "dev", "round": round, "attempt": attempt, "model": cfg.Model})

	activityPath := filepath.Join(runsDir, r.runID, fmt.Sprintf("dev-r%d-a%d.activity.jsonl", round, attempt))
	stopFollow := followActivity(r.lifecycleProgressRenderer(), "dev", activityPath)

	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}

	r.emitAgentContext(cfg)
	exitCode, paths, err := runFn(r.agentCtx(), cfg)
	stopFollow()

	usage := parseActivityUsage(activityPath)
	r.recordTokenUsage("dev", round, attempt, usage)
	endSpan = wrapEndSpanWithUsage(endSpan, usage)

	if err != nil {
		r.emitAgentWrittenEvents(eventLogPath)
		return r.handleDevAgentErrorWithLog(eventLogPath, err, endSpan, paths.Stdout, paths.Stderr), ""
	}

	r.writeAgentCompleted(eventLogPath, "dev", exitCode, paths.Stdout, paths.Stderr)
	r.emitAgentWrittenEvents(eventLogPath)

	if exitCode != 0 {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent exited with code %d; see %s\n", exitCode, paths.Stderr)
		return loop.EventDevFailed, ""
	}

	return r.finishDevAgentOutcome(gmb, eventLogPath, devWorktreePath, endSpan)
}

func (r *Runner) finishDevAgentOutcome(gmb *gmbroker.Broker, eventLogPath, devWorktreePath string, endSpan func(string, map[string]any)) (loop.EventKey, string) {
	if gmb != nil {
		return r.finishDevAgentWithBroker(gmb, eventLogPath, devWorktreePath, endSpan)
	}

	endSpan(telemetry.StatusOK, nil)
	return loop.EventDevDone, ""
}

func (r *Runner) finishDevAgentWithBroker(gmb *gmbroker.Broker, eventLogPath, devWorktreePath string, endSpan func(string, map[string]any)) (loop.EventKey, string) {
	// Accepted path: dev called gm_dev_done and the gate passed.
	if devDone, ok := gmb.DevDoneResult(); ok {
		return r.finalizeAcceptedDevDone(gmb, devWorktreePath, eventLogPath, endSpan, devDone)
	}

	// Deterministic finalize: the §10 gate rejected the call but the tree is now
	// green (last check OK + current fingerprint matches) and valid params were
	// already supplied. Finalize without launching another LLM turn.
	recoveredParams, hasParams := gmb.RecoveredDevDoneParams()
	if hasParams && gmb.IsTreeGreen() {
		if sideEffectErr := r.commitDevDone(devWorktreePath, eventLogPath, *recoveredParams); sideEffectErr != nil {
			endSpan(telemetry.StatusError, nil)
			fmt.Fprintf(r.stderr, "dev_failed: %v\n", sideEffectErr)
			return loop.EventDevFailed, ""
		}
		endSpan(telemetry.StatusOK, nil)
		return loop.EventDevDone, ""
	}

	if ev, reason, handled := r.finishDevAgentWithoutAcceptedDevDone(gmb, endSpan); handled {
		if output := gmb.LastCheckOutput(); output != "" {
			reason = reason + "\n\nFailing gm_project_check output:\n" + output
		}
		r.emitGateRejectionError(gmb, reason)
		return ev, reason
	}
	endSpan(telemetry.StatusError, nil)
	reason := "the invocation ended without a successful gm_dev_done call; run gm_project_check until green, then call gm_dev_done with summary, commitMsg, prTitle, and prBody"
	r.emitGateRejectionError(gmb, reason)
	return loop.EventDevGateRejected, reason
}

func (r *Runner) finalizeAcceptedDevDone(gmb *gmbroker.Broker, devWorktreePath, eventLogPath string, endSpan func(string, map[string]any), devDone *gmbroker.DevDoneParams) (loop.EventKey, string) {
	acceptedFP, fpOK := gmb.DevDoneFingerprint()
	if !fpOK {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: gm_dev_done acceptance fingerprint missing\n")
		return loop.EventDevFailed, ""
	}
	currentFP, currentOK := gmb.CurrentFingerprint()
	if currentOK && currentFP != acceptedFP {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: worktree changed after gm_dev_done acceptance\n")
		return loop.EventDevFailed, ""
	}
	if sideEffectErr := r.commitDevDone(devWorktreePath, eventLogPath, *devDone); sideEffectErr != nil {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", sideEffectErr)
		return loop.EventDevFailed, ""
	}
	endSpan(telemetry.StatusOK, nil)
	return loop.EventDevDone, ""
}

func (r *Runner) emitGateRejectionError(gmb *gmbroker.Broker, reason string) {
	treeState := "tree red"
	if gmb.IsTreeGreen() {
		treeState = "tree green"
	}
	fmt.Fprintf(r.stderr, "%v\n", &loop.StateError{Step: loop.StepRunDev, Event: loop.EventDevGateRejected, Msg: "predicate gm_dev_done unmet (" + treeState + "): " + reason}) //nolint:errcheck
}

func (r *Runner) finishDevAgentWithoutAcceptedDevDone(gmb *gmbroker.Broker, endSpan func(string, map[string]any)) (loop.EventKey, string, bool) {
	if gmb.DevDoneGateRejected() {
		endSpan(telemetry.StatusError, nil)
		return loop.EventDevGateRejected, gmb.DevDoneGateReason(), true
	}
	if status, ok := gmb.DevDoneTerminalStatus(); ok && (status == "SCHEMA_INVALID" || status == "PROTOCOL_ERROR") {
		if msg, msgOK := gmb.DevDoneTerminalMessage(); msgOK && msg != "" {
			endSpan(telemetry.StatusError, nil)
			return loop.EventDevGateRejected, msg, true
		}
	}
	return "", "", false
}

func (r *Runner) commitDevDone(devWT, eventLogPath string, devDone gmbroker.DevDoneParams) error {
	if r.hasPROpenedEvent(eventLogPath) {
		return r.commitAndForcePush(devWT, devDone)
	}
	return r.commitPushAndOpenPR(devWT, eventLogPath, devDone)
}

// commitPushAndOpenPR stages all changes, commits, pushes the branch, and opens
// a GitHub PR. It then writes a pr_opened event to the event log.
func (r *Runner) commitPushAndOpenPR(devWT, eventLogPath string, devDone gmbroker.DevDoneParams) error {
	if _, err := r.executor.RunInDir(devWT, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}
	if _, err := r.executor.RunInDir(devWT, "git", "commit", "-m", devDone.CommitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if _, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		devWT,
		"git", "push", "--set-upstream", "origin", r.branchName,
	); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		devWT,
		"gh", "pr", "create", "--title", devDone.PrTitle, "--body", ensureBodyClosesIssueNum(devDone.PrBody, r.issueNum),
	)
	if err != nil {
		return fmt.Errorf("gh pr create: %w", err)
	}
	prURL := strings.TrimSpace(out)
	prNumber := parsePRNumber(prURL)
	if prNumber == "" {
		return fmt.Errorf("failed to parse PR number from: %s", prURL)
	}
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer w.Close() //nolint:errcheck
	payload, _ := json.Marshal(map[string]string{
		"prNumber": prNumber,
		"url":      prURL,
		"branch":   r.branchName,
	})
	return w.Write(eventlog.Event{
		Type:    eventlog.EventPROpened,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  r.turnCounter,
		Payload: payload,
	})
}

// commitAndForcePush stages all changes, commits, and force-pushes the branch.
// Used for dev-retry turns where the PR is already open.
func (r *Runner) commitAndForcePush(devWT string, devDone gmbroker.DevDoneParams) error {
	if _, err := r.executor.RunInDir(devWT, "git", "add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %w", err)
	}
	// git diff --cached --quiet exits 0 when there is nothing staged, 1 when there are changes.
	// A nil error means nothing is staged — the dev-retry produced no working-tree changes.
	if _, err := r.executor.RunInDir(devWT, "git", "diff", "--cached", "--quiet"); err == nil {
		fmt.Fprintf(r.stderr, "dev_retry: no working-tree changes; re-reviewing the same SHA\n")
		return nil
	}
	if _, err := r.executor.RunInDir(devWT, "git", "commit", "-m", devDone.CommitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	if _, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		devWT,
		"git", "push", "--force-with-lease",
	); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

// ensureBodyClosesIssueNum appends "Closes #N" to body if no GitHub closing
// keyword for that issue number is already present.
func ensureBodyClosesIssueNum(body string, issueNum int) string {
	pattern := fmt.Sprintf("#%d", issueNum)
	if strings.Contains(strings.ToLower(body), strings.ToLower(fmt.Sprintf("closes %s", pattern))) ||
		strings.Contains(strings.ToLower(body), strings.ToLower(fmt.Sprintf("fixes %s", pattern))) ||
		strings.Contains(strings.ToLower(body), strings.ToLower(fmt.Sprintf("resolves %s", pattern))) {
		return body
	}
	return strings.TrimRight(body, "\n") + fmt.Sprintf("\n\nCloses %s\n", pattern)
}

// parsePRNumber extracts the PR number from a GitHub PR URL.
func parsePRNumber(prURL string) string {
	if idx := strings.LastIndex(prURL, "/"); idx >= 0 {
		candidate := prURL[idx+1:]
		if _, err := strconv.Atoi(candidate); err == nil {
			return candidate
		}
	}
	return ""
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

func (r *Runner) buildDevAgentConfig(systemPromptFile, model, devWorktreePath, eventLogPath, userPrompt, golemicBinaryPath string, timeout time.Duration, runsDir string, round, attempt int, brokerEnv []string) agent.RoleConfig {
	_ = golemicBinaryPath
	toolAllowlist := []string{"read", "bash", "write", "edit"}
	hasGMSock := false
	hasCBMSock := false
	for _, e := range brokerEnv {
		if strings.HasPrefix(e, "GOLEMIC_GM_SOCK=") {
			hasGMSock = true
		}
		if strings.HasPrefix(e, "CBM_SOCK=") {
			hasCBMSock = true
		}
	}
	if hasGMSock {
		toolAllowlist = append(toolAllowlist, gmDevToolNames...)
		if hasCBMSock {
			toolAllowlist = append(toolAllowlist, gmCodeToolNames...)
		}
	}
	return agent.RoleConfig{
		Role:             "dev",
		SystemPromptFile: systemPromptFile,
		UserPrompt:       userPrompt,
		WorktreeDir:      devWorktreePath,
		RunID:            r.runID,
		EventLogPath:     eventLogPath,
		TurnID:           r.turnCounter,
		Model:            model,
		Timeout:          timeout,
		IdleTimeout:      time.Duration(r.cfg.AgentIdleTimeoutMinutes) * time.Minute,
		ToolAllowlist:    toolAllowlist,
		RunsDir:          runsDir,
		Round:            round,
		Attempt:          attempt,
		Env:              brokerEnv,
	}
}

// startCBMBrokerFn is a variable so tests can replace it without spawning a real npx process.
var startCBMBrokerFn = func(sockPath string, env map[string]string) (*cbmbroker.Broker, error) {
	return cbmbroker.Start(sockPath, env)
}

// gmDevToolNames are added to the dev agent tool allowlist when the GM broker is running.
// gm_dev_done is intentionally omitted while the broker handler is a no-op skeleton.
var gmDevToolNames = []string{"gm_slice_get", "gm_project_check", "gm_dev_done"}

// gmCodeToolNames are added to the dev and reviewer allowlists when CBM is also enabled.
// They expose read-only code-intelligence operations over the gm_ transport.
var gmCodeToolNames = []string{
	"gm_code_search",
	"gm_code_search_graph",
	"gm_code_query_graph",
	"gm_code_trace_call_path",
	"gm_code_get_architecture",
	"gm_code_get_graph_schema",
	"gm_code_get_snippet",
	"gm_code_detect_changes",
}

// gmReviewerToolNames are the gm_ tools available to the reviewer agent.
// bash/edit/write/gm_project_check are excluded (reviewer read-only lockdown).
var gmReviewerToolNames = []string{"gm_slice_get", "gm_pr_view", "gm_repo_tree", "gm_review_submit_comment", "gm_review_submit"}

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
// Failure is logged and does not abort the run.
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
// exhaustion, and returns the event. eventLogPath may be empty for cases where no
// pr comment is needed.
func (r *Runner) handleDevAgentErrorWithLog(eventLogPath string, err error, endSpan func(string, map[string]any), activityPath, stderrPath string) loop.EventKey {
	if errors.Is(err, agent.ErrTimeout) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent exceeded timeout\n")
		return loop.EventAgentTimedOut
	}
	if errors.Is(err, agent.ErrStalled) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent stalled\n")
		return loop.EventAgentStalled
	}
	if errors.Is(err, agent.ErrThinkingLoop) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "dev_failed: dev agent thinking loop\n")
		return loop.EventAgentAborted
	}
	var chainErr *agent.ModelChainExhaustedError
	if errors.As(err, &chainErr) {
		if eventLogPath != "" {
			r.writeAgentCompleted(eventLogPath, "dev", 1, activityPath, stderrPath)
			if prNum, prErr := r.getPRNumber(eventLogPath); prErr == nil {
				r.postModelChainExhaustedComment(prNum, chainErr)
			}
		}
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err)
		return loop.EventDevFailed
	}
	endSpan(telemetry.StatusError, nil)
	fmt.Fprintf(r.stderr, "dev_failed: agent failed: %v\n", err)
	return loop.EventDevFailed
}
