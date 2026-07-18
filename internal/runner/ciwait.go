package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/prompt"
	"golemic/internal/worktree"
)

// maxCIFixRounds is the maximum number of dev retry rounds allowed after CI failure.
const maxCIFixRounds = 2

// maxLogExcerptBytes caps the total size of log excerpts included in the retry prompt.
const maxLogExcerptBytes = 8192

// ghCheck is the parsed shape of one entry from gh pr checks --json name,state.
type ghCheck struct {
	Name  string `json:"name"`
	State string `json:"state"` // pass, fail, pending, skipping, error
}

// ghRun is the parsed shape of one entry from gh run list --json databaseId,conclusion.
type ghRun struct {
	DatabaseID int    `json:"databaseId"`
	Conclusion string `json:"conclusion"` // failure, success, etc.
	Status     string `json:"status"`     // completed, in_progress, etc.
}

// pollCheckResult is the outcome of one check-poll cycle.
type pollCheckResult struct {
	result       string   // green, red, timeout, no_checks
	failedChecks []string // check names that failed (for red result)
}

// runCIGate implements the CI wait phase between pr_opened and reviewer worktree creation.
// It polls checks, retries the dev agent on failure, and escalates after maxCIFixRounds.
// Returns the final outcome string.
func (r *Runner) runCIGate(writer worktree.EventWriter, eventLogPath string, prNumber int, golemicDir string, timeoutDuration time.Duration) string {
	fixRoundsUsed := 0

	for {
		// PS-001: Wait for checks to complete.
		poll := r.waitForChecks(prNumber, r.cfg.CITimeoutMinutes)
		round := fixRoundsUsed // round number for event (0 = initial wait, 1+ = after retry)

		// Write ci_wait_finished event (BR-006).
		if payload, err := eventlog.MarshalCIWaitFinishedPayload(poll.result, round); err == nil {
			w, werr := eventlog.NewWriter(eventLogPath)
			if werr == nil {
				_ = w.Write(eventlog.Event{
					Type:    eventlog.EventCIWaitFinished,
					Ts:      time.Now().Format(time.RFC3339),
					RunID:   r.runID,
					Payload: payload,
				})
				_ = w.Close()
			}
		}

		switch poll.result {
		case "green", "no_checks":
			// PS-004: Proceed to reviewer.
			return ""

		case "red", "timeout":
			// PS-002: Budget check.
			if fixRoundsUsed >= maxCIFixRounds {
				// Retries exhausted — escalate (BR-004, BR-005).
				msg := fmt.Sprintf("CI build failed after %d attempt(s); giving up. See PR #%d for details.", fixRoundsUsed+1, prNumber)
				r.postPRComment(prNumber, msg)
				fmt.Fprintf(r.stderr, "dev_failed: %s\n", msg) //nolint:errcheck
				return outcomeDevFailed
			}

			// PS-003: Dev retry round.
			fixRoundsUsed++
			failedChecks := r.collectFailedLogs(poll.failedChecks)
			retryOutcome := r.runDevRetryAgent(golemicDir, eventLogPath, timeoutDuration, failedChecks)
			if retryOutcome != outcomeSuccess {
				msg := fmt.Sprintf("CI fix attempt %d failed (dev agent error). See PR #%d for details.", fixRoundsUsed, prNumber)
				r.postPRComment(prNumber, msg)
				fmt.Fprintf(r.stderr, "dev_failed: retry round %d failed with outcome %q\n", fixRoundsUsed, retryOutcome) //nolint:errcheck
				return outcomeDevFailed
			}

		default:
			// CHECKS_QUERY_FAILED: fail-closed (IF-001 error).
			msg := fmt.Sprintf("Cannot determine CI check state (query failed). See PR #%d.", prNumber)
			r.postPRComment(prNumber, msg)
			fmt.Fprintf(r.stderr, "dev_failed: CI check query failed\n") //nolint:errcheck
			return outcomeDevFailed
		}
	}
}

