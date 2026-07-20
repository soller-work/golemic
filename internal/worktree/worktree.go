// Package worktree provides isolated working directories for golemic roles.
//
// The Create function sets up a git worktree from origin/main with correct
// authentication (env-based credential helper) and bot identity
// (user.name/user.email). The Cleanup function removes the worktree and its
// local branch — called only on success outcome (not on errors).
//
// All git commands go through the injectable preflight.Executor interface.
package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// EventWriter is the subset of eventlog.Writer needed by this package.
// Defined as an interface so tests can use mocks without writing to disk.
// Real production code passes *eventlog.Writer which satisfies this interface.
type EventWriter interface {
	Write(event eventlog.Event) error
}

// worktreePath returns the absolute path for an issue worktree.
func worktreePath(golemicDir string, issueNumber int) string {
	return filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d", issueNumber))
}

// branchName returns the git branch name for an issue worktree.
func branchName(issueNumber int) string {
	return fmt.Sprintf("golemic/issue-%d", issueNumber)
}

// configureWorktreeGit sets credential.helper, user.name, and user.email in the
// worktree at wtPath via git -C <wtPath> config.
func configureWorktreeGit(executor preflight.Executor, wtPath, login string) error {
	credHelper := "!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f"
	if _, err := executor.Run("git", "-C", wtPath, "config", "credential.helper", credHelper); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: credential.helper: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.name", login); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.name: %w", err)
	}
	if _, err := executor.Run("git", "-C", wtPath, "config", "user.email", login); err != nil {
		return fmt.Errorf("GIT_CONFIG_FAILED: user.email: %w", err)
	}
	return nil
}

// Create sets up a dev worktree for the given issue.
//
// Steps:
//  1. Validate issueNumber > 0
//  2. git -C <repoRoot> fetch origin
//  3. git -C <repoRoot> rev-parse origin/main → baseSha
//  4. git -C <repoRoot> worktree add <path> -b golemic/issue-<N> origin/main
//  5. git config credential.helper, user.name, user.email in the worktree
//  6. write worktree_created event with role: dev
//
// If any step fails, the partial worktree is left in place for debugging
// (no cleanup is called — see BR-005).
func Create(repoRoot, golemicDir, runID string, issueNumber int, botLogin string, executor preflight.Executor, eventWriter EventWriter, turnID int) error {
	if issueNumber <= 0 {
		return fmt.Errorf("INVALID_ISSUE_NUMBER: %d", issueNumber)
	}
	// 2. git -C <repoRoot> fetch origin
	if _, err := executor.Run("git", "-C", repoRoot, "fetch", "origin"); err != nil {
		return fmt.Errorf("GIT_FETCH_FAILED: %w", err)
	}

	// 3. Capture baseSha from origin/main
	baseShaOut, err := executor.Run("git", "-C", repoRoot, "rev-parse", "origin/main")
	if err != nil {
		return fmt.Errorf("GIT_REV_PARSE_FAILED: %w", err)
	}
	baseSha := strings.TrimSpace(baseShaOut)

	// 4. git worktree add
	wtPath := worktreePath(golemicDir, issueNumber)
	branch := branchName(issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "add", wtPath, "-b", branch, "origin/main"); err != nil {
		return fmt.Errorf("GIT_WORKTREE_ADD_FAILED: %w", err)
	}

	// 5. Set worktree-local git config (env-based credential helper, bot identity)
	if err := configureWorktreeGit(executor, wtPath, botLogin); err != nil {
		return err
	}

	// 6. Write worktree_created event
	payload := map[string]string{
		"path":    wtPath,
		"branch":  branch,
		"baseSha": baseSha,
		"role":    "dev",
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("EVENT_MARSHAL_FAILED: %w", err)
	}

	event := eventlog.Event{
		Type:    eventlog.EventWorktreeCreated,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: rawPayload,
	}
	if err := eventWriter.Write(event); err != nil {
		return fmt.Errorf("EVENT_WRITE_FAILED: %w", err)
	}

	return nil
}

