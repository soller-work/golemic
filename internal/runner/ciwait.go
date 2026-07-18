package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/prompt"
)

const (
	defaultCIPollInterval = 10 * time.Second
	maxCILogBytes         = 8000
	maxCIFixRounds        = 2
)

// noChecksRe matches the exact stderr line gh emits for a PR on a branch with no CI checks.
var noChecksRe = regexp.MustCompile(`^no checks reported on the '.+' branch$`)

// ghCheckItem is one entry from `gh pr checks --json name,bucket,link`.
type ghCheckItem struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"` // pass/fail/pending/skipping/cancel/actionRequired/waiting
	Link   string `json:"link"`
}

// categorizeBucket maps a gh check bucket to "pass", "fail", or "pending".
func categorizeBucket(bucket string) string {
	switch bucket {
	case "pass", "skipping":
		return "pass"
	case "fail", "cancel", "actionRequired":
		return "fail"
	default:
		return "pending"
	}
}

// queryCIChecks calls gh pr checks and returns a result string and failed items.
// Result is one of: "green", "red", "no_checks", "pending".
// Returns error on gh invocation or parse failure (CHECKS_QUERY_FAILED).
func (r *Runner) queryCIChecks(prNumber int) (string, []ghCheckItem, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "pr", "checks", fmt.Sprintf("%d", prNumber), "--json", "name,bucket,link",
	)
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) && ee.ExitCode == 1 && noChecksRe.MatchString(strings.TrimSpace(ee.Stderr)) {
			return "no_checks", nil, nil
		}
		return "", nil, fmt.Errorf("CHECKS_QUERY_FAILED: %w", err)
	}

	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return "no_checks", nil, nil
	}

	var checks []ghCheckItem
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return "", nil, fmt.Errorf("CHECKS_QUERY_FAILED: failed to parse gh pr checks output: %w", err)
	}

	if len(checks) == 0 {
		return "no_checks", nil, nil
	}

	return classifyChecks(checks)
}

// classifyChecks inspects check items and returns the aggregate result.
func classifyChecks(checks []ghCheckItem) (string, []ghCheckItem, error) {
	var failed []ghCheckItem
	hasPending := false
	for _, c := range checks {
		switch categorizeBucket(c.Bucket) {
		case "fail":
			failed = append(failed, c)
		case "pending":
			hasPending = true
		}
	}
	if len(failed) > 0 {
		return "red", failed, nil
	}
	if hasPending {
		return "pending", nil, nil
	}
	return "green", nil, nil
}

var runIDRegexp = regexp.MustCompile(`/actions/runs/(\d+)`)

// fetchRunLog retrieves the failed-run log for a GitHub Actions run ID.
// Returns empty string if unavailable or empty.
func (r *Runner) fetchRunLog(runID string) string {
	log, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "run", "view", runID, "--log-failed",
	)
	if err != nil || strings.TrimSpace(log) == "" {
		return ""
	}
	if len(log) > maxCILogBytes {
		return log[:maxCILogBytes] + "\n... (truncated)"
	}
	return log
}

// appendCheckLog writes one check's log excerpt to sb.
func (r *Runner) appendCheckLog(sb *strings.Builder, check ghCheckItem) {
	m := runIDRegexp.FindStringSubmatch(check.Link)
	if len(m) >= 2 {
		if log := r.fetchRunLog(m[1]); log != "" {
			fmt.Fprintf(sb, "```\n%s\n```\n", log)
			return
		}
		fmt.Fprintf(sb, "(log unavailable; details: %s)\n", check.Link)
		return
	}
	if check.Link != "" {
		fmt.Fprintf(sb, "Details: %s\n", check.Link)
		return
	}
	sb.WriteString("(no details link available)\n")
}

