package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const cbmVersion = "codebase-memory-mcp@0.9.0"

// cbmDevTools are the read/query MCP tools granted to the dev role.
var cbmDevTools = []string{"search_code", "find_symbol", "get_related"}

// cbmReviewerTools are the read/query MCP tools granted to the reviewer role
// (dev tools plus detect_changes for git-diff blast-radius analysis).
var cbmReviewerTools = append(append([]string{}, cbmDevTools...), "detect_changes")

// cbmCacheDir returns the per-issue CBM cache directory shared across dev and reviewer roles.
func (r *Runner) cbmCacheDir() string {
	return filepath.Join(r.homeDir, ".golemic", r.project, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
}

// cbmIndex invokes the indexer for a worktree. Fail-soft: logs on non-zero exit
// and continues without aborting the role (BR-7).
func (r *Runner) cbmIndex(worktreePath string) {
	cacheDir := r.cbmCacheDir()
	env := map[string]string{
		"CBM_CACHE_DIR": cacheDir,
		"CBM_LOG_LEVEL": "warn",
	}
	if _, err := r.executor.RunWithEnvInDir(env, worktreePath,
		"npx", "-y", cbmVersion, "cli", "index_repository",
		"--repo-path", worktreePath, "--mode", "fast",
	); err != nil {
		fmt.Fprintf(r.stderr, "codebase-memory: index failed (proceeding without code intelligence): %v\n", err) //nolint:errcheck
	}
}

// cbmSetup writes .mcp.json and adds it to .git/info/exclude for a worktree.
// Returns true on success; false on any failure (caller should treat the feature
// as disabled for this role to preserve the BR-1 invariant).
func (r *Runner) cbmSetup(worktreePath, role string) bool {
	if err := r.writeMCPJSON(worktreePath); err != nil {
		fmt.Fprintf(r.stderr, "codebase-memory: failed to write .mcp.json (%v); proceeding without code intelligence\n", err) //nolint:errcheck
		return false
	}
	if err := addGitExclude(worktreePath); err != nil {
		fmt.Fprintf(r.stderr, "codebase-memory: failed to update .git/info/exclude (%v); proceeding without code intelligence\n", err) //nolint:errcheck
		return false
	}
	return true
}

type mcpServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type mcpConfig struct {
	MCPServers map[string]mcpServerConfig `json:"mcpServers"`
}

func (r *Runner) writeMCPJSON(worktreePath string) error {
	cacheDir := r.cbmCacheDir()
	cfg := mcpConfig{
		MCPServers: map[string]mcpServerConfig{
			"codebase-memory": {
				Command: "npx",
				Args:    []string{"-y", cbmVersion},
				Env: map[string]string{
					"CBM_CACHE_DIR":    cacheDir,
					"CBM_LOG_LEVEL":    "warn",
					"CBM_ALLOWED_ROOT": worktreePath,
				},
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal .mcp.json: %w", err)
	}
	dest := filepath.Join(worktreePath, ".mcp.json")
	if err := os.WriteFile(dest, data, 0644); err != nil { //nolint:gosec
		return fmt.Errorf("write .mcp.json: %w", err)
	}
	return nil
}

func addGitExclude(worktreePath string) error {
	excludePath := filepath.Join(worktreePath, ".git", "info", "exclude")
	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .git/info/exclude: %w", err)
	}
	if strings.Contains(string(existing), ".mcp.json") {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644) //nolint:gosec
	if err != nil {
		return fmt.Errorf("open .git/info/exclude: %w", err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := fmt.Fprintln(f, ".mcp.json"); err != nil {
		return fmt.Errorf("write .git/info/exclude: %w", err)
	}
	return nil
}

// cbmCleanup removes the per-issue CBM cache directory. Fail-soft.
func (r *Runner) cbmCleanup() {
	cacheDir := r.cbmCacheDir()
	if err := os.RemoveAll(cacheDir); err != nil {
		fmt.Fprintf(r.stderr, "Warning: CBM cache cleanup failed: %v\n", err) //nolint:errcheck
	}
}