// waitForChecks polls gh pr checks every 10 seconds until all checks complete or
// ci_timeout_minutes expires. Returns pollCheckResult with one of:
//   - "green"     all checks passed
//   - "red"       at least one check failed
//   - "timeout"   checks still pending after ci_timeout_minutes
//   - "no_checks" PR has no checks configured
//   - ""          check query failed (caller must handle as fail-closed)
func (r *Runner) waitForChecks(prNumber, ciTimeoutMinutes int) pollCheckResult {
	deadline := time.Now().Add(time.Duration(ciTimeoutMinutes) * time.Minute)

	for {
		checks, err := r.queryPRChecks(prNumber)
		if err != nil {
			return pollCheckResult{result: ""}
		}
		if len(checks) == 0 {
			return pollCheckResult{result: "no_checks"}
		}
		if pr := r.resolvePollResult(checks, deadline); pr != nil {
			return *pr
		}
		time.Sleep(10 * time.Second)
	}
}

// resolvePollResult converts check states to a terminal pollCheckResult, or nil when
// checks are still pending and the deadline has not expired.
func (r *Runner) resolvePollResult(checks []ghCheck, deadline time.Time) *pollCheckResult {
	result, failed := evaluateChecks(checks)
	switch result {
	case "green":
		return &pollCheckResult{result: "green"}
	case "red":
		return &pollCheckResult{result: "red", failedChecks: failed}
	default:
		if time.Now().After(deadline) {
			return &pollCheckResult{result: "timeout", failedChecks: pendingNames(checks)}
		}
		return nil
	}
}

// pendingNames returns the names of checks that are still in a pending state.
func pendingNames(checks []ghCheck) []string {
	var names []string
	for _, c := range checks {
		switch strings.ToLower(c.State) {
		case "pending", "in_progress", "queued", "waiting", "requested":
			names = append(names, c.Name+" (timed out)")
		}
	}
	return names
}

// queryPRChecks calls gh pr checks --json name,state and returns the parsed checks.
// Returns (nil, nil) when there are no checks (empty array or no-checks response).
// Returns (nil, err) on query failure.
func (r *Runner) queryPRChecks(prNumber int) ([]ghCheck, error) {
	env := map[string]string{"GH_TOKEN": r.creds.DevToken()}
	out, err := r.executor.RunWithEnvInDir(env, r.repoRoot, "gh", "pr", "checks",
		strconv.Itoa(prNumber), "--json", "name,state")
	if err != nil {
		// gh pr checks exits non-zero when there are no checks ("no checks to display").
		// Detect this case by checking the error message.
		if isNoChecksError(err.Error(), out) {
			return nil, nil
		}
		return nil, fmt.Errorf("CHECKS_QUERY_FAILED: %w", err)
	}

	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}

	var checks []ghCheck
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		return nil, fmt.Errorf("CHECKS_QUERY_FAILED: failed to parse gh pr checks output: %w", err)
	}
	return checks, nil
}

// isNoChecksError returns true if the error message and output indicate no checks are configured.
func isNoChecksError(errMsg, output string) bool {
	lower := strings.ToLower(errMsg + " " + output)
	return strings.Contains(lower, "no checks") ||
		strings.Contains(lower, "no check") ||
		strings.Contains(lower, "no status checks")
}

// evaluateChecks determines the overall check state.
// Returns ("green", nil), ("red", failedNames), or ("pending", nil).
func evaluateChecks(checks []ghCheck) (string, []string) {
	var failed []string
	hasPending := false

	for _, c := range checks {
		switch strings.ToLower(c.State) {
		case "fail", "failure", "error", "action_required", "cancelled", "timed_out":
			failed = append(failed, c.Name)
		case "pending", "in_progress", "queued", "waiting", "requested":
			hasPending = true
		// pass, success, neutral, skipping, skipped → pass-through
		}
	}

	if len(failed) > 0 {
		return "red", failed
	}
	if hasPending {
		return "pending", nil
	}
	return "green", nil
}