// CreateForReviewer sets up a reviewer worktree for the given issue from the remote PR branch.
//
// Steps:
//  1. Validate issueNumber > 0
//  2. git -C <repoRoot> fetch origin
//  3. Verify remote branch exists via git rev-parse --verify origin/<branchName>
//  4. git -C <repoRoot> rev-parse origin/<branchName> → baseSha
//  5. git -C <repoRoot> worktree add <path> origin/<branchName> (detached HEAD)
//  6. git config credential.helper, user.name, user.email in the worktree
//  7. write worktree_created event with role: reviewer
//
// Returns REMOTE_BRANCH_NOT_FOUND if origin/<branchName> doesn't exist.
// If any other step fails, the partial worktree is left in place for debugging.
func CreateForReviewer(repoRoot, golemicDir, runID string, issueNumber int, branchName, reviewerBotLogin string, executor preflight.Executor, eventWriter EventWriter, turnID int) error {
	if issueNumber <= 0 {
		return fmt.Errorf("INVALID_ISSUE_NUMBER: %d", issueNumber)
	}
	// 2. git -C <repoRoot> fetch origin
	if _, err := executor.Run("git", "-C", repoRoot, "fetch", "origin"); err != nil {
		return fmt.Errorf("GIT_FETCH_FAILED: %w", err)
	}

	// 3. Verify the remote branch exists (for clear error messaging).
	remoteBranch := "origin/" + branchName
	if _, err := executor.Run("git", "-C", repoRoot, "rev-parse", "--verify", remoteBranch); err != nil {
		return fmt.Errorf("REMOTE_BRANCH_NOT_FOUND: Remote branch %s not found; was the branch pushed?", remoteBranch)
	}

	// 4. Capture baseSha from remote branch
	baseShaOut, err := executor.Run("git", "-C", repoRoot, "rev-parse", remoteBranch)
	if err != nil {
		return fmt.Errorf("GIT_REV_PARSE_FAILED: %w", err)
	}
	baseSha := strings.TrimSpace(baseShaOut)

	// 5. git worktree add with detached HEAD
	wtPath := reviewerWorktreePath(golemicDir, issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "add", wtPath, remoteBranch); err != nil {
		return fmt.Errorf("GIT_WORKTREE_ADD_FAILED: %w", err)
	}

	// 6. Set worktree-local git config (env-based credential helper, reviewer bot identity)
	if err := configureWorktreeGit(executor, wtPath, reviewerBotLogin); err != nil {
		return err
	}

	// 7. Write worktree_created event
	payload := map[string]string{
		"path":    wtPath,
		"branch":  branchName,
		"baseSha": baseSha,
		"role":    "reviewer",
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("EVENT_MARSHAL_FAILED: %w", err)
	}

	event := eventlog.Event{
		Type:    eventlog.EventWorktreeCreated,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: rawPayload,
	}
	if err := eventWriter.Write(event); err != nil {
		return fmt.Errorf("EVENT_WRITE_FAILED: %w", err)
	}

	return nil
}

// IsDirty checks if the worktree at worktreePath has uncommitted changes
// by running git status --porcelain. Returns true if the output is non-empty
// (dirty), false if empty (clean).
func IsDirty(worktreePath string, executor preflight.Executor) (bool, error) {
	output, err := executor.Run("git", "-C", worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("GIT_STATUS_FAILED: %w", err)
	}

	// Trim whitespace and check if output is empty
	if strings.TrimSpace(output) == "" {
		return false, nil
	}
	return true, nil
}

// reviewerWorktreePath returns the absolute path for a reviewer worktree.
func reviewerWorktreePath(golemicDir string, issueNumber int) string {
	return filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", issueNumber))
}

// mcpServerEntry is the MCP server config entry for codebase-memory.
type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// mcpConfig is the top-level .mcp.json structure.
type mcpConfig struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

