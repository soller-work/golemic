// Package lintnoprodnolint contains integration tests for the lint-no-prod-nolint Makefile target.
package lintnoprodnolint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the repository root by walking up from this file's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return dir
}

// fixtureRepo creates a minimal git repository in dir with the given files.
func fixtureRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture setup %v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-b", "main")
	run("git", "config", "user.email", "test@test")
	run("git", "config", "user.name", "test")

	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	// Also copy in the Makefile from the real repo so git ls-files works.
	makefile := filepath.Join(repoRoot(t), "Makefile")
	data, err := os.ReadFile(makefile)
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), data, 0o644); err != nil {
		t.Fatalf("write Makefile copy: %v", err)
	}

	run("git", "add", ".")
	run("git", "commit", "-m", "fixture")
}

// runTarget executes make <target> in dir and returns stdout+stderr and the error.
func runTarget(t *testing.T, dir, target string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", target)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// AC-001: Clean production tree passes the ban.
func TestCleanTreePasses(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"cmd/golemic/main.go": "package main\n\nfunc main() {}\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err != nil {
		t.Fatalf("expected exit 0, got error: %v\noutput: %s", err, out)
	}
}

// AC-002: Single banned directive in a production file causes exit 1.
func TestSingleBannedDirectiveFails(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"cmd/golemic/main.go": "package main\n\nfunc big() {} //nolint:cyclop\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err == nil {
		t.Fatal("expected exit 1, got exit 0")
	}
	if !strings.Contains(out, "cmd/golemic/main.go") {
		t.Errorf("expected cmd/golemic/main.go in output, got: %s", out)
	}
	if !strings.Contains(out, "nolint:cyclop") {
		t.Errorf("expected nolint:cyclop in output, got: %s", out)
	}
}

// AC-003: Combined banned directive in internal file causes exit 1.
func TestCombinedBannedDirectiveFails(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"internal/runner/runner.go": "package runner\n\nfunc run() {} //nolint:cyclop,gocognit,funlen\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err == nil {
		t.Fatal("expected exit 1, got exit 0")
	}
	if !strings.Contains(out, "internal/runner/runner.go") {
		t.Errorf("expected internal/runner/runner.go in output, got: %s", out)
	}
}

// AC-004: Complexity nolint inside a test file is ignored.
func TestTestFileExempt(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"internal/runner/runner_test.go": "package runner\n\nfunc TestBig(t *testing.T) {} //nolint:cyclop,gocognit\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err != nil {
		t.Fatalf("expected exit 0, got error: %v\noutput: %s", err, out)
	}
	if strings.Contains(out, "runner_test.go") {
		t.Errorf("test file should not appear in output, got: %s", out)
	}
}

// AC-005: errcheck nolint in production file is allowed.
func TestErrcheckAllowed(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"cmd/golemic/main.go": "package main\n\nfunc main() { _ = doIt() } //nolint:errcheck\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err != nil {
		t.Fatalf("expected exit 0, got error: %v\noutput: %s", err, out)
	}
}

// AC-006: make lint invokes lint-no-prod-nolint so it propagates failure.
// We verify this with a dry-run (make -n lint) against the real repo Makefile,
// avoiding the need to actually run golangci-lint in the fixture.
func TestMakeLintInvokesLintNoProdNolint(t *testing.T) {
	cmd := exec.Command("make", "-n", "lint")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n lint failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "lint-no-prod-nolint") {
		t.Errorf("make lint dry-run does not invoke lint-no-prod-nolint; output:\n%s", out)
	}
}

// New test: Top-level file in cmd/ is caught.
func TestTopLevelCmdFileCaught(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"cmd/tool.go": "package main\n\nfunc big() {} //nolint:cyclop\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err == nil {
		t.Fatal("expected exit 1, got exit 0")
	}
	if !strings.Contains(out, "cmd/tool.go") {
		t.Errorf("expected cmd/tool.go in output, got: %s", out)
	}
	if !strings.Contains(out, "nolint:cyclop") {
		t.Errorf("expected nolint:cyclop in output, got: %s", out)
	}
}

// New test: Top-level file in internal/ is caught.
func TestTopLevelInternalFileCaught(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"internal/doc.go": "package internal\n\nfunc big() {} //nolint:gocognit\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err == nil {
		t.Fatal("expected exit 1, got exit 0")
	}
	if !strings.Contains(out, "internal/doc.go") {
		t.Errorf("expected internal/doc.go in output, got: %s", out)
	}
	if !strings.Contains(out, "nolint:gocognit") {
		t.Errorf("expected nolint:gocognit in output, got: %s", out)
	}
}

// New test: Violation count line appears on failure.
func TestViolationCountLine(t *testing.T) {
	dir := t.TempDir()
	fixtureRepo(t, dir, map[string]string{
		"cmd/a.go":        "package main\n\nfunc a() {} //nolint:cyclop\n",
		"internal/b.go":   "package internal\n\nfunc b() {} //nolint:funlen\n",
		"cmd/sub/c.go":    "package sub\n\nfunc c() {} //nolint:nestif\n",
	})

	out, err := runTarget(t, dir, "lint-no-prod-nolint")
	if err == nil {
		t.Fatal("expected exit 1, got exit 0")
	}
	if !strings.Contains(out, "3 violation(s) found") {
		t.Errorf("expected '3 violation(s) found' in output, got: %s", out)
	}
}
