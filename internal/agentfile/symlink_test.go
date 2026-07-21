package agentfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// findRepoRoot locates the repository root by walking up from the current
// working directory looking for go.mod. Used by tests to find repo-relative paths.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	current := wd
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root
			t.Fatalf("could not find go.mod walking up from %s", wd)
		}
		current = parent
	}
}

// TestAgentSymlinkIdentity verifies that .pi/agents/{role}.md symlinks resolve
// to .golemic/agents/{role}.md and content is byte-identical. This test runs
// against the actual repository files at repo root.
func TestAgentSymlinkIdentity(t *testing.T) {
	repoRoot := findRepoRoot(t)

	roles := []string{"dev", "reviewer"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			piPath := filepath.Join(repoRoot, ".pi", "agents", role+".md")
			golemicPath := filepath.Join(repoRoot, ".golemic", "agents", role+".md")

			// Test 1: .pi/agents/{role}.md is a symlink
			stat, err := os.Lstat(piPath)
			if err != nil {
				t.Fatalf("os.Lstat(%s): %v", piPath, err)
			}
			if stat.Mode()&os.ModeSymlink == 0 {
				t.Errorf("%s is not a symlink (mode=%v)", piPath, stat.Mode())
			}

			// Test 2: Symlink resolves to correct target
			target, err := os.Readlink(piPath)
			if err != nil {
				t.Fatalf("os.Readlink(%s): %v", piPath, err)
			}

			// Resolve relative symlink from the link's directory
			linkDir := filepath.Dir(piPath)
			resolvedTarget := filepath.Join(linkDir, target)
			resolvedTarget = filepath.Clean(resolvedTarget)
			golemicPathClean := filepath.Clean(golemicPath)

			if resolvedTarget != golemicPathClean {
				t.Errorf("symlink target %s (resolved to %s) does not match expected %s",
					target, resolvedTarget, golemicPathClean)
			}

			// Test 3: Content is byte-identical
			piBytes, err := os.ReadFile(piPath)
			if err != nil {
				t.Fatalf("os.ReadFile(%s): %v", piPath, err)
			}
			golemicBytes, err := os.ReadFile(golemicPath)
			if err != nil {
				t.Fatalf("os.ReadFile(%s): %v", golemicPath, err)
			}

			if !bytes.Equal(piBytes, golemicBytes) {
				t.Errorf("content mismatch between %s and %s", piPath, golemicPath)
			}
		})
	}
}
