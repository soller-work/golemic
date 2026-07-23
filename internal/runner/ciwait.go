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
	"golemic/internal/prompt"
)

const (
	defaultCIPollInterval = 10 * time.Second
	maxCILogBytes         = 8000
	maxCIFixRounds        = 2

	// branchProtectionRequiredCICheck is the required CI context trusted for green.
	branchProtectionRequiredCICheck = "verify"
)

// ghCheckItem is the internal check representation used throughout the CI gate.
type ghCheckItem struct {
	Name   string
	Bucket string // pass/fail/pending
	Link   string
}

// ghCheckRunItem is one entry from the GitHub check-runs REST API.
type ghCheckRunItem struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, neutral, cancelled, skipped, timed_out, action_required
	HTMLURL    string `json:"html_url"`
}

type ghCheckRunsResponse struct {
	CheckRuns []ghCheckRunItem `json:"check_runs"`
}

// checkRunBucket maps a GitHub check run status/conclusion to a bucket string.
func checkRunBucket(status, conclusion string) string {
	if status != "completed" {
		return "pending"
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return "pass"
	case "failure", "cancelled", "timed_out", "action_required":
		return "fail"
	default:
		return "pending"
	}
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

// getPRHeadSHA returns the current head commit SHA of the given PR.
func (r *Runner) getPRHeadSHA(prNumber int) (string, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "pr", "view", fmt.Sprintf("%d", prNumber), "--json", "headRefOid", "--jq", ".headRefOid",
	)
	if err != nil {
		return "", fmt.Errorf("failed to get PR head SHA: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// getRepoNWO returns the repository "owner/name" string, cached after the first successful call.
func (r *Runner) getRepoNWO() (string, error) {
	if r.cachedNWO != "" {
		return r.cachedNWO, nil
	}
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner",
	)
	if err != nil {
		return "", fmt.Errorf("failed to get repo NWO: %w", err)
	}
	r.cachedNWO = strings.TrimSpace(out)
	return r.cachedNWO, nil
}

// queryCheckRunsForSHA fetches check runs for a specific commit SHA via the GitHub API.
func (r *Runner) queryCheckRunsForSHA(nwo, sha string) ([]ghCheckItem, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "api", fmt.Sprintf("repos/%s/commits/%s/check-runs", nwo, sha),
	)
	if err != nil {
		return nil, fmt.Errorf("CHECKS_QUERY_FAILED: check-runs API failed: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}
	var resp ghCheckRunsResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("CHECKS_QUERY_FAILED: failed to parse check-runs response: %w", err)
	}
	items := make([]ghCheckItem, 0, len(resp.CheckRuns))
	for _, cr := range resp.CheckRuns {
		items = append(items, ghCheckItem{
			Name:   cr.Name,
			Bucket: checkRunBucket(cr.Status, cr.Conclusion),
			Link:   cr.HTMLURL,
		})
	}
	return items, nil
}

// getLocalHeadSHA reads the HEAD commit SHA from the given worktree directory.
func (r *Runner) getLocalHeadSHA(worktreeDir string) (string, error) {
	out, err := r.executor.RunInDir(worktreeDir, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to get local HEAD SHA: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// queryCIChecks queries check runs for the PR's current head SHA and returns a result string.
// Result is one of: "green", "red", "pending".
// Only checks reported for the PR's current head commit are trusted; this prevents
// stale completed checks from a superseded SHA from causing a premature "green".
// A missing required check for the current head is pending so the poll loop waits
// for the workflow to register instead of falling back to a local merge path.
// Returns error on gh invocation or parse failure (CHECKS_QUERY_FAILED).
func (r *Runner) queryCIChecks(prNumber int) (string, []ghCheckItem, error) {
	headSHA, err := r.getPRHeadSHA(prNumber)
	if err != nil {
		return "", nil, fmt.Errorf("CHECKS_QUERY_FAILED: %w", err)
	}
	nwo, err := r.getRepoNWO()
	if err != nil {
		return "", nil, fmt.Errorf("CHECKS_QUERY_FAILED: %w", err)
	}
	checks, err := r.queryCheckRunsForSHA(nwo, headSHA)
	if err != nil {
		return "", nil, err // already CHECKS_QUERY_FAILED prefixed
	}
	if len(checks) == 0 {
		return "pending", nil, nil
	}
	return classifyChecks(checks)
}

// pollCheckRunsForSHA polls the check-runs API for a specific commit SHA until all
// checks complete or ciTimeout expires. Empty check runs are treated as pending so
// the runner waits through the brief window after a force-push before GitHub creates
// new check run objects for the new commit.
func (r *Runner) pollCheckRunsForSHA(sha, nwo string, ciTimeout time.Duration) (string, []ghCheckItem, error) {
	query := func() (string, []ghCheckItem, error) {
		checks, err := r.queryCheckRunsForSHA(nwo, sha)
		if err != nil {
			return "", nil, err
		}
		if len(checks) == 0 {
			return "pending", nil, nil // CI hasn't started yet for this SHA
		}
		return classifyChecks(checks)
	}

	result, failed, err := query()
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
			result, failed, err = query()
			if err != nil {
				return "", nil, err
			}
			if result != "pending" {
				return result, failed, nil
			}
		}
	}
}

// classifyChecks inspects check items and returns the aggregate result.
func classifyChecks(checks []ghCheckItem) (string, []ghCheckItem, error) {
	var failed []ghCheckItem
	hasPending := false
	hasRequiredCheck := false
	for _, c := range checks {
		if c.Name == branchProtectionRequiredCICheck {
			hasRequiredCheck = true
		}
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
	if hasPending || !hasRequiredCheck {
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
// Returns result ("green"|"red"|"timeout") and any failed items.
func (r *Runner) pollCIChecks(prNumber int, ciTimeout time.Duration) (string, []ghCheckItem, error) {
	result, failed, err := r.queryCIChecks(prNumber)
	if err != nil {
		return "", nil, err
	}
	if result == "green" || result == "red" {
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
			if result == "green" || result == "red" {
				return result, failed, nil
			}
		}
	}
}

// writeCIWaitFinished appends a ci_wait_finished event to the event log and
// emits a progress line. Errors are silently dropped per BR-P3.
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
	ev := eventlog.Event{
		Type:    eventlog.EventCIWaitFinished,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  r.turnCounter,
		Payload: payload,
	}
	if w.Write(ev) == nil && r.progressRenderer != nil {
		r.progressRenderer.EmitLifecycle(ev)
	}
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
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	systemPromptFile, model, cleanupPrompt, err := r.resolveAgentFile("dev")
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}
	defer cleanupPrompt()

	userPrompt, err := prompt.RenderDevCIRetry(
		failedCheckInfo,
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
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
		ToolAllowlist:     []string{"read", "bash", "write", "edit"},
		RunsDir:           runsDir,
	})

	if err != nil {
		if errors.Is(err, agent.ErrTimeout) {
			fmt.Fprintf(r.stderr, "dev_failed: CI retry dev agent exceeded timeout\n") //nolint:errcheck
			return outcomeDevFailed
		}
		if errors.Is(err, agent.ErrStalled) {
			fmt.Fprintf(r.stderr, "dev_failed: CI retry dev agent stalled\n") //nolint:errcheck
			return outcomeStalled
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
// Returns outcomeSuccess when checks are green; outcomeDevFailed otherwise.
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

		if result == "green" {
			return outcomeSuccess
		}

		if outcome := r.onCICheckFailed(prNumber, eventLogPath, golemicDir, agentTimeout, fixRound, result, failedChecks); outcome != "" {
			return outcome
		}
	}

	return outcomeDevFailed
}