// collectFailedCheckLogs builds a string with failed check names and truncated log excerpts.
func (r *Runner) collectFailedCheckLogs(failedChecks []ghCheckItem) string {
	if len(failedChecks) == 0 {
		return "No specific failed checks recorded."
	}
	var sb strings.Builder
	for _, check := range failedChecks {
		fmt.Fprintf(&sb, "### %s\n", check.Name)
		r.appendCheckLog(&sb, check)
		sb.WriteString("\n")
	}
	return sb.String()
}

// ciPollInterval returns the CI poll interval (overridable in tests).
func (r *Runner) ciPollInterval() time.Duration {
	if r.ciPollIntervalOverride > 0 {
		return r.ciPollIntervalOverride
	}
	return defaultCIPollInterval
}

// ciTimeout returns the effective CI check wait timeout.
func (r *Runner) ciTimeout() time.Duration {
	if r.ciTimeoutOverride > 0 {
		return r.ciTimeoutOverride
	}
	return time.Duration(r.cfg.CITimeoutMinutes) * time.Minute
}

// pollCIChecks polls gh pr checks until all complete or ciTimeout expires.
// Returns result ("green"|"red"|"timeout"|"no_checks") and any failed items.
func (r *Runner) pollCIChecks(prNumber int, ciTimeout time.Duration) (string, []ghCheckItem, error) {
	result, failed, err := r.queryCIChecks(prNumber)
	if err != nil {
		return "", nil, err
	}
	if result != "pending" {
		return result, failed, nil
	}

	ticker := time.NewTicker(r.ciPollInterval())
	defer ticker.Stop()
	deadline := time.NewTimer(ciTimeout)
	defer deadline.Stop()

	for {
		select {
		case <-deadline.C:
			return "timeout", nil, nil
		case <-ticker.C:
			result, failed, err = r.queryCIChecks(prNumber)
			if err != nil {
				return "", nil, err
			}
			if result != "pending" {
				return result, failed, nil
			}
		}
	}
}

// writeCIWaitFinished appends a ci_wait_finished event to the event log.
// Errors are silently dropped; a log write failure must not change the run outcome.
func (r *Runner) writeCIWaitFinished(eventLogPath, result string, round int) {
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		return
	}
	defer w.Close() //nolint:errcheck

	payload, err := eventlog.MarshalCIWaitFinishedPayload(result, round)
	if err != nil {
		return
	}
	_ = w.Write(eventlog.Event{
		Type:    eventlog.EventCIWaitFinished,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  r.turnCounter,
		Payload: payload,
	})
}

// postCIEscalationComment posts a PR comment explaining why the CI gate escalated.
// Comment errors are logged to stderr but do not change the outcome.
func (r *Runner) postCIEscalationComment(prNumber int, message string) {
	body := fmt.Sprintf(
		"golemic: %s for issue #%d (PR #%d). Human intervention required.",
		message, r.issueNum, prNumber,
	)
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "pr", "comment", fmt.Sprintf("%d", prNumber), "--body", body,
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to post CI escalation comment: %v\n", err) //nolint:errcheck
	}
}

// getRemoteBranchSHA returns the current SHA of the branch on origin.
// Returns empty string if the branch doesn't exist on the remote.
func (r *Runner) getRemoteBranchSHA() (string, error) {
	out, err := r.executor.RunInDir(
		r.repoRoot,
		"git", "ls-remote", "origin", r.branchName,
	)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// runDevCIRetryAgent runs the dev agent in the existing dev worktree to fix CI failures.
func (r *Runner) runDevCIRetryAgent(golemicDir, eventLogPath string, timeout time.Duration, failedCheckInfo string) string {
	golemicBinaryPath, _ := os.Executable()
	binaryDir := filepath.Dir(golemicBinaryPath)
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	userPrompt, err := prompt.RenderDevCIRetry(
		failedCheckInfo,
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
			Body:   r.issue.Body,
		},
		r.branchName,
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, ".golemic", "guidelines", "dev.md"),
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}

	r.turnCounter++ // CI retry dev agent gets its own turn
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
			fmt.Fprintf(r.stderr, "dev_failed: CI retry dev agent exceeded timeout\n") //nolint:errcheck
			return outcomeDevFailed
		}
		fmt.Fprintf(r.stderr, "dev_failed: CI retry agent failed: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}

	r.writeAgentCompleted(eventLogPath, "dev", exitCode)

	if exitCode != 0 {
		fmt.Fprintf(r.stderr, "dev_failed: CI retry dev agent exited with code %d; see %s\n", exitCode, paths.Stderr) //nolint:errcheck
		return outcomeDevFailed
	}

	return outcomeSuccess
}