// collectFailedLogs gathers failed check names and log excerpts from gh run view --log-failed.
// The result is size-bounded. On any error it returns just the check names without logs.
func (r *Runner) collectFailedLogs(failedChecks []string) []string {
	if len(failedChecks) == 0 {
		return nil
	}

	entries := make([]string, len(failedChecks))
	copy(entries, failedChecks)

	// Try to fetch log excerpts for failing GitHub Actions runs.
	env := map[string]string{"GH_TOKEN": r.creds.DevToken()}
	runsOut, err := r.executor.RunWithEnvInDir(env, r.repoRoot, "gh", "run", "list",
		"--branch", r.branchName,
		"--json", "databaseId,conclusion,status",
		"--limit", "5")
	if err != nil {
		return entries
	}

	var runs []ghRun
	if err := json.Unmarshal([]byte(strings.TrimSpace(runsOut)), &runs); err != nil {
		return entries
	}

	totalBytes := 0
	for _, run := range runs {
		if run.Status != "completed" || run.Conclusion == "success" {
			continue
		}
		logOut, err := r.executor.RunWithEnvInDir(env, r.repoRoot, "gh", "run", "view",
			strconv.Itoa(run.DatabaseID), "--log-failed")
		if err != nil {
			continue
		}
		excerpt := truncate(logOut, maxLogExcerptBytes-totalBytes)
		if excerpt == "" {
			break
		}
		entries = append(entries, fmt.Sprintf("Log excerpt (run %d):\n%s", run.DatabaseID, excerpt))
		totalBytes += len(excerpt)
		if totalBytes >= maxLogExcerptBytes {
			break
		}
	}

	return entries
}

// truncate returns at most maxBytes bytes from s, trimmed to a line boundary.
func truncate(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	// Trim to last newline within the limit.
	cut := s[:maxBytes]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		return cut[:idx]
	}
	return cut
}

// runDevRetryAgent re-invokes the dev agent in the existing dev worktree with a retry prompt.
func (r *Runner) runDevRetryAgent(golemicDir, eventLogPath string, timeout time.Duration, failedChecks []string) string {
	golemicBinaryPath, _ := os.Executable()
	binaryDir := filepath.Dir(golemicBinaryPath)
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	// Snapshot remote SHA before retry to detect whether the dev actually pushed.
	shaBeforeRetry := r.remoteHeadSHA(r.branchName)

	userPrompt, err := prompt.RenderDevRetry(
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
			Body:   r.issue.Body,
		},
		r.branchName,
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, ".golemic", "guidelines", "dev.md"),
		failedChecks,
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render dev retry prompt: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}

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
		GHToken:           r.creds.DevToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             r.cfg.Models.Dev,
		Timeout:           timeout,
		ToolAllowlist:     []string{"read", "bash", "write", "edit"},
		RunsDir:           runsDir,
	})

	if err != nil {
		if errors.Is(err, agent.ErrTimeout) {
			fmt.Fprintf(r.stderr, "dev_failed: dev retry agent exceeded timeout\n") //nolint:errcheck
			return outcomeTimeout
		}
		fmt.Fprintf(r.stderr, "dev_failed: dev retry agent failed: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}

	r.writeAgentCompleted(eventLogPath, "dev", exitCode)

	if exitCode != 0 {
		fmt.Fprintf(r.stderr, "dev_failed: dev retry agent exited with code %d; see %s\n", exitCode, paths.Stderr) //nolint:errcheck
		return outcomeDevFailed
	}

	// Detect missing push: if remote SHA is unchanged, the dev didn't push.
	if shaBeforeRetry != "" && r.remoteHeadSHA(r.branchName) == shaBeforeRetry {
		fmt.Fprintf(r.stderr, "dev_failed: dev retry agent did not push any new commits\n") //nolint:errcheck
		return outcomeDevFailed
	}

	return outcomeSuccess
}

// remoteHeadSHA returns the current SHA of branchName on origin, or "" on error.
func (r *Runner) remoteHeadSHA(branchName string) string {
	out, err := r.executor.RunInDir(r.repoRoot, "git", "ls-remote", "origin", "refs/heads/"+branchName)
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// postPRComment posts a comment to the PR. Errors are logged to stderr but do not
// change the run outcome (SE-001 failure policy).
func (r *Runner) postPRComment(prNumber int, body string) {
	env := map[string]string{"GH_TOKEN": r.creds.DevToken()}
	_, err := r.executor.RunWithEnvInDir(env, r.repoRoot, "gh", "pr", "comment",
		strconv.Itoa(prNumber), "--body", body)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to post PR comment: %v\n", err) //nolint:errcheck
	}
}
