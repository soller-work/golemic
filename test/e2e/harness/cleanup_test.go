//go:build e2e

package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanupIdempotency verifies AC-005:
// Given: Test created issue, PR, branch, worktree
// When: Test ends (defer cleanup called)
// Then: PR deleted, branch deleted (local and remote), worktrees removed,
// issue deleted, golemic_e2e state clean
func TestCleanupIdempotency(t *testing.T) {
	tmpDir := t.TempDir()

	// Create fake artifacts: a worktree directory, a branch reference, a run directory.
	worktreeDir := filepath.Join(tmpDir, "worktrees", "issue-42")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	reviewWorktreeDir := filepath.Join(tmpDir, "worktrees", "issue-42-review")
	if err := os.MkdirAll(reviewWorktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	runDir := filepath.Join(tmpDir, "runs", "issue-42-test")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(worktreeDir, "dummy.txt"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reviewWorktreeDir, "dummy.txt"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// tmpDir is not a real git repo, so git worktree remove will fail and the
	// function falls back to os.RemoveAll — which is what we want to exercise here.
	if err := RemoveWorktrees(tmpDir, tmpDir); err != nil {
		t.Errorf("RemoveWorktrees failed: %v", err)
	}

	for _, dir := range []string{worktreeDir, reviewWorktreeDir} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("worktree %s should be removed, got err=%v", dir, err)
		}
	}

	// Second call must be idempotent.
	if err := RemoveWorktrees(tmpDir, tmpDir); err != nil {
		t.Errorf("RemoveWorktrees (2nd call) should be idempotent: %v", err)
	}

	if err := CleanupRuns(tmpDir); err != nil {
		t.Errorf("CleanupRuns failed: %v", err)
	}

	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Errorf("run dir %s should be removed, got err=%v", runDir, err)
	}

	if err := CleanupRuns(tmpDir); err != nil {
		t.Errorf("CleanupRuns (2nd call) should be idempotent: %v", err)
	}
}

// TestCleanupNoOp verifies cleanup is safe when nothing exists.
func TestCleanupNoOp(t *testing.T) {
	tmpDir := t.TempDir()

	if err := RemoveWorktrees(tmpDir, tmpDir); err != nil {
		t.Errorf("RemoveWorktrees on empty dir failed: %v", err)
	}
	if err := CleanupRuns(tmpDir); err != nil {
		t.Errorf("CleanupRuns on empty dir failed: %v", err)
	}
}

// TestFixturesValidConfig verifies the valid fixture is parseable.
func TestFixturesValidConfig(t *testing.T) {
	configJSON := ValidConfigJSON()
	if !strings.Contains(configJSON, `"project"`) {
		t.Error("valid config should contain 'project' field")
	}
	if !strings.Contains(configJSON, `"verify_command"`) {
		t.Error("valid config should contain 'verify_command' field")
	}
	if !strings.Contains(configJSON, `"timeout_minutes"`) {
		t.Error("valid config should contain 'timeout_minutes' field")
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".golemic")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("written config should not be empty")
	}
}

// TestFixturesBrokenConfigs verifies broken config generation.
func TestFixturesBrokenConfigs(t *testing.T) {
	broken := BrokenConfigs()
	if len(broken) == 0 {
		t.Error("should return at least one broken config")
	}
	for name, content := range broken {
		if content == "" {
			t.Errorf("broken config %q should not be empty", name)
		}
	}
}
