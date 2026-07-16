// Package runner orchestrates a golemic run: host-repo resolution, config/credentials
// loading, runId generation, event log creation, issue loading via gh, collision checks,
// dev/reviewer worktrees, agent execution, event reading, outcome determination,
// run_finished writing, and cleanup.
//
// Process steps (PS-001–PS-006 per spec):
//   1. Resolve host repo (git root; if under tools/golemic, find enclosing repo)
//   2. Load config and credentials (fail-closed before any GitHub access)
//   3. Generate runId, create event log, write run_started
//   4. Load issue from GitHub via gh issue view
//   5. Collision check (worktree, local/remote branch, open PR)
//   6. Full orchestration: dev worktree → dev agent → pr_opened → reviewer worktree → reviewer agent → dirty check → review_submitted → outcome determination → run_finished → cleanup
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/agent"
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/prompt"
	"golemic/internal/worktree"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	outcomeSuccess      = "success"
	outcomeDevFailed    = "dev_failed"
	outcomeReviewFailed = "review_failed"
	outcomeTimeout      = "timeout"
	outcomeAborted      = "aborted"
	branchPrefix        = "golemic/issue-"
)

// ---------------------------------------------------------------------------
// Payload types
// ---------------------------------------------------------------------------

// runStartedPayload is the payload for run_started events.
type runStartedPayload struct {
	Issue int    `json:"issue"`
	RunID string `json:"runId"`
}

// runFinishedPayload is the payload for run_finished events.
type runFinishedPayload struct {
	Outcome string `json:"outcome"`
}

// issueData holds the parsed result of gh issue view --json title,body.
type issueData struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// ---------------------------------------------------------------------------
// Collision types
// ---------------------------------------------------------------------------

