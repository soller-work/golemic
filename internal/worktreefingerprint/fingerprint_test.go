package worktreefingerprint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type realGitExecutor struct{}

func (realGitExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := realGitExecutor{}.RunInDir(dir, "git", args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test User")

	writeFile(t, filepath.Join(dir, "tracked.txt"), "hello\n")
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	git(t, dir, "add", "tracked.txt", ".gitignore")
	git(t, dir, "commit", "-m", "init")

	writeFile(t, filepath.Join(dir, "ignored.txt"), "ignored\n")
	return dir
}

func TestComputeStableOnCleanTree(t *testing.T) {
	dir := initRepo(t)

	fp1, err := Compute(dir, realGitExecutor{})
	if err != nil {
		t.Fatalf("Compute clean: %v", err)
	}
	fp2, err := Compute(dir, realGitExecutor{})
	if err != nil {
		t.Fatalf("Compute clean again: %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("clean fingerprint mismatch: %s vs %s", fp1, fp2)
	}
}

func assertFingerprintChanged(t *testing.T, mutate func(dir string)) {
	t.Helper()
	dir := initRepo(t)
	clean, err := Compute(dir, realGitExecutor{})
	if err != nil {
		t.Fatalf("Compute clean: %v", err)
	}
	mutate(dir)
	changed, err := Compute(dir, realGitExecutor{})
	if err != nil {
		t.Fatalf("Compute mutated: %v", err)
	}
	if clean == changed {
		t.Fatalf("fingerprint did not change: %s", clean)
	}
}

func TestComputeChangesOnTrackedEdit(t *testing.T) {
	assertFingerprintChanged(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "tracked.txt"), "edited\n")
	})
}

func TestComputeChangesOnStagedChange(t *testing.T) {
	assertFingerprintChanged(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "tracked.txt"), "staged\n")
		git(t, dir, "add", "tracked.txt")
	})
}

func TestComputeChangesOnDeletion(t *testing.T) {
	assertFingerprintChanged(t, func(dir string) {
		if err := os.Remove(filepath.Join(dir, "tracked.txt")); err != nil {
			t.Fatalf("remove tracked.txt: %v", err)
		}
	})
}

func TestComputeChangesOnUntrackedFile(t *testing.T) {
	assertFingerprintChanged(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "new.txt"), "new\n")
	})
}

func TestComputeIgnoresIgnoredOnlyChanges(t *testing.T) {
	dir := initRepo(t)
	clean, err := Compute(dir, realGitExecutor{})
	if err != nil {
		t.Fatalf("Compute clean: %v", err)
	}
	writeFile(t, filepath.Join(dir, "ignored.txt"), "ignored v2\n")
	changed, err := Compute(dir, realGitExecutor{})
	if err != nil {
		t.Fatalf("Compute ignored change: %v", err)
	}
	if clean != changed {
		t.Fatalf("ignored-only change altered fingerprint: %s vs %s", clean, changed)
	}
}
