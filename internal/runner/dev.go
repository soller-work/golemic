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
)

// runDevAgent runs the dev agent and returns the outcome.
func (r *Runner) runDevAgent(golemicDir, eventLogPath string, timeout time.Duration) string {
	golemicBinaryPath, _ := os.Executable()
	binaryDir := filepath.Dir(golemicBinaryPath)
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	// Render dev prompt
	userPrompt, err := prompt.RenderDev(
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
		fmt.Fprintf(r.stderr, "Failed to render dev prompt: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}

	// Run dev agent
	_, _, err = agent.RunRole(context.Background(), agent.RoleConfig{
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
			fmt.Fprintf(r.stderr, "dev_failed: dev agent exceeded timeout\n") //nolint:errcheck
			return outcomeTimeout
		}
		fmt.Fprintf(r.stderr, "dev_failed: agent failed: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}

	return outcomeSuccess
}