// Collision describes one concrete collision found during checkAllCollisions.
type Collision struct {
	Message string // human-readable error with cleanup commands
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Runner orchestrates a golemic run.
type Runner struct {
	executor preflight.Executor
	homeDir  string
	cwd      string
	stdout   io.Writer
	stderr   io.Writer
	issueNum int

	// Resolved during Run
	repoRoot   string
	project    string
	runID      string
	branchName string
	cfg        *config.Config
	creds      *credentials.Credentials
	issue      *issueData
}

// New creates a new Runner. executor is used for all gh/git commands, homeDir is
// the user's home directory (~/.golemic is resolved relative to it), cwd is the
// current working directory, issueNum is the GitHub issue number.
func New(executor preflight.Executor, homeDir, cwd string, issueNum int) *Runner {
	return &Runner{
		executor: executor,
		homeDir:  homeDir,
		cwd:      cwd,
		stdout:   io.Discard,
		stderr:   io.Discard,
		issueNum: issueNum,
	}
}

// SetStdout sets the writer for normal output (e.g. runId on success).
func (r *Runner) SetStdout(w io.Writer) { r.stdout = w }

// SetStderr sets the writer for error output.
func (r *Runner) SetStderr(w io.Writer) { r.stderr = w }

// ---------------------------------------------------------------------------
// Host repo resolution
// ---------------------------------------------------------------------------

// resolveHostRepo determines the host repository root.
//
// BR-001: Host repo is determined by walking up from cwd; if cwd contains
// "tools/golemic" in its path, resolve to the enclosing git root.
func resolveHostRepo(exec preflight.Executor, cwd string) (string, error) {
	gitRoot, err := exec.Run("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}
	gitRoot = strings.TrimSpace(gitRoot)
	if gitRoot == "" {
		return "", fmt.Errorf("not in a git repository")
	}

	// BR-001: If we are inside tools/golemic (golemic as submodule or dropped dir),
	// resolve the enclosing repo as the host repo.
	if strings.Contains(cwd, "/tools/golemic") || strings.HasSuffix(gitRoot, "/tools/golemic") {
		parent := filepath.Dir(gitRoot)
		out, err := exec.Run("git", "-C", parent, "rev-parse", "--show-toplevel")
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
		// No enclosing git repo; fall through and return gitRoot as-is.
	}

	return gitRoot, nil
}

// ---------------------------------------------------------------------------
// Issue loading
// ---------------------------------------------------------------------------

// loadIssue fetches issue details from GitHub via gh issue view with the dev token.
func (r *Runner) loadIssue() (*issueData, error) {
	out, err := r.executor.RunWithEnv(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		"gh", "issue", "view", fmt.Sprintf("%d", r.issueNum), "--json", "title,body",
	)
	if err != nil {
		return nil, fmt.Errorf("gh issue view: %w", err)
	}

	var data struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, fmt.Errorf("invalid gh response: %w", err)
	}

	return &issueData{
		Number: r.issueNum,
		Title:  data.Title,
		Body:   data.Body,
	}, nil
}

// ---------------------------------------------------------------------------
// Collision check
// ---------------------------------------------------------------------------

// worktreeDir returns the expected path for the issue worktree.
func (r *Runner) worktreeDir() string {
	return filepath.Join(r.homeDir, ".golemic", r.project, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
}

// checkWorktreeCollision checks BR-004: worktree exists → abort.
func (r *Runner) checkWorktreeCollision() *Collision {
	wtDir := r.worktreeDir()
	if _, err := os.Stat(wtDir); err == nil {
		return &Collision{
			Message: fmt.Sprintf("Worktree exists at %s; remove with: git worktree remove %s", wtDir, wtDir),
		}
	}
	return nil
}

// checkBranchCollision checks BR-005: local or remote branch exists → abort.
// Returns error on git command failure (fail-closed per IC-002).
func (r *Runner) checkBranchCollision() (*Collision, error) {
	// Local branch check
	localOut, err := r.executor.Run("git", "branch", "--list", r.branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check git state: %w", err)
	}
	if strings.TrimSpace(localOut) != "" {
		return &Collision{
			Message: fmt.Sprintf("Branch %s exists locally; remove with: git branch -D %s", r.branchName, r.branchName),
		}, nil
	}

	// Remote branch check
	remoteOut, err := r.executor.Run("git", "ls-remote", "--heads", "origin", r.branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to check git state: %w", err)
	}
	if strings.TrimSpace(remoteOut) != "" {
		return &Collision{
			Message: fmt.Sprintf("Branch %s exists on origin; remove with: git push origin --delete %s", r.branchName, r.branchName),
		}, nil
	}

	return nil, nil
}

// checkPRCollision checks BR-006: open PR with head branch exists → abort.
// Returns error on gh command or parse failure (fail-closed).
func (r *Runner) checkPRCollision() (*Collision, error) {
	out, err := r.executor.RunWithEnv(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		"gh", "pr", "list", "--head", r.branchName, "--json", "url,state",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to check PR state: %w", err)
	}

	var prs []struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("failed to check PR state: %w", err)
	}

	for _, pr := range prs {
		if pr.State == "OPEN" {
			return &Collision{
				Message: fmt.Sprintf("Open PR %s exists with head branch %s; close it first", pr.URL, r.branchName),
			}, nil
		}
	}
	return nil, nil
}

// checkAllCollisions runs all three collision checks in order and returns the first found.
// Order: worktree, local branch, remote branch, open PR (per DT-001).
// Returns error if any check fails (fail-closed).
func (r *Runner) checkAllCollisions() (*Collision, error) {
	// BR-004
	if c := r.checkWorktreeCollision(); c != nil {
		return c, nil
	}
	// BR-005
	c, err := r.checkBranchCollision()
	if err != nil {
		return nil, err
	}
	if c != nil {
		return c, nil
	}
	// BR-006
	c, err = r.checkPRCollision()
	if err != nil {
		return nil, err
	}
	if c != nil {
		return c, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Main Run method
// ---------------------------------------------------------------------------

// Run executes the full run process and returns the process exit code.
//
// Process flow (per spec §Process Steps):
//
//	PS-001: Resolve host repo
//	PS-002: Load config and credentials (fail-closed)
//	PS-003: Generate runId, create event log, write run_started
//	PS-004: Load issue from GitHub
//	PS-005: Collision check
//	PS-006: Full orchestration
func (r *Runner) Run() int {
	// ---- PS-001: Resolve host repo ----
	repoRoot, err := resolveHostRepo(r.executor, r.cwd)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to resolve host repo: %v\n", err)
		return 1
	}
	r.repoRoot = repoRoot
	r.project = filepath.Base(repoRoot)

	// ---- PS-002: Load config and credentials (BR-002: fail-closed) ----
	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to load config: %v\n", err)
		return 1
	}
	r.cfg = cfg
	r.project = cfg.Project

	loader := credentials.NewLoader(r.homeDir)
	creds, err := loader.Load(r.project)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to load credentials: %v\n", err)
		return 1
	}
	r.creds = creds

	// ---- PS-003: Generate runId and create event log (BR-003, BR-007) ----
	r.runID = fmt.Sprintf("issue-%d-%s", r.issueNum, time.Now().UTC().Format("20060102T150405Z"))
	r.branchName = fmt.Sprintf("%s%d", branchPrefix, r.issueNum)

	eventLogPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")

	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to create event log: %v\n", err)
		return 1
	}
	defer writer.Close()

	// Write run_started (BR-007: must be written before any GitHub access)
	startPayload, _ := json.Marshal(runStartedPayload{
		Issue: r.issueNum,
		RunID: r.runID,
	})
	if err := writer.Write(eventlog.Event{
		Type:    eventlog.EventRunStarted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: startPayload,
	}); err != nil {
		fmt.Fprintf(r.stderr, "Failed to write run_started event: %v\n", err)
		return 1
	}

	// ---- PS-004: Load issue from GitHub ----
	issue, err := r.loadIssue()
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to load issue %d: %v\n", r.issueNum, err)
		return 1
	}
	r.issue = issue

	// ---- PS-005: Collision check ----
	collision, err := r.checkAllCollisions()
	if err != nil {
		fmt.Fprintln(r.stderr, err.Error())
		// Write run_finished with outcome aborted
		finishedPayload, _ := json.Marshal(runFinishedPayload{Outcome: outcomeAborted})
		_ = writer.Write(eventlog.Event{
			Type:    eventlog.EventRunFinished,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   r.runID,
			Payload: finishedPayload,
		})
		fmt.Fprintf(r.stdout, "runs/%s\n", r.runID)
		return 1
	}
	if collision != nil {
		fmt.Fprintln(r.stderr, collision.Message)
		// Write run_finished with outcome aborted
		finishedPayload, _ := json.Marshal(runFinishedPayload{Outcome: outcomeAborted})
		_ = writer.Write(eventlog.Event{
			Type:    eventlog.EventRunFinished,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   r.runID,
			Payload: finishedPayload,
		})
		fmt.Fprintf(r.stdout, "runs/%s\n", r.runID)
		return 1
	}

	// ---- PS-006: Full orchestration ----
	finalOutcome := r.orchestrate(writer, eventLogPath)

	// Write run_finished with final outcome (BR-001: always the last event)
	finishedPayload, _ := json.Marshal(runFinishedPayload{Outcome: finalOutcome})
	_ = writer.Write(eventlog.Event{
		Type:    eventlog.EventRunFinished,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: finishedPayload,
	})

	// BR-006: Cleanup (worktree removal) only on success
	if finalOutcome == outcomeSuccess {
		golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
		if err := worktree.Cleanup(r.repoRoot, golemicDir, r.issueNum, r.executor); err != nil {
			fmt.Fprintf(r.stderr, "Warning: dev worktree cleanup failed: %v\n", err)
		}
		if err := worktree.CleanupReviewer(r.repoRoot, golemicDir, r.issueNum, r.executor); err != nil {
			fmt.Fprintf(r.stderr, "Warning: reviewer worktree cleanup failed: %v\n", err)
		}
	}

	// BR-007: Exit 0 only for success; exit != 0 otherwise
	if finalOutcome == outcomeSuccess {
		fmt.Fprintln(r.stdout, r.runID)
		return 0
	}

	fmt.Fprintf(r.stdout, "runs/%s\n", r.runID)
	return 1
}

