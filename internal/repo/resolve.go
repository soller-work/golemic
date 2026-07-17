// Package repo provides utilities for resolving repository roots.
package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golemic/internal/preflight"
)

// ResolveHostRepo determines the host repository root that golemic operates on.
//
// golemic is dropped into an arbitrary subdirectory of a host repo (the location
// is free; tools/golemic is only the conventional default). This resolves the
// host repo's git root, not golemic's own, without hardcoding any path component.
//
// The signal is whether cwd lies inside the git root that git resolved:
//   - cwd inside gitRoot: golemic is its own repo or a real subdirectory of the
//     host repo; git already resolved the correct root, so return it.
//   - cwd outside gitRoot: git followed a symlink out of the host repo (golemic
//     symlinked in from a separate checkout), so walk the logical cwd path upward
//     to the nearest enclosing git root that differs from golemic's own.
//
// This handles, at any location:
//   - golemic symlinked into a host repo: /host-repo/<any-dir> → /actual/golemic
//   - golemic as a git submodule or regular subdirectory
//   - golemic as the main repo (no host repo; returns its own root)
func ResolveHostRepo(executor preflight.Executor, cwd string) (string, error) {
	// Get the git root of cwd (may be golemic itself if symlinked)
	gitRoot, err := executor.Run("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}
	gitRoot = strings.TrimSpace(gitRoot)
	if gitRoot == "" {
		return "", fmt.Errorf("not in a git repository")
	}

	// cwd inside gitRoot: git resolved the correct root (self or real subdir).
	if cwd == gitRoot || strings.HasPrefix(cwd, gitRoot+string(filepath.Separator)) {
		return gitRoot, nil
	}

	// cwd outside gitRoot: golemic was reached via a symlink out of the host repo.
	// Walk up the logical cwd path to find the enclosing host repo.
	golemicGitRoot := gitRoot

	current := cwd
	for current != "" && current != string(filepath.Separator) {
		gitDir := filepath.Join(current, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			// Found a .git; check its git root
			candidate, err := executor.Run("git", "-C", current, "rev-parse", "--show-toplevel")
			if err == nil {
				candidate = strings.TrimSpace(candidate)
				// Found a different git root; this is the host repo
				if candidate != "" && candidate != golemicGitRoot {
					return candidate, nil
				}
			}
		}
		current = filepath.Dir(current)
	}

	// No alternative git root found; return the current one
	// (golemic is the main repo, or no host repo with git exists)
	return gitRoot, nil
}
