package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/gmbroker"
	"golemic/internal/prompt"
	"golemic/internal/telemetry"
	"golemic/internal/worktreefingerprint"
)

// graphqlDiscoverPending queries the viewer's PENDING reviews on a PR.
// states:[PENDING] already scopes to the token's own pending reviews, so no author filter is needed
// (and the reviews connection's author arg is a String login, not an object — GitHub rejects object literals).
const graphqlDiscoverPending = `query($owner:String!,$name:String!,$prNumber:Int!){repository(owner:$owner,name:$name){pullRequest(number:$prNumber){reviews(first:1,states:[PENDING]){nodes{id}}}}}`

// graphqlDeleteReview deletes a pending review by its node ID.
const graphqlDeleteReview = `mutation($reviewId:ID!){deletePullRequestReview(input:{pullRequestReviewId:$reviewId}){pullRequestReview{id}}}`

// findingEntry is one inline comment from a review, transformed into FindingsJSON shape.
type findingEntry struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// repoNWO resolves the current repository's owner/name string via gh.
func (r *Runner) repoNWO() (string, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "repo", "view", "--json", "owner,name",
	)
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	var v struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", fmt.Errorf("gh repo view parse: %w", err)
	}
	if v.Owner.Login == "" || v.Name == "" {
		return "", fmt.Errorf("gh repo view: owner or name empty")
	}
	return v.Owner.Login + "/" + v.Name, nil
}