// orchestrate implements the full dev→reviewer loop after collision check passes.
// Returns final outcome.
func (r *Runner) orchestrate(writer worktree.EventWriter, eventLogPath string) string {
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	timeoutDuration := time.Duration(r.cfg.TimeoutMinutes) * time.Minute

	// PS-002: Create dev worktree
	if err := worktree.Create(r.repoRoot, golemicDir, r.runID, r.issueNum, "golemic-dev", r.executor, writer); err != nil {
		fmt.Fprintf(r.stderr, "Failed to create dev worktree: %v\n", err)
		return outcomeDevFailed
	}

	// PS-003: Run dev agent
	devOutcome := r.runDevAgent(golemicDir, eventLogPath, timeoutDuration)
	if devOutcome != outcomeSuccess {
		return devOutcome
	}

	// Check if pr_opened event exists and is valid
	if !r.hasPROpenedEvent(eventLogPath) {
		fmt.Fprintf(r.stderr, "dev_failed: pr_opened event missing or invalid\n")
		return outcomeDevFailed
	}

	// PS-004: Create reviewer worktree
	if err := worktree.CreateForReviewer(r.repoRoot, golemicDir, r.runID, r.issueNum, r.branchName, "golemic-reviewer", r.executor, writer); err != nil {
		fmt.Fprintf(r.stderr, "Failed to create reviewer worktree: %v\n", err)
		return outcomeReviewFailed
	}

	// PS-005: Run reviewer agent
	reviewerOutcome := r.runReviewerAgent(golemicDir, eventLogPath, timeoutDuration)
	if reviewerOutcome != outcomeSuccess {
		return reviewerOutcome
	}

	// Dirty check
	reviewerWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	isDirty, err := worktree.IsDirty(reviewerWorktreePath, r.executor)
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: failed to check dirty status: %v\n", err)
		return outcomeReviewFailed
	}
	if isDirty {
		fmt.Fprintf(r.stderr, "review_failed: reviewer worktree has uncommitted changes\n")
		return outcomeReviewFailed
	}

	// Check if review_submitted event exists and is valid
	if !r.hasReviewSubmittedEvent(eventLogPath) {
		fmt.Fprintf(r.stderr, "review_failed: review_submitted event missing or invalid\n")
		return outcomeReviewFailed
	}

	return outcomeSuccess
}

