//go:build e2e

package e2e

// AC→test mapping (spec 003_e2e-test-preflight-failures):
//
//	AC-001 "gh missing detected"       → TestPreflightFailures/gh_missing
//	AC-002 "tokens identical rejected" → TestPreflightFailures/tokens_identical
//	Additional (DoD test_cases):
//	  pi_missing     → TestPreflightFailures/pi_missing
//	  config_invalid → TestPreflightFailures/config_invalid
//	  token_missing  → TestPreflightFailures/token_missing

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golemic/test/e2e/harness"
)

// TestPreflightFailures verifies that each preflight check failure causes
// golemic run to exit 1 with the correct error on stderr, before any GitHub
// access (no runs dir is created under HOME/.golemic).
func TestPreflightFailures(t *testing.T) {
	binary := findBinary()
	if binary == "" {
		t.Skip("golemic binary not found — run `go build ./cmd/golemic` or set GOLEMIC_BINARY")
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available in test environment")
	}

	// AC-001: gh missing → FAILED: gh installiert, exit 1, no GitHub access.
	t.Run("gh_missing", func(t *testing.T) {
		sandbox := pfSandboxRepo(t)
		binDir := t.TempDir()
		pfLinkGit(t, binDir, realGit)
		pfWritePiShim(t, binDir)
		// no gh in bin

		tempHome := t.TempDir()
		result := pfInvoke(t, binary, sandbox, binDir, tempHome, nil)

		if result.exitCode == 0 {
			t.Fatalf("want exit 1, got 0\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "FAILED: gh installiert") {
			t.Errorf("want 'FAILED: gh installiert' in stderr, got:\n%s", result.stderr)
		}
		// Assert no GitHub state: nothing written under HOME/.golemic before gate returned.
		pfAssertNoGolemicDir(t, tempHome)
	})

	// pi missing → FAILED: pi installiert, exit 1.
	t.Run("pi_missing", func(t *testing.T) {
		sandbox := pfSandboxRepo(t)
		binDir := t.TempDir()
		pfLinkGit(t, binDir, realGit)
		pfWriteGhShim(t, binDir)
		// no pi in bin

		tempHome := t.TempDir()
		result := pfInvoke(t, binary, sandbox, binDir, tempHome, nil)

		if result.exitCode == 0 {
			t.Fatalf("want exit 1, got 0\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "FAILED: pi installiert") {
			t.Errorf("want 'FAILED: pi installiert' in stderr, got:\n%s", result.stderr)
		}
	})

	// config invalid → FAILED: config.json valide, exit 1.
	t.Run("config_invalid", func(t *testing.T) {
		sandbox := pfSandboxRepo(t)
		binDir := t.TempDir()
		pfLinkGit(t, binDir, realGit)
		pfWriteGhShim(t, binDir)
		pfWritePiShim(t, binDir)

		// .golemic/config.json exists (scaffolding check passes) but is not valid JSON.
		pfWriteConfig(t, sandbox, harness.BrokenConfigs()["not-json"])

		tempHome := t.TempDir()
		result := pfInvoke(t, binary, sandbox, binDir, tempHome, nil)

		if result.exitCode == 0 {
			t.Fatalf("want exit 1, got 0\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "FAILED: config.json valide") {
			t.Errorf("want 'FAILED: config.json valide' in stderr, got:\n%s", result.stderr)
		}
		// Verify earlier checks passed so this isn't a hollow pass.
		pfAssertStderrLacks(t, result.stderr,
			"FAILED: gh installiert",
			"FAILED: pi installiert",
			"FAILED: git",
			"FAILED: .golemic/ Scaffolding",
		)
	})

	// AC-002: both tokens identical → FAILED: Credentials - tokens identical, exit 1.
	t.Run("tokens_identical", func(t *testing.T) {
		sandbox := pfSandboxRepo(t)
		binDir := t.TempDir()
		pfLinkGit(t, binDir, realGit)
		pfWriteGhShim(t, binDir)
		pfWritePiShim(t, binDir)
		pfWriteConfig(t, sandbox, harness.ValidConfigJSON())

		tempHome := t.TempDir()
		env := map[string]string{
			"GOLEMIC_DEV_TOKEN":      "same-token-value",
			"GOLEMIC_REVIEWER_TOKEN": "same-token-value",
		}
		result := pfInvoke(t, binary, sandbox, binDir, tempHome, env)

		if result.exitCode == 0 {
			t.Fatalf("want exit 1, got 0\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "tokens identical") {
			t.Errorf("want 'tokens identical' in stderr, got:\n%s", result.stderr)
		}
		// Verify all earlier checks passed so this isn't a hollow pass.
		pfAssertStderrLacks(t, result.stderr,
			"FAILED: gh installiert",
			"FAILED: pi installiert",
			"FAILED: git",
			"FAILED: .golemic/ Scaffolding",
			"FAILED: config.json valide",
		)
	})

	// token missing → FAILED: Credentials, exit 1.
	t.Run("token_missing", func(t *testing.T) {
		sandbox := pfSandboxRepo(t)
		binDir := t.TempDir()
		pfLinkGit(t, binDir, realGit)
		pfWriteGhShim(t, binDir)
		pfWritePiShim(t, binDir)
		pfWriteConfig(t, sandbox, harness.ValidConfigJSON())

		// No GOLEMIC_*_TOKEN env vars and no credentials file → missing credentials error.
		tempHome := t.TempDir()
		result := pfInvoke(t, binary, sandbox, binDir, tempHome, nil)

		if result.exitCode == 0 {
			t.Fatalf("want exit 1, got 0\nstdout: %s\nstderr: %s", result.stdout, result.stderr)
		}
		if !strings.Contains(result.stderr, "FAILED: Credentials") {
			t.Errorf("want 'FAILED: Credentials' in stderr, got:\n%s", result.stderr)
		}
		// Verify all earlier checks passed so this isn't a hollow pass.
		pfAssertStderrLacks(t, result.stderr,
			"FAILED: gh installiert",
			"FAILED: pi installiert",
			"FAILED: git",
			"FAILED: .golemic/ Scaffolding",
			"FAILED: config.json valide",
		)
	})
}

// ---------------------------------------------------------------------------
// Helpers (preflight_failures scope; pf- prefix avoids collision with
// helpers in happy_path_test.go which share the e2e package).
// ---------------------------------------------------------------------------

type pfResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// pfInvoke runs `golemic run --issue 1` in workDir with a hermetic environment.
// PATH is set to binDir only; HOME is set to homeDir; extraEnv keys are appended.
// Ambient GH_TOKEN / GOLEMIC_* vars are NOT inherited.
func pfInvoke(t *testing.T, binary, workDir, binDir, homeDir string, extraEnv map[string]string) pfResult {
	t.Helper()
	cmd := exec.Command(binary, "run", "--issue", "1")
	cmd.Dir = workDir
	cmd.Env = []string{
		"PATH=" + binDir,
		"HOME=" + homeDir,
	}
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	return pfResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

// pfSandboxRepo creates a temporary git repository with an HTTPS origin and
// one commit, satisfying the git preflight check (worktree list, remote HTTPS).
func pfSandboxRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		// Isolate from ~/.gitconfig and system config so flags like
		// commit.gpgsign=true or core.hooksPath don't break sandbox setup.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sandbox setup %v: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Test User")
	run("git", "remote", "add", "origin", "https://github.com/example/golemic_e2e.git")

	// One commit so `git worktree list` and `git rev-parse --show-toplevel` work.
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("test repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "README.md")
	run("git", "commit", "-m", "init")

	return dir
}

// pfLinkGit symlinks the real git binary into binDir so git-based checks pass.
func pfLinkGit(t *testing.T, binDir, realGit string) {
	t.Helper()
	if err := os.Symlink(realGit, filepath.Join(binDir, "git")); err != nil {
		t.Fatal(err)
	}
}

// pfWriteGhShim writes a minimal gh shim that exits 0 on `gh --version`.
func pfWriteGhShim(t *testing.T, binDir string) {
	t.Helper()
	pfWriteShim(t, filepath.Join(binDir, "gh"), "#!/bin/sh\necho 'gh version 2.0.0'\n")
}

// pfWritePiShim writes a minimal pi shim that exits 0 on `pi --version`.
func pfWritePiShim(t *testing.T, binDir string) {
	t.Helper()
	pfWriteShim(t, filepath.Join(binDir, "pi"), "#!/bin/sh\necho 'pi 1.0.0'\n")
}

func pfWriteShim(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

// pfWriteConfig creates .golemic/config.json in the repository.
func pfWriteConfig(t *testing.T, repoDir, content string) {
	t.Helper()
	golemicDir := filepath.Join(repoDir, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// pfAssertNoGolemicDir asserts that HOME/.golemic was not created, proving no
// GitHub-side state was written before the preflight gate returned exit 1.
func pfAssertNoGolemicDir(t *testing.T, homeDir string) {
	t.Helper()
	golemicDir := filepath.Join(homeDir, ".golemic")
	if _, err := os.Stat(golemicDir); err == nil {
		t.Errorf("HOME/.golemic dir unexpectedly exists at %s — gate should have returned before any writes", golemicDir)
	}
}

// pfAssertStderrLacks fails if stderr contains any of the given substrings.
func pfAssertStderrLacks(t *testing.T, stderr string, substrs ...string) {
	t.Helper()
	for _, s := range substrs {
		if strings.Contains(stderr, s) {
			t.Errorf("earlier check unexpectedly failed — stderr contains %q:\n%s", s, stderr)
		}
	}
}
