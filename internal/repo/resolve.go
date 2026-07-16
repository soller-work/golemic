// Package repo provides utilities for resolving repository roots.
package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golemic/internal/preflight"
)

// ResolveHostRepo determines the host repository root when golemic is symlinked into it.
//
// When golemic is installed as tools/golemic (symlink or subdir) in a host repo,
// this function finds the host repo's git root, not the golemic repo's root.
//
// Algorithm:
//  1. Get the git root of the current directory (may be golemic itself if symlinked)
//  2. If we're under tools/golemic, walk up the directory tree
//  3. For each directory with a .git subdirectory, check its git root
//  4. Skip any git root that matches the golemic repo's root
//  5. Return the first different git root found (the host repo)
//  6. Fall back to the cwd's git root if no alternative is found
//
// This handles:
//   - golemic symlinked into a host repo: /host-repo/tools/golemic → /actual/golemic
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

	// If we're not under tools/golemic, assume this is the host repo
	if !strings.Contains(cwd, "/tools/golemic") {
		return gitRoot, nil
	}

	// We're under tools/golemic; try to find the enclosing host repo
	// by walking up from cwd and checking for a different git root

	gollemicGitRoot := gitRoot

	current := cwd
	for current != "" && current != "/" {
		gitDir := filepath.Join(current, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			// Found a .git; check its git root
			candidate, err := executor.Run("git", "-C", current, "rev-parse", "--show-toplevel")
			if err == nil {
				candidate = strings.TrimSpace(candidate)
				// Found a different git root; this is the host repo
				if candidate != "" && candidate != gollemicGitRoot {
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