// onCICheckFailed handles the red/timeout branch in runCIGate.
// Returns "" if the dev retry succeeded and the gate loop should continue,
// or an outcome string if the run should terminate.
func (r *Runner) onCICheckFailed(prNumber int, eventLogPath, golemicDir string, agentTimeout time.Duration, fixRound int, result string, failedChecks []ghCheckItem) string {
	if fixRound >= maxCIFixRounds {
		totalAttempts := 1 + maxCIFixRounds
		msg := fmt.Sprintf("CI build failed after %d attempt(s)", totalAttempts)
		fmt.Fprintf(r.stderr, "dev_failed: %s\n", msg) //nolint:errcheck
		r.postCIEscalationComment(prNumber, msg)
		return outcomeDevFailed
	}

	failedCheckInfo := r.buildFailedCheckInfo(result, failedChecks)

	shaBefore, err := r.getRemoteBranchSHA()
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: failed to read remote branch SHA: %v\n", err) //nolint:errcheck
		r.postCIEscalationComment(prNumber, "failed to read remote branch state before CI retry")
		return outcomeDevFailed
	}

	if devOutcome := r.runDevCIRetryAgent(golemicDir, eventLogPath, agentTimeout, failedCheckInfo); devOutcome != outcomeSuccess {
		r.postCIEscalationComment(prNumber, "CI retry dev agent failed")
		return outcomeDevFailed
	}

	shaAfter, err := r.getRemoteBranchSHA()
	if err != nil || shaAfter == "" || shaAfter == shaBefore {
		fmt.Fprintf(r.stderr, "dev_failed: CI retry dev agent did not push new commits\n") //nolint:errcheck
		r.postCIEscalationComment(prNumber, "CI retry dev agent did not push new commits")
		return outcomeDevFailed
	}

	return "" // continue gate loop
}

// buildFailedCheckInfo constructs the failed check info string for the retry prompt.
func (r *Runner) buildFailedCheckInfo(result string, failedChecks []ghCheckItem) string {
	if result == "red" && len(failedChecks) > 0 {
		return r.collectFailedCheckLogs(failedChecks)
	}
	return fmt.Sprintf("CI checks %s; no specific failure logs available.", result)
}

// runCIGate implements the CI gate phase between pr_opened and reviewer creation.
// It polls PR checks and retries the dev agent up to maxCIFixRounds times on failure.
// Returns outcomeSuccess when checks are green or absent; outcomeDevFailed otherwise.
func (r *Runner) runCIGate(prNumber int, eventLogPath string, agentTimeout time.Duration) string {
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	ciTimeout := r.ciTimeout()

	for fixRound := 0; fixRound <= maxCIFixRounds; fixRound++ {
		result, failedChecks, err := r.pollCIChecks(prNumber, ciTimeout)
		if err != nil {
			fmt.Fprintf(r.stderr, "dev_failed: %v\n", err) //nolint:errcheck
			r.postCIEscalationComment(prNumber, "CI check query failed")
			return outcomeDevFailed
		}

		r.writeCIWaitFinished(eventLogPath, result, fixRound)

		if result == "green" || result == "no_checks" {
			return outcomeSuccess
		}

		if outcome := r.onCICheckFailed(prNumber, eventLogPath, golemicDir, agentTimeout, fixRound, result, failedChecks); outcome != "" {
			return outcome
		}
	}

	return outcomeDevFailed
}