// sweepPendingReviews deletes any viewer PENDING review on the PR before a reviewer round.
// An empty result (no pending review) is not an error.
func (r *Runner) sweepPendingReviews(prNumber int) error {
	nwo, err := r.repoNWO()
	if err != nil {
		return fmt.Errorf("failed to resolve repo: %w", err)
	}
	parts := strings.SplitN(nwo, "/", 2)
	owner, repoName := parts[0], parts[1]

	// IC-001: discover pending reviews
	discoverOut, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", "graphql",
		"-f", "query="+graphqlDiscoverPending,
		"-f", "owner="+owner,
		"-f", "name="+repoName,
		"-F", fmt.Sprintf("prNumber=%d", prNumber),
	)
	if err != nil {
		return fmt.Errorf("review_failed: failed to discover pending reviews: %w", err)
	}

	var discoverResp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					Reviews struct {
						Nodes []struct {
							ID string `json:"id"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(discoverOut), &discoverResp); err != nil {
		return fmt.Errorf("review_failed: failed to parse discover response: %w", err)
	}

	nodes := discoverResp.Data.Repository.PullRequest.Reviews.Nodes
	if len(nodes) == 0 {
		return nil // no orphaned pending review; proceed
	}

	// IC-002: delete the orphaned pending review
	_, err = r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", "graphql",
		"-f", "query="+graphqlDeleteReview,
		"-f", "reviewId="+nodes[0].ID,
	)
	if err != nil {
		return fmt.Errorf("review_failed: failed to delete pending review %s: %w", nodes[0].ID, err)
	}
	return nil
}

// loadInlineComments fetches the inline comments for a specific review via REST
// and returns them as a FindingsJSON array. Empty slice is valid (no inline comments).
func (r *Runner) loadInlineComments(prNumber int, reviewID string) ([]findingEntry, error) {
	nwo, err := r.repoNWO()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repo: %w", err)
	}

	// IC-003: REST GET reviews/{id}/comments
	path := fmt.Sprintf("repos/%s/pulls/%d/reviews/%s/comments", nwo, prNumber, reviewID)
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", path,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load inline comments: %w", err)
	}

	var raw []struct {
		Path         string `json:"path"`
		Line         *int   `json:"line"`
		OriginalLine *int   `json:"original_line"`
		Side         string `json:"side"`
		Body         string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse inline comments: %w", err)
	}

	entries := make([]findingEntry, 0, len(raw))
	for _, c := range raw {
		line := 0
		if c.Line != nil {
			line = *c.Line
		} else if c.OriginalLine != nil {
			line = *c.OriginalLine
		}
		side := c.Side
		if side == "" {
			side = "RIGHT"
		}
		entries = append(entries, findingEntry{
			Path: c.Path,
			Line: line,
			Side: side,
			Body: c.Body,
		})
	}
	return entries, nil
}

// reviewerInvocationState captures the GM broker output after a reviewer agent invocation.
type reviewerInvocationState struct {
	reviewSubmitParams       *gmbroker.ReviewSubmitParams
	reviewSubmitGateRejected bool
	reviewSubmitGateMsg      string
	pendingReviewID          string
}

// runReviewerAgent runs the reviewer agent and returns the outcome and broker state.
// precheckBlock is the pre-rendered precheck section for the prompt.
// gateRetryReason is non-empty when this is a retry after a rejected approved verdict;
// in that case a gate-retry prompt is rendered instead of the normal reviewer prompt.
func (r *Runner) runReviewerAgent(golemicDir, eventLogPath string, timeout time.Duration, parentSpanID string, round int, precheckBlock string, precheck *reviewerPrecheckResult, gateRetryReason string) (string, *reviewerInvocationState) {
	golemicBinaryPath, _ := os.Executable()
	reviewerWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	systemPromptFile, model, cleanupPrompt, err := r.resolveAgentFile("reviewer")
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed, nil
	}
	defer cleanupPrompt()

	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to get PR number: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed, nil
	}

	cbmEnabled, brokerEnv, gmBroker, cleanupBrokers := r.startReviewerBrokers(golemicDir, runsDir, reviewerWorktreePath, prNumber, precheck)
	defer cleanupBrokers()

	userPrompt, err := r.renderReviewerPrompt(prNumber, precheckBlock, gateRetryReason, cbmEnabled)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render reviewer prompt: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed, nil
	}

	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanAgentTurn,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "role": "reviewer", "round": round, "model": model})
	stopFollow := followActivity(r.progressRenderer, "reviewer", filepath.Join(runsDir, r.runID, "reviewer.activity.jsonl"))

	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}
	cfg := r.buildReviewerRoleConfig(systemPromptFile, userPrompt, reviewerWorktreePath, golemicBinaryPath, model, eventLogPath, runsDir, timeout, brokerEnv)
	exitCode, paths, runErr := runFn(context.Background(), cfg)
	stopFollow()

	return r.handleReviewerAgentResult(runErr, exitCode, paths, eventLogPath, endSpan), captureReviewerBrokerState(gmBroker)
}

// renderReviewerPrompt renders either the normal reviewer prompt or a gate-retry prompt.
func (r *Runner) renderReviewerPrompt(prNumber int, precheckBlock, gateRetryReason string, cbmEnabled bool) (string, error) {
	guidelinesPath := filepath.Join(r.repoRoot, ".golemic", "guidelines", "reviewer.md")
	issue := prompt.Issue{Number: r.issue.Number, Title: r.issue.Title}
	if gateRetryReason != "" {
		return prompt.RenderReviewerGateRetry(gateRetryReason, prNumber, issue, r.cfg.VerifyCommand, guidelinesPath, precheckBlock, cbmEnabled)
	}
	return prompt.RenderReviewer(prNumber, issue, r.cfg.VerifyCommand, guidelinesPath, cbmEnabled, precheckBlock)
}

// captureReviewerBrokerState extracts relevant state from the GM broker after the agent exits.
func captureReviewerBrokerState(gmBroker *gmbroker.Broker) *reviewerInvocationState {
	if gmBroker == nil {
		return nil
	}
	p, _ := gmBroker.ReviewSubmitResult()
	return &reviewerInvocationState{
		reviewSubmitParams:       p,
		reviewSubmitGateRejected: gmBroker.ReviewSubmitGateRejected(),
		reviewSubmitGateMsg:      gmBroker.ReviewSubmitGateReason(),
		pendingReviewID:          gmBroker.PendingReviewID(),
	}
}

// handleReviewerAgentResult translates the agent run result into an outcome string.
func (r *Runner) handleReviewerAgentResult(err error, exitCode int, paths agent.TranscriptPaths, eventLogPath string, endSpan func(string, map[string]any)) string {
	if err != nil {
		return r.handleReviewerAgentError(err, exitCode, eventLogPath, endSpan)
	}

	// Record agent exit code in event log (BR-004)
	r.writeAgentCompleted(eventLogPath, "reviewer", exitCode)
	r.emitAgentWrittenEvents(eventLogPath)

	if exitCode != 0 {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "review_failed: reviewer agent exited with code %d; see %s\n", exitCode, paths.Stderr) //nolint:errcheck
		return outcomeReviewFailed
	}

	endSpan(telemetry.StatusOK, nil)
	return outcomeSuccess
}

// handleReviewerAgentError translates a non-nil agent.RunRole error into an outcome.
func (r *Runner) handleReviewerAgentError(err error, _ int, eventLogPath string, endSpan func(string, map[string]any)) string {
	if errors.Is(err, agent.ErrTimeout) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "review_failed: reviewer agent exceeded timeout\n") //nolint:errcheck
		return outcomeTimeout
	}
	if errors.Is(err, agent.ErrStalled) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "review_failed: reviewer agent stalled\n") //nolint:errcheck
		return outcomeStalled
	}
	if errors.Is(err, agent.ErrThinkingLoop) {
		endSpan(telemetry.StatusKilled, nil)
		fmt.Fprintf(r.stderr, "review_failed: reviewer agent thinking loop\n") //nolint:errcheck
		return outcomeAborted
	}
	var chainErr *agent.ModelChainExhaustedError
	if errors.As(err, &chainErr) {
		r.writeAgentCompleted(eventLogPath, "reviewer", 1)
		r.emitAgentWrittenEvents(eventLogPath)
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "review_failed: %v\n", err) //nolint:errcheck
		if prNum, prErr := r.getPRNumber(eventLogPath); prErr == nil {
			r.postModelChainExhaustedComment(prNum, chainErr)
		}
		return outcomeReviewFailed
	}
	endSpan(telemetry.StatusError, nil)
	fmt.Fprintf(r.stderr, "review_failed: agent failed: %v\n", err) //nolint:errcheck
	return outcomeReviewFailed
}

// buildFindingsJSON loads inline comments for the latest review and returns them
// serialised as a JSON string. Returns an empty string (not an error) when there
// are no inline comments. Returns an error only when the reviewId is missing or
// the REST call fails.
func (r *Runner) buildFindingsJSON(eventLogPath string) (string, error) {
	reviewID, err := r.latestReviewID(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("failed to load inline comments: %w", err)
	}
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("failed to load inline comments: %w", err)
	}
	entries, err := r.loadInlineComments(prNumber, reviewID)
	if err != nil {
		return "", fmt.Errorf("failed to load inline comments: %w", err)
	}
	if len(entries) == 0 {
		return "", nil
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("failed to marshal FindingsJSON: %w", err)
	}
	return string(b), nil
}

// getPRNumber extracts the PR number from the pr_opened event in the log.
func (r *Runner) getPRNumber(eventLogPath string) (int, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return 0, err
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventPROpened {
			var payload map[string]interface{}
			if err := json.Unmarshal(events[i].Payload, &payload); err != nil {
				return 0, err
			}
			prNumStr, ok := payload["prNumber"].(string)
			if !ok {
				return 0, fmt.Errorf("prNumber field not a string in pr_opened event")
			}
			var prNum int
			if _, err := fmt.Sscanf(prNumStr, "%d", &prNum); err != nil {
				return 0, err
			}
			return prNum, nil
		}
	}
	return 0, fmt.Errorf("pr_opened event not found")
}

// reviewerPrecheckResult holds the result of running the reviewer precheck (arch §11).
type reviewerPrecheckResult struct {
	OK                bool   `json:"ok"`
	Command           string `json:"command"`
	ExitCode          int    `json:"exitCode"`
	Stdout            string `json:"stdout"`
	Stderr            string `json:"stderr"`
	BeforeFingerprint string `json:"beforeFingerprint"`
	AfterFingerprint  string `json:"afterFingerprint"`
}

// runReviewerPrecheck runs the reviewer precheck before each reviewer attempt.
// It computes before/after working-tree fingerprints, runs config.VerifyCommand
// in the reviewer worktree, writes a reviewer_precheck event, and returns the
// precheck block string and the structured result.
//
// Returns ("", nil, error) when fingerprint computation fails; the caller should
// surface review_failed in this case. A non-zero verify exit code is not an
// error; it produces an ok:false precheck and the run continues.
func (r *Runner) runReviewerPrecheck(reviewerWorktreePath, eventLogPath string) (string, *reviewerPrecheckResult, error) {
	// Allow injection for tests.
	if r.reviewerPrecheckFn != nil {
		block, err := r.reviewerPrecheckFn(reviewerWorktreePath, eventLogPath)
		if err != nil {
			return "", nil, err
		}
		// Read the precheck result from the event log so callers can use it.
		result := r.readLastPrecheckResult(eventLogPath)
		return block, result, nil
	}
	return runReviewerPrecheckImpl(r, reviewerWorktreePath, eventLogPath)
}

// readLastPrecheckResult reads the last reviewer_precheck event from the log.
// Returns nil when no event is found (safe for callers to handle as missing precheck).
func (r *Runner) readLastPrecheckResult(eventLogPath string) *reviewerPrecheckResult {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return nil
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewerPrecheck {
			var d struct {
				OK                bool   `json:"ok"`
				BeforeFingerprint string `json:"beforeFingerprint"`
				AfterFingerprint  string `json:"afterFingerprint"`
			}
			if err := json.Unmarshal(events[i].Payload, &d); err != nil {
				return nil
			}
			return &reviewerPrecheckResult{
				OK:                d.OK,
				BeforeFingerprint: d.BeforeFingerprint,
				AfterFingerprint:  d.AfterFingerprint,
			}
		}
	}
	return nil
}

func runReviewerPrecheckImpl(r *Runner, worktreePath, eventLogPath string) (string, *reviewerPrecheckResult, error) {
	cmd := r.cfg.VerifyCommand

	before, err := worktreefingerprint.Compute(worktreePath, r.executor)
	if err != nil {
		return "", nil, fmt.Errorf("reviewer_precheck: compute beforeFingerprint: %w", err)
	}

	stdout, stderr, exitCode := runPrecheckVerify(worktreePath, cmd)

	after, err := worktreefingerprint.Compute(worktreePath, r.executor)
	if err != nil {
		return "", nil, fmt.Errorf("reviewer_precheck: compute afterFingerprint: %w", err)
	}

	ok := exitCode == 0 && before == after

	result := &reviewerPrecheckResult{
		OK:                ok,
		Command:           cmd,
		ExitCode:          exitCode,
		Stdout:            stdout,
		Stderr:            stderr,
		BeforeFingerprint: before,
		AfterFingerprint:  after,
	}

	writeReviewerPrecheckEvent(r, eventLogPath, result)
	fmt.Fprintf(r.stderr, "reviewer_precheck: ok=%v exitCode=%d before=%s after=%s\n",
		ok, exitCode, before, after)

	return buildReviewerPrecheckBlock(result), result, nil
}

// runPrecheckVerify runs cmd via sh -c in worktreePath and returns stdout, stderr,
// and the exit code. A non-zero exit code is returned as exitCode, not as an error.
func runPrecheckVerify(worktreePath, cmd string) (stdout, stderr string, exitCode int) {
	if cmd == "" {
		return "", "", 0
	}

	c := exec.Command("sh", "-c", cmd) //nolint:gosec
	c.Dir = worktreePath

	// Inherit PATH from login shell so toolchain is found.
	pathOut, err := exec.Command("sh", "-l", "-c", "echo $PATH").Output()
	if err == nil {
		c.Env = append(os.Environ(), "PATH="+strings.TrimSpace(string(pathOut)))
	}

	var outBuf, errBuf strings.Builder
	c.Stdout = &outBuf
	c.Stderr = &errBuf

	runErr := c.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return stdout, stderr, exitErr.ExitCode()
		}
		// System-level error: treat as exit 1 but include the error message.
		return stdout, stderr + runErr.Error(), 1
	}
	return stdout, stderr, 0
}

// writeReviewerPrecheckEvent appends a reviewer_precheck event to the event log.
func writeReviewerPrecheckEvent(r *Runner, eventLogPath string, res *reviewerPrecheckResult) {
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		return
	}
	defer w.Close() //nolint:errcheck

	summary := "verify passed and tree unchanged"
	if !res.OK {
		if res.ExitCode != 0 {
			summary = fmt.Sprintf("verify failed (exit %d)", res.ExitCode)
		} else {
			summary = "verify passed but tree was mutated"
		}
	}

	payload, _ := json.Marshal(map[string]any{
		"exitCode":          res.ExitCode,
		"ok":                res.OK,
		"beforeFingerprint": res.BeforeFingerprint,
		"afterFingerprint":  res.AfterFingerprint,
		"summary":           summary,
	})

	_ = w.Write(eventlog.Event{
		Type:    eventlog.EventReviewerPrecheck,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  r.turnCounter,
		Payload: payload,
	})
}

const precheckTailBytes = 8 * 1024

// buildReviewerPrecheckBlock builds the precheck block string for injection into the reviewer prompt.
func buildReviewerPrecheckBlock(res *reviewerPrecheckResult) string {
	treeMutated := res.BeforeFingerprint != res.AfterFingerprint
	var sb strings.Builder

	sb.WriteString("## Precheck Result\n\n")
	if res.OK {
		sb.WriteString(fmt.Sprintf("ok: true | command: `%s` | exitCode: 0 | tree-mutated: false | verify passed and tree unchanged\n",
			res.Command))
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("ok: false | command: `%s` | exitCode: %d | tree-mutated: %v\n\n",
		res.Command, res.ExitCode, treeMutated))
	sb.WriteString("**Action required:** The precheck was not ok. You MUST submit `changes_requested` and explain why in the review body.\n")

	combined := res.Stdout
	if res.Stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += res.Stderr
	}
	if combined == "" {
		return sb.String()
	}

	sb.WriteString("\nOutput tail:\n")
	tail, truncated := tailBytes(combined, precheckTailBytes)
	if truncated {
		omitted := len(combined) - len(tail)
		sb.WriteString(fmt.Sprintf("... <%d bytes truncated> ...\n", omitted))
	}
	sb.WriteString(tail)
	return sb.String()
}

// tailBytes returns the last n bytes of s and whether it was truncated.
func tailBytes(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	start := len(s) - n
	// Align to valid UTF-8 boundary.
	for start < len(s) && s[start]&0xC0 == 0x80 {
		start++
	}
	return s[start:], true
}

// buildReviewerToolList builds the reviewer tool allowlist (BR-9: read-only).
// bash/edit/write are excluded; gm_ reviewer tools are added when the GM broker
// socket is present in the environment.
func buildReviewerToolList(brokerEnv []string) []string {
	tools := []string{"read"}
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
		tools = append(tools, gmReviewerToolNames...)
		if hasCBMSock {
			tools = append(tools, gmCodeToolNames...)
		}
	}
	return tools
}

// buildReviewerRoleConfig assembles the agent.RoleConfig for a reviewer invocation.
func (r *Runner) buildReviewerRoleConfig(systemPromptFile, userPrompt, worktreePath, golemicBinaryPath, model, eventLogPath, runsDir string, timeout time.Duration, brokerEnv []string) agent.RoleConfig {
	return agent.RoleConfig{
		Role:              "reviewer",
		SystemPromptFile:  systemPromptFile,
		UserPrompt:        userPrompt,
		WorktreeDir:       worktreePath,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		TurnID:            r.turnCounter,
		GHToken:           r.creds.ReviewerToken(),
		DevToken:          r.creds.DevToken(),
		ReviewerToken:     r.creds.ReviewerToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             model,
		Timeout:           timeout,
		IdleTimeout:       time.Duration(r.cfg.AgentIdleTimeoutMinutes) * time.Minute,
		ToolAllowlist:     buildReviewerToolList(brokerEnv),
		RunsDir:           runsDir,
		Env:               brokerEnv,
	}
}

// startReviewerBrokers starts CBM and GM brokers for a reviewer invocation.
// Returns cbmEnabled, the combined broker environment, the GM broker (may be nil),
// and a cleanup function.
func (r *Runner) startReviewerBrokers(golemicDir, runsDir, worktreePath string, prNumber int, precheck *reviewerPrecheckResult) (cbmEnabled bool, brokerEnv []string, gmBroker *gmbroker.Broker, cleanup func()) {
	var cleanups []func()
	var cbmCfg gmbroker.CBMConfig
	if r.cfg.CodebaseMemory.Enabled {
		cbmCacheDir := filepath.Join(golemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		projectName := fmt.Sprintf("golemic-issue-%d-reviewer", r.issueNum)
		sockPath := filepath.Join(runsDir, r.runID, "cbm-reviewer.sock")
		if b, env, ok := r.startCBMForRole(worktreePath, cbmCacheDir, sockPath, projectName); ok {
			cleanups = append(cleanups, b.Shutdown)
			brokerEnv = env
			cbmEnabled = true
			cbmCfg = gmbroker.CBMConfig{SockPath: sockPath, Project: projectName}
		}
	}
	gmSockPath := filepath.Join(runsDir, r.runID, "gm-reviewer.sock")
	if gmb, gmEnv, ok := r.startGMForRole(gmSockPath, "reviewer", worktreePath); ok {
		var precheckState *gmbroker.PrecheckState
		if precheck != nil {
			precheckState = &gmbroker.PrecheckState{
				OK:                precheck.OK,
				BeforeFingerprint: precheck.BeforeFingerprint,
				AfterFingerprint:  precheck.AfterFingerprint,
			}
		}
		gmb.SetReviewerConfig(gmbroker.ReviewerConfig{
			WorktreePath:  worktreePath,
			ReviewerToken: r.creds.ReviewerToken(),
			RepoRoot:      r.repoRoot,
			PRNumber:      prNumber,
			Precheck:      precheckState,
		})
		if cbmCfg.SockPath != "" {
			gmb.ConfigureCBM(cbmCfg)
			gmb.SetAllowedTools(append(gmReviewerToolNames, gmCodeToolNames...))
		}
		cleanups = append(cleanups, gmb.Shutdown)
		brokerEnv = append(brokerEnv, gmEnv...)
		gmBroker = gmb
	}
	cleanup = func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	return cbmEnabled, brokerEnv, gmBroker, cleanup
}

// ---------------------------------------------------------------------------
// Review submission and event writing (runner-side, \u00a712/\u00a714)
// ---------------------------------------------------------------------------

// graphqlSubmitPendingReview submits an existing pending review with verdict + body.
const graphqlSubmitPendingReview = `mutation($reviewId:ID!,$event:PullRequestReviewEvent!,$body:String!){submitPullRequestReview(input:{pullRequestReviewId:$reviewId,event:$event,body:$body}){pullRequestReview{fullDatabaseId comments{totalCount}}}}` //nolint:lll

// graphqlDiscoverPendingForSubmit queries viewer login + PR pending reviews, for discover-or-create.
const graphqlDiscoverPendingForSubmit = `query($owner:String!,$name:String!,$prNumber:Int!){viewer{login}repository(owner:$owner,name:$name){pullRequest(number:$prNumber){id reviews(first:10,states:[PENDING]){nodes{id author{login}}}}}}` //nolint:lll

// graphqlCreatePendingReviewForSubmit creates an empty pending review for submit.
const graphqlCreatePendingReviewForSubmit = `mutation($prId:ID!){addPullRequestReview(input:{pullRequestId:$prId}){pullRequestReview{id}}}` //nolint:lll

// submitReviewAndWriteEvent submits the pending review via GitHub and writes the
// review_submitted event. It is called after a valid gm_review_submit by the broker.
func (r *Runner) submitReviewAndWriteEvent(state *reviewerInvocationState, eventLogPath string) error {
	if state == nil || state.reviewSubmitParams == nil {
		return nil
	}
	params := state.reviewSubmitParams
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		return fmt.Errorf("submitReviewAndWriteEvent: %w", err)
	}

	pendingID := state.pendingReviewID
	if pendingID == "" {
		// No inline comments were posted; discover-or-create a pending review.
		pendingID, err = r.discoverOrCreateReviewerPendingReview(prNumber)
		if err != nil {
			return fmt.Errorf("submitReviewAndWriteEvent: %w", err)
		}
	}

	submittedID, inlineCount, err := r.submitPendingReview(pendingID, params.Verdict, params.Body)
	if err != nil {
		return fmt.Errorf("submitReviewAndWriteEvent: %w", err)
	}

	if err := r.writeReviewSubmittedEventFromRunner(eventLogPath, submittedID, params.Verdict, params.Body, params.MergeConfidence, inlineCount); err != nil {
		return fmt.Errorf("submitReviewAndWriteEvent: write event: %w", err)
	}

	// set merge-confidence label
	if err := r.setMergeConfidenceLabel(params.MergeConfidence, prNumber); err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to set merge confidence label: %v\n", err) //nolint:errcheck
	}

	return nil
}

// discoverOrCreateReviewerPendingReview finds or creates a pending review for the reviewer token.
func (r *Runner) discoverOrCreateReviewerPendingReview(prNumber int) (string, error) {
	nwo, err := r.repoNWO()
	if err != nil {
		return "", err
	}
	parts := strings.SplitN(nwo, "/", 2)
	owner, repoName := parts[0], parts[1]

	viewerLogin, prID, existingID, err := r.discoverReviewerPendingReview(owner, repoName, prNumber)
	if err != nil {
		return "", err
	}
	_ = viewerLogin
	if existingID != "" {
		return existingID, nil
	}
	return r.createReviewerPendingReview(prID)
}

func (r *Runner) discoverReviewerPendingReview(owner, repoName string, prNumber int) (viewerLogin, prID, existingID string, err error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", "graphql",
		"-f", "query="+graphqlDiscoverPendingForSubmit,
		"-f", "owner="+owner,
		"-f", "name="+repoName,
		"-F", fmt.Sprintf("prNumber=%d", prNumber),
	)
	if err != nil {
		return "", "", "", fmt.Errorf("discover pending review: %w", err)
	}
	var resp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Repository struct {
				PullRequest struct {
					ID      string `json:"id"`
					Reviews struct {
						Nodes []struct {
							ID     string `json:"id"`
							Author struct {
								Login string `json:"login"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", "", "", fmt.Errorf("parse discover response: %w", err)
	}
	vl := resp.Data.Viewer.Login
	pid := resp.Data.Repository.PullRequest.ID
	if pid == "" {
		return "", "", "", fmt.Errorf("PR #%d not found", prNumber)
	}
	for _, node := range resp.Data.Repository.PullRequest.Reviews.Nodes {
		if node.Author.Login == vl {
			return vl, pid, node.ID, nil
		}
	}
	return vl, pid, "", nil
}

