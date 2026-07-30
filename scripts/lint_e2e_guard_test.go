package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scriptPath returns the absolute path to lint-e2e-guard.sh.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file path")
	}
	return filepath.Join(filepath.Dir(file), "lint-e2e-guard.sh")
}

// newGitRepo creates a temp dir, initialises a git repo, commits a file under
// test/e2e/, and returns the repo path plus the initial commit hash.
func newGitRepo(t *testing.T) (repoDir, baseCommit string) {
	t.Helper()
	dir := t.TempDir()

	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	git("init")
	git("config", "user.email", "test@test")
	git("config", "user.name", "test")

	e2eFile := filepath.Join(dir, "test", "e2e", "harness", "harness.go")
	if err := os.MkdirAll(filepath.Dir(e2eFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e2eFile, []byte("package harness\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "initial")
	base := git("rev-parse", "HEAD")

	return dir, base
}

// runGuard runs lint-e2e-guard.sh in repoDir with LINT_BASE_REF=baseRef and
// optional extra env vars. Returns (exitCode, stderr).
func runGuard(t *testing.T, repoDir, baseRef string, extraEnv map[string]string) (int, string) {
	t.Helper()
	c := exec.Command("sh", scriptPath(t))
	c.Dir = repoDir
	env := append(os.Environ(), "LINT_BASE_REF="+baseRef)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	c.Env = env

	var errBuf strings.Builder
	c.Stderr = &errBuf

	err := c.Run()
	if err == nil {
		return 0, errBuf.String()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), errBuf.String()
	}
	t.Fatalf("unexpected error running guard: %v", err)
	return -1, ""
}

func TestLintE2EGuard_NoChanges_PassesSilently(t *testing.T) {
	dir, base := newGitRepo(t)
	code, stderr := runGuard(t, dir, base, nil)
	if code != 0 {
		t.Errorf("expected exit 0, got %d; stderr: %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("expected no output, got: %s", stderr)
	}
}

func TestLintE2EGuard_ModifiedFile_FailsWithExplanation(t *testing.T) {
	dir, base := newGitRepo(t)

	// Commit a modification to the e2e file.
	e2eFile := filepath.Join(dir, "test", "e2e", "harness", "harness.go")
	if err := os.WriteFile(e2eFile, []byte("package harness\n// changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Use a single shell command to avoid shell splitting issues.
	commit := exec.Command("sh", "-c", "git add . && git commit -m modify")
	commit.Dir = dir
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	code, stderr := runGuard(t, dir, base, nil)
	if code == 0 {
		t.Error("expected non-zero exit, got 0")
	}
	if !strings.Contains(stderr, "E2E-MODIFY-APPROVED") {
		t.Errorf("expected E2E-MODIFY-APPROVED in stderr; got: %s", stderr)
	}
	if !strings.Contains(stderr, "harness.go") {
		t.Errorf("expected offending file in stderr; got: %s", stderr)
	}
}

func TestLintE2EGuard_DeletedFile_FailsWithExplanation(t *testing.T) {
	dir, base := newGitRepo(t)

	del := exec.Command("sh", "-c", "git rm test/e2e/harness/harness.go && git commit -m delete")
	del.Dir = dir
	del.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := del.CombinedOutput(); err != nil {
		t.Fatalf("delete commit: %v\n%s", err, out)
	}

	code, stderr := runGuard(t, dir, base, nil)
	if code == 0 {
		t.Error("expected non-zero exit, got 0")
	}
	if !strings.Contains(stderr, "E2E-MODIFY-APPROVED") {
		t.Errorf("expected E2E-MODIFY-APPROVED in stderr; got: %s", stderr)
	}
}

func TestLintE2EGuard_OnlyNewFile_PassesSilently(t *testing.T) {
	dir, base := newGitRepo(t)

	// Add a new e2e file (untracked, then commit it).
	newFile := filepath.Join(dir, "test", "e2e", "scenarios", "new_test.go")
	if err := os.MkdirAll(filepath.Dir(newFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("package scenarios\n"), 0644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("sh", "-c", "git add . && git commit -m add-new")
	add.Dir = dir
	add.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("add commit: %v\n%s", err, out)
	}

	code, stderr := runGuard(t, dir, base, nil)
	if code != 0 {
		t.Errorf("expected exit 0, got %d; stderr: %s", code, stderr)
	}
}

func TestLintE2EGuard_ApprovedByIssueBodyFile_Passes(t *testing.T) {
	dir, base := newGitRepo(t)

	// Commit a modification.
	e2eFile := filepath.Join(dir, "test", "e2e", "harness", "harness.go")
	if err := os.WriteFile(e2eFile, []byte("package harness\n// changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("sh", "-c", "git add . && git commit -m modify")
	commit.Dir = dir
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	// Write issue body file with the approval marker.
	bodyFile := filepath.Join(t.TempDir(), "issue-body.txt")
	body := "Some issue text\nE2E-MODIFY-APPROVED\nMore text\n"
	if err := os.WriteFile(bodyFile, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	code, stderr := runGuard(t, dir, base, map[string]string{
		"GOLEMIC_ISSUE_BODY_FILE": bodyFile,
	})
	if code != 0 {
		t.Errorf("expected exit 0, got %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "E2E-MODIFY-APPROVED") {
		t.Errorf("expected approval notice in stderr; got: %s", stderr)
	}
}

func TestLintE2EGuard_UnreadableBodyFile_TreatedAsNoApproval(t *testing.T) {
	dir, base := newGitRepo(t)

	// Commit a modification.
	e2eFile := filepath.Join(dir, "test", "e2e", "harness", "harness.go")
	if err := os.WriteFile(e2eFile, []byte("package harness\n// changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("sh", "-c", "git add . && git commit -m modify")
	commit.Dir = dir
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	code, stderr := runGuard(t, dir, base, map[string]string{
		"GOLEMIC_ISSUE_BODY_FILE": "/nonexistent/path/body.txt",
	})
	if code == 0 {
		t.Errorf("expected non-zero exit, got 0; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "not readable") {
		t.Errorf("expected 'not readable' message in stderr; got: %s", stderr)
	}
}

func TestLintE2EGuard_UncommittedModification_Fails(t *testing.T) {
	dir, base := newGitRepo(t)

	// Modify the file without committing.
	e2eFile := filepath.Join(dir, "test", "e2e", "harness", "harness.go")
	if err := os.WriteFile(e2eFile, []byte("package harness\n// dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	code, stderr := runGuard(t, dir, base, nil)
	if code == 0 {
		t.Errorf("expected non-zero exit, got 0; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "E2E-MODIFY-APPROVED") {
		t.Errorf("expected E2E-MODIFY-APPROVED in stderr; got: %s", stderr)
	}
}