// WriteMCPFiles writes .mcp.json into the worktree and adds it to .git/info/exclude.
// The CBM_CACHE_DIR in the .mcp.json matches the one passed to the indexer so that
// both the indexer and the agent reader share the same on-disk database.
func WriteMCPFiles(wtPath, cbmCacheDir string) error {
	cfg := mcpConfig{
		MCPServers: map[string]mcpServerEntry{
			"codebase-memory": {
				Command: "npx",
				Args:    []string{"-y", "codebase-memory-mcp@0.9.0"},
				Env: map[string]string{
					"CBM_CACHE_DIR":    cbmCacheDir,
					"CBM_LOG_LEVEL":    "warn",
					"CBM_ALLOWED_ROOT": wtPath,
				},
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal .mcp.json: %w", err)
	}
	mcpPath := filepath.Join(wtPath, ".mcp.json")
	if err := os.WriteFile(mcpPath, data, 0644); err != nil {
		return fmt.Errorf("write .mcp.json: %w", err)
	}

	gitDir, err := worktreeGitDir(wtPath)
	if err != nil {
		return fmt.Errorf("find git dir: %w", err)
	}
	return appendExcludePattern(filepath.Join(gitDir, "info", "exclude"), ".mcp.json")
}

// worktreeGitDir returns the path to the .git directory for a worktree.
// In a git worktree, .git is a file containing "gitdir: <path>".
func worktreeGitDir(wtPath string) (string, error) {
	dotGit := filepath.Join(wtPath, ".git")
	fi, err := os.Stat(dotGit)
	if err != nil {
		return "", fmt.Errorf("stat .git: %w", err)
	}
	if fi.IsDir() {
		return dotGit, nil
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return "", fmt.Errorf("read .git file: %w", err)
	}
	content := strings.TrimSpace(string(raw))
	const prefix = "gitdir: "
	if !strings.HasPrefix(content, prefix) {
		return "", fmt.Errorf(".git file has unexpected content: %q", content)
	}
	gitDir := strings.TrimPrefix(content, prefix)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wtPath, gitDir)
	}
	return gitDir, nil
}

// appendExcludePattern appends pattern to the git exclude file if not already present.
func appendExcludePattern(excludePath, pattern string) error {
	data, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read exclude file: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open exclude file: %w", err)
	}
	defer f.Close() //nolint:errcheck
	_, err = fmt.Fprintf(f, "\n%s\n", pattern)
	return err
}

// Cleanup removes the worktree and its local branch for the given issue.
//
// This is called only on success outcome (BR-004 / §2.11). On errors the
// partial worktree is left in place for debugging (BR-005).
func Cleanup(repoRoot, golemicDir string, issueNumber int, executor preflight.Executor) error {
	wtPath := worktreePath(golemicDir, issueNumber)
	branch := branchName(issueNumber)

	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "remove", wtPath); err != nil {
		return fmt.Errorf("CLEANUP_REMOVE_FAILED: %w", err)
	}

	if _, err := executor.Run("git", "-C", repoRoot, "branch", "-D", branch); err != nil {
		return fmt.Errorf("CLEANUP_BRANCH_FAILED: %w", err)
	}

	return nil
}

// CleanupReviewer removes the reviewer worktree for the given issue.
// Unlike Cleanup, this does not delete a git branch (reviewer worktree uses detached HEAD).
// Called only on success outcome. On errors, the partial worktree is left in place for debugging.
func CleanupReviewer(repoRoot, golemicDir string, issueNumber int, executor preflight.Executor) error {
	wtPath := reviewerWorktreePath(golemicDir, issueNumber)
	if _, err := executor.Run("git", "-C", repoRoot, "worktree", "remove", wtPath); err != nil {
		return fmt.Errorf("CLEANUP_REMOVE_FAILED: %w", err)
	}
	return nil
}