// runDevAgent runs the dev agent and returns the outcome.
func (r *Runner) runDevAgent(golemicDir, eventLogPath string, timeout time.Duration) string {
	golemicBinaryPath, _ := os.Executable()
	devWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	// Render dev prompt
	systemPromptFile, userPrompt, err := prompt.RenderDev(
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
			Body:   r.issue.Body,
		},
		r.branchName,
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, "guidelines.md"),
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render dev prompt: %v\n", err)
		return outcomeDevFailed
	}

	// Run dev agent
	_, _, err = agent.RunRole(context.Background(), agent.RoleConfig{
		Role:              "dev",
		SystemPromptFile:  filepath.Join(r.repoRoot, systemPromptFile),
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
			fmt.Fprintf(r.stderr, "dev_failed: dev agent exceeded timeout\n")
			return outcomeTimeout
		}
		fmt.Fprintf(r.stderr, "dev_failed: agent failed: %v\n", err)
		return outcomeDevFailed
	}

	return outcomeSuccess
}

// runReviewerAgent runs the reviewer agent and returns the outcome.
func (r *Runner) runReviewerAgent(golemicDir, eventLogPath string, timeout time.Duration) string {
	golemicBinaryPath, _ := os.Executable()
	reviewerWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")

	// Get PR number from pr_opened event
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to get PR number: %v\n", err)
		return outcomeReviewFailed
	}

	// Render reviewer prompt
	systemPromptFile, userPrompt, err := prompt.RenderReviewer(
		prNumber,
		prompt.Issue{
			Number: r.issue.Number,
			Title:  r.issue.Title,
			Body:   r.issue.Body,
		},
		r.cfg.VerifyCommand,
		filepath.Join(r.repoRoot, "guidelines.md"),
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to render reviewer prompt: %v\n", err)
		return outcomeReviewFailed
	}

	// Run reviewer agent
	_, _, err = agent.RunRole(context.Background(), agent.RoleConfig{
		Role:              "reviewer",
		SystemPromptFile:  filepath.Join(r.repoRoot, systemPromptFile),
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
			fmt.Fprintf(r.stderr, "review_failed: reviewer agent exceeded timeout\n")
			return outcomeTimeout
		}
		fmt.Fprintf(r.stderr, "review_failed: agent failed: %v\n", err)
		return outcomeReviewFailed
	}

	return outcomeSuccess
}

// hasPROpenedEvent checks if a valid pr_opened event exists in the log.
func (r *Runner) hasPROpenedEvent(eventLogPath string) bool {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return false
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventPROpened {
			if err := eventlog.ValidatePROpenedPayload(events[i].Payload); err != nil {
				return false
			}
			return true
		}
	}
	return false
}

// hasReviewSubmittedEvent checks if a valid review_submitted event exists in the log.
func (r *Runner) hasReviewSubmittedEvent(eventLogPath string) bool {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return false
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			if err := eventlog.ValidateReviewSubmittedPayload(events[i].Payload); err != nil {
				return false
			}
			return true
		}
	}
	return false
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
