package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepGuard_NoGhPrReview enforces BR-008: the literal "gh pr review" invocation
// pattern must not appear in any production Go source file. Any violation risks a
// silent resurrection of the legacy submission path.
//
// Test files (*_test.go) are excluded because they may legitimately mention the
// pattern in assertions that verify the pattern is absent.
func TestGrepGuard_NoGhPrReview(t *testing.T) { //nolint:cyclop
	root := findModuleRoot(t)

	// Pattern that would appear in actual gh-invocation code (not in assertions).
	// This matches both "pr", "review" as separate string args and the inline form.
	const badPattern = `"pr", "review"`

	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only scan production Go files; test files may reference the pattern in assertions.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), badPattern) {
			rel, _ := filepath.Rel(root, path)
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk error: %v", err)
	}
	if len(violations) > 0 {
		t.Errorf("BR-008: 'gh pr review' invocation pattern found in production Go files (must use GraphQL exclusively):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// findModuleRoot walks up from the test binary's working directory to find go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}
