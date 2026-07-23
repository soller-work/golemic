package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/prompt"
	"golemic/internal/telemetry"
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

// runReviewerAgent runs the reviewer agent and returns the outcome.
func (r *Runner) runReviewerAgent(golemicDir, eventLogPath string, timeout time.Duration, parentSpanID string, round int) string {
	golemicBinaryPath, _ := os.Executable()
	reviewerWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	systemPromptFile, model, cleanupPrompt, err := r.resolveAgentFile("reviewer")
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed
	}
	defer cleanupPrompt()

	cbmEnabled := false
	var brokerEnv []string
	if r.cfg.CodebaseMemory.Enabled {
		cbmCacheDir := filepath.Join(golemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		projectName := fmt.Sprintf("golemic-issue-%d-reviewer", r.issueNum)
		sockPath := filepath.Join(runsDir, r.runID, "cbm-reviewer.sock")
		if b, env, ok := r.startCBMForRole(reviewerWorktreePath, cbmCacheDir, sockPath, projectName); ok {
			defer b.Shutdown()
			brokerEnv = env
			cbmEnabled = true
		}
	}

	// Get PR number from pr_opened event
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to get PR number: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed
	}

	// Render reviewer prompt
	userPrompt, err := prompt.RenderReviewer(
		prNumber,
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
		},
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, ".golemic", "guidelines", "reviewer.md"),
		cbmEnabled,
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render reviewer prompt: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed
	}

	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanAgentTurn,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "role": "reviewer", "round": round, "model": model})

	activityPath := filepath.Join(runsDir, r.runID, "reviewer.activity.jsonl")
	stopFollow := followActivity(r.progressRenderer, "reviewer", activityPath)

	// Run reviewer agent
	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}
	exitCode, paths, err := runFn(context.Background(), agent.RoleConfig{
		Role:              "reviewer",
		SystemPromptFile:  systemPromptFile,
		UserPrompt:        userPrompt,
		WorktreeDir:       reviewerWorktreePath,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		TurnID:            r.turnCounter,
		GHToken:           r.creds.ReviewerToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             model,
		Timeout:           timeout,
		ToolAllowlist:     []string{"read", "bash", "write", "edit"},
		RunsDir:           runsDir,
		Env:               r.credEnv(brokerEnv),
	})
	stopFollow()

	if err != nil {
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

	// Record agent exit code in event log (BR-004)
	r.writeAgentCompleted(eventLogPath, "reviewer", exitCode)
	r.emitAgentWrittenEvents(eventLogPath)

	// Fail on non-zero exit (BR-001, BR-002)
	if exitCode != 0 {
		endSpan(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "review_failed: reviewer agent exited with code %d; see %s\n", exitCode, paths.Stderr) //nolint:errcheck
		return outcomeReviewFailed
	}

	endSpan(telemetry.StatusOK, nil)
	return outcomeSuccess
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
