package worktreefingerprint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Executor runs git commands in a worktree.
type Executor interface {
	RunInDir(dir string, name string, args ...string) (string, error)
}

// Compute returns the deterministic working-tree fingerprint for worktreePath.
func Compute(worktreePath string, executor Executor) (string, error) {
	if executor == nil {
		return "", fmt.Errorf("worktreefingerprint: nil executor")
	}

	statusOut, err := executor.RunInDir(worktreePath, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", fmt.Errorf("worktreefingerprint: git status: %w", err)
	}

	diffOut, err := executor.RunInDir(worktreePath, "git", "diff", "--binary", "HEAD")
	if err != nil {
		return "", fmt.Errorf("worktreefingerprint: git diff: %w", err)
	}

	untrackedOut, err := executor.RunInDir(worktreePath, "git", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", fmt.Errorf("worktreefingerprint: git ls-files: %w", err)
	}

	h := sha256.New()
	_, _ = io.WriteString(h, statusOut)
	_, _ = io.WriteString(h, diffOut)

	paths := parseNULList(untrackedOut)
	sort.Strings(paths)
	for _, relPath := range paths {
		if relPath == "" {
			continue
		}
		data, err := readEntry(filepath.Join(worktreePath, filepath.FromSlash(relPath)))
		if err != nil {
			return "", fmt.Errorf("worktreefingerprint: read %s: %w", relPath, err)
		}
		if _, err := fmt.Fprintf(h, "%d:%s%d:", len(relPath), relPath, len(data)); err != nil {
			return "", fmt.Errorf("worktreefingerprint: frame %s: %w", relPath, err)
		}
		if _, err := h.Write(data); err != nil {
			return "", fmt.Errorf("worktreefingerprint: hash %s: %w", relPath, err)
		}
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func parseNULList(out string) []string {
	if out == "" {
		return nil
	}
	parts := bytes.Split([]byte(out), []byte{0})
	paths := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		paths = append(paths, string(p))
	}
	return paths
}

func readEntry(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		return []byte(target), nil
	}
	return os.ReadFile(path)
}
