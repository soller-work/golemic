package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/prompt"
)

// runReviewerAgent runs the reviewer agent and returns the outcome.
func (r *Runner) runReviewerAgent(golemicDir, eventLogPath string, timeout time.Duration) string {
	golemicBinaryPath, _ := os.Executable()
	binaryDir := filepath.Dir(golemicBinaryPath)
	reviewerWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

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
			Body:   r.issue.Body,
		},
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, ".golemic", "guidelines", "reviewer.md"),
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render reviewer prompt: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed
	}

	// Run reviewer agent
	runFn := r.runAgentFn
	if runFn == nil {
		runFn = agent.RunRole
	}
	exitCode, paths, err := runFn(context.Background(), agent.RoleConfig{
		Role:              "reviewer",
		SystemPromptFile:  filepath.Join(binaryDir, "prompts", "reviewer.md"),
		UserPrompt:        userPrompt,
		WorktreeDir:       reviewerWorktreePath,
		RunID:             r.runID,
		EventLogPath:      eventLogPath,
		GHToken:           r.creds.ReviewerToken(),
		GolemicBinaryPath: golemicBinaryPath,
		Model:             r.cfg.Models.Reviewer,
		Timeout:           timeout,
		ToolAllowlist:     []string{"read", "bash"},
		RunsDir:           runsDir,
	})

	if err != nil {
		if errors.Is(err, agent.ErrTimeout) {
			fmt.Fprintf(r.stderr, "review_failed: reviewer agent exceeded timeout\n") //nolint:errcheck
			return outcomeTimeout
		}
		fmt.Fprintf(r.stderr, "review_failed: agent failed: %v\n", err) //nolint:errcheck
		return outcomeReviewFailed
	}

	// Record agent exit code in event log (BR-004)
	r.writeAgentCompleted(eventLogPath, "reviewer", exitCode)

	// Fail on non-zero exit (BR-001, BR-002)
	if exitCode != 0 {
		fmt.Fprintf(r.stderr, "review_failed: reviewer agent exited with code %d; see %s\n", exitCode, paths.Stderr) //nolint:errcheck
		return outcomeReviewFailed
	}

	return outcomeSuccess
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