func (r *Runner) createReviewerPendingReview(prID string) (string, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", "graphql",
		"-f", "query="+graphqlCreatePendingReviewForSubmit,
		"-f", "prId="+prID,
	)
	if err != nil {
		return "", fmt.Errorf("create pending review: %w", err)
	}
	var resp struct {
		Data struct {
			AddPullRequestReview struct {
				PullRequestReview struct {
					ID string `json:"id"`
				} `json:"pullRequestReview"`
			} `json:"addPullRequestReview"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("parse create review response: %w", err)
	}
	newID := resp.Data.AddPullRequestReview.PullRequestReview.ID
	if newID == "" {
		return "", fmt.Errorf("create pending review returned empty id")
	}
	return newID, nil
}

// submitPendingReview submits the pending review and returns the submitted review ID
// and inline comment count.
func (r *Runner) submitPendingReview(reviewID, verdict, body string) (submittedReviewID string, inlineCount int, err error) {
	ghEvent := "APPROVE"
	if verdict == "changes_requested" {
		ghEvent = "REQUEST_CHANGES"
	}

	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", "graphql",
		"-f", "query="+graphqlSubmitPendingReview,
		"-f", "reviewId="+reviewID,
		"-f", "event="+ghEvent,
		"-f", "body="+body,
	)
	if err != nil {
		return "", 0, fmt.Errorf("submit review: %w", err)
	}

	var resp struct {
		Data struct {
			SubmitPullRequestReview struct {
				PullRequestReview struct {
					FullDatabaseID int64 `json:"fullDatabaseId"`
					Comments       struct {
						TotalCount int `json:"totalCount"`
					} `json:"comments"`
				} `json:"pullRequestReview"`
			} `json:"submitPullRequestReview"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", 0, fmt.Errorf("parse submit review response: %w", err)
	}
	dbID := resp.Data.SubmitPullRequestReview.PullRequestReview.FullDatabaseID
	count := resp.Data.SubmitPullRequestReview.PullRequestReview.Comments.TotalCount
	if dbID == 0 {
		return "", 0, fmt.Errorf("submit review returned empty id")
	}
	return fmt.Sprintf("%d", dbID), count, nil
}

// writeReviewSubmittedEventFromRunner writes a review_submitted event (BR-10).
func (r *Runner) writeReviewSubmittedEventFromRunner(eventLogPath, reviewID, verdict, body, mergeConfidence string, inlineCommentCount int) error {
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		return err
	}
	defer w.Close() //nolint:errcheck

	payload, err := json.Marshal(map[string]any{
		"reviewId":           reviewID,
		"verdict":            verdict,
		"body":               body,
		"mergeConfidence":    mergeConfidence,
		"inlineCommentCount": inlineCommentCount,
	})
	if err != nil {
		return err
	}
	return w.Write(eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  r.turnCounter,
		Payload: payload,
	})
}

// setMergeConfidenceLabel sets the merge-confidence label on the PR.
// Non-fatal: caller logs a warning on failure.
func (r *Runner) setMergeConfidenceLabel(mergeConfidence string, prNumber int) error {
	labelName := "merge-confidence:" + mergeConfidence
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "pr", "edit", fmt.Sprintf("%d", prNumber),
		"--add-label", labelName,
	)
	return err
}
