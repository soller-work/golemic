package e2e

// AC→test mapping (spec 005_e2e-test-dev-role-failures):
//
//	AC-001 "verify_command fails; no push" → TestDevFailure/verify_command_fails
//	AC-002 "Token invalid; PR fails"        → TestDevFailure/invalid_dev_token
//	AC-003 "PR creation fails"              → TestDevFailure/pr_creation_fails

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDevFailure verifies that each dev-role failure mode causes golemic to exit 1
// with outcome dev_failed and without creating a GitHub PR.
//
// Each subtest uses a hermetic sandbox with a local bare git repo as origin
// and script shims for git/gh/pi.  No real GitHub access is required.
//
// Business rules verified:
//
//	BR-001: any dev-step failure aborts the whole run (reviewer never runs)
//	BR-002: verify_command must pass before push; if it fails, no push occurs
func TestDevFailure(t *testing.T) {
	binary := findBinary()
	if binary == "" {
		t.Skip("golemic binary not found — run `go build ./cmd/golemic` or set GOLEMIC_BINARY")
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available in test environment")
	}

	tests := []struct {
		name        string
		devfailMode string // DEVFAIL_MODE env var for pi shim
		pushMode    string // DEVFAIL_PUSH_MODE env var for git shim ("fail"/"succeed")
		wantBranch  bool   // expect golemic/issue-1 branch in bare repo after run
	}{
		{
			// AC-001 / BR-002: verify_command exits non-zero → dev commits but
			// never pushes; runner sees no pr_opened event → dev_failed.
			name:        "verify_command_fails",
			devfailMode: "verify_fails",
			pushMode:    "fail",
			wantBranch:  false,
		},
		{
			// AC-002 / BR-001: token invalid → push fails; no PR, exit 1.
			name:        "invalid_dev_token",
			devfailMode: "push_fails",
			pushMode:    "fail",
			wantBranch:  false,
		},
		{
			// AC-003 / BR-001: push succeeds but PR creation fails; branch on
			// origin, no PR, exit 1.
			name:        "pr_creation_fails",
			devfailMode: "pr_fails",
			pushMode:    "succeed",
			wantBranch:  true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			bareRepo, sandbox := dfSetupSandbox(t, realGit)
			binDir := t.TempDir()
			tempHome := t.TempDir()

			dfWriteConfig(t, sandbox)
			dfWriteGitShim(t, binDir, realGit, bareRepo)
			dfWriteGhShim(t, binDir)
			dfWritePiShim(t, binDir)

			env := map[string]string{
				"GOLEMIC_DEV_TOKEN":      "fake-dev-token",
				"GOLEMIC_REVIEWER_TOKEN": "fake-reviewer-token",
				"DEVFAIL_MODE":           tc.devfailMode,
				"DEVFAIL_PUSH_MODE":      tc.pushMode,
				"DEVFAIL_BARE_REPO":      bareRepo,
			}
			result := dfInvoke(t, binary, sandbox, binDir, tempHome, env)

			// BR-001: dev failure must abort the run with exit 1.
			if result.exitCode != 1 {
				t.Errorf("want exit 1, got %d\nstdout: %s\nstderr: %s",
					result.exitCode, result.stdout, result.stderr)
			}

			// Runner writes "dev_failed" to stderr in all dev-failure paths.
			if !strings.Contains(result.stderr, "dev_failed") {
				t.Errorf("want 'dev_failed' in stderr, got:\n%s", result.stderr)
			}

			// BR-001: no PR must be opened when dev fails (reviewer never runs).
			dfAssertNoPR(t, result.stdout, tempHome)

			// Per-AC branch-presence assertion (BR-002 distinction):
			//   AC-001/002: push must not have occurred → no branch in bare repo.
			//   AC-003: push succeeded, only PR step failed → branch in bare repo.
			hasBranch := dfBranchInBare(t, realGit, bareRepo, "golemic/issue-1")
			switch {
			case tc.wantBranch && !hasBranch:
				t.Errorf("AC-003: want branch golemic/issue-1 on origin (push succeeded before PR failed), got none")
			case !tc.wantBranch && hasBranch:
				t.Errorf("BR-002 violated: branch golemic/issue-1 was pushed to origin before dev step completed cleanly")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers (df- prefix to avoid collisions with helpers in other e2e tests)
// ---------------------------------------------------------------------------

type dfResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// dfInvoke runs `golemic run --issue 1` in workDir with a hermetic environment.
// PATH is restricted to binDir; HOME is tempHome; extraEnv keys are appended.
// Ambient GH_TOKEN / GOLEMIC_* / DEVFAIL_* vars are NOT inherited.
func dfInvoke(t *testing.T, binary, workDir, binDir, homeDir string, extraEnv map[string]string) dfResult {
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
	return dfResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

// dfSetupSandbox creates:
//   - a local bare git repo (the "origin")
//   - a working git repo with origin pointing to the bare repo
//   - .golemic/guidelines/dev.md (required by the runner's prompt renderer)
//
// prompts/dev.md must exist next to the golemic binary (BR-003: never in the fixture repo).
//
// Returns (bareRepoPath, sandboxRepoPath).
func dfSetupSandbox(t *testing.T, realGit string) (string, string) {
	t.Helper()

	bareParent := t.TempDir()
	bareRepo := filepath.Join(bareParent, "repo.git")
	workDir := t.TempDir()

	// Isolate from user git config so hooks/signing don't break setup.
	gitEnv := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sandbox setup %v: %v\n%s", args, err, out)
		}
	}

	// Create bare repo (acts as the remote "origin").
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatal(err)
	}
	run(bareRepo, realGit, "init", "--bare", bareRepo)

	// Create working repo.
	run(workDir, realGit, "init")
	run(workDir, realGit, "config", "user.email", "test@example.com")
	run(workDir, realGit, "config", "user.name", "Test User")

	// Initial commit so we have something to push as main.
	readme := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(readme, []byte("test repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(workDir, realGit, "add", "README.md")
	run(workDir, realGit, "commit", "-m", "init")

	// Set origin to the local bare repo and push as main.
	// Using explicit refspec so the local branch name doesn't matter.
	run(workDir, realGit, "remote", "add", "origin", bareRepo)
	run(workDir, realGit, "push", "origin", "HEAD:refs/heads/main")

	// Fetch so origin/main tracking ref exists in the working repo.
	run(workDir, realGit, "fetch", "origin")

	guidelinesDir := filepath.Join(workDir, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"),
		[]byte("# Guidelines\nFollow best practices.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	return bareRepo, workDir
}

// dfWriteConfig creates .golemic/config.json in the sandbox repo.
// The verify_command is set to `exit 1` — if the pi shim were a real agent it
// would honour this; the pi shim's DEVFAIL_MODE controls actual behaviour instead.
func dfWriteConfig(t *testing.T, repoDir string) {
	t.Helper()
	golemicDir := filepath.Join(repoDir, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
  "project": "golemic_e2e",
  "verify_command": "exit 1",
  "label": "ready-for-agent",
  "timeout_minutes": 5,
  "models": {
    "dev": "test/dev-model",
    "reviewer": "test/reviewer-model"
  }
}`
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
}

// dfWriteGitShim writes a git wrapper to binDir.
//
// The shim intercepts two commands to enable hermetic testing:
//
//  1. `git config --get remote.origin.url` — returns a fake HTTPS URL so the
//     preflight origin-URL check passes even though the real origin is a local
//     bare repo.
//
//  2. `git push origin …` — behaviour controlled by DEVFAIL_PUSH_MODE:
//     - "succeed": redirects push to DEVFAIL_BARE_REPO (simulates AC-003 push success)
//     - anything else: exits 128 with an auth-error message (simulates AC-002 push failure)
//
// All other git commands are delegated to the real git binary (realGit).
func dfWriteGitShim(t *testing.T, binDir, realGit, bareRepo string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
REAL_GIT=%q

# Preflight HTTPS check: intercept "git config --get remote.origin.url".
# The real origin is a local bare repo; we return a fake HTTPS URL here so the
# preflight URL-must-be-HTTPS assertion passes.
if [ "$1" = "config" ] && [ "$2" = "--get" ] && [ "$3" = "remote.origin.url" ]; then
  echo "https://github.com/example/golemic_e2e.git"
  exit 0
fi

# Push interception: "git push origin <refspec>".
# Only intercept the direct form (not git -C <path> push); the pi shim runs in
# the worktree CWD and issues push without -C.
if [ "$1" = "push" ] && [ "$2" = "origin" ]; then
  case "${DEVFAIL_PUSH_MODE}" in
    succeed)
      # Redirect push to the local bare repo so the branch appears on "origin"
      # without any network access.  Shift off "push" and "origin", keep refspec.
      shift 2
      exec "$REAL_GIT" push %q "$@"
      ;;
    *)
      # Simulate authentication failure (AC-002: invalid dev token).
      printf "error: Authentication failed for 'https://github.com/example/golemic_e2e.git/'\n" >&2
      exit 128
      ;;
  esac
fi

exec "$REAL_GIT" "$@"
`, realGit, bareRepo)
	dfWriteShim(t, filepath.Join(binDir, "git"), script)
}

// dfWriteGhShim writes a gh CLI shim to binDir.
//
// Handled subcommands:
//   - gh --version           → version string (preflight)
//   - gh issue view N …      → minimal JSON issue (runner PS-004)
//   - gh pr list --head …    → empty JSON array (collision check)
func dfWriteGhShim(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "gh version 2.0.0"
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  printf '{"title":"Dev Failure Test Issue","body":"Automated dev failure E2E test.","state":"OPEN","labels":[]}'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '[]'
  exit 0
fi
if [ "$1" = "label" ] && [ "$2" = "list" ]; then
  printf '[{"name":"in-progress"},{"name":"needs-human"}]'
  exit 0
fi
printf "gh shim: unhandled command: %s\n" "$*" >&2
exit 1
`
	dfWriteShim(t, filepath.Join(binDir, "gh"), script)
}

// dfWritePiShim writes the pi agent shim to binDir.
//
// DEVFAIL_MODE controls the simulated failure:
//
//   - "verify_fails": agent exits 1 without pushing (AC-001 — verify_command
//     failure aborts before push; no pr_opened event written).
//   - "push_fails": agent attempts `git push origin …` which the git shim
//     rejects, then exits 1 (AC-002 — push failure; no pr_opened event).
//   - "pr_fails": agent pushes (git shim redirects to bare repo, succeeds)
//     but does NOT write pr_opened event, then exits 1 (AC-003 — PR creation
//     failure; branch on origin, no PR).
//
// In all modes the pi shim deliberately omits writing the pr_opened event to
// GOLEMIC_EVENT_LOG, which is what causes the runner to emit dev_failed.
func dfWritePiShim(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "pi 1.0.0"
  exit 0
fi

# Agent invocation (pi -p …).
# Extract issue number from GOLEMIC_RUN_ID (format: issue-N-TIMESTAMP).
# Uses only shell built-ins; sed is not available in the restricted PATH.
RUN_ID="${GOLEMIC_RUN_ID:-0}"
NO_PREFIX="${RUN_ID#issue-}"
ISSUE_NUM="${NO_PREFIX%%-*}"
BRANCH="golemic/issue-$ISSUE_NUM"

case "${DEVFAIL_MODE}" in
  verify_fails)
    # AC-001 / BR-002: verify_command would fail; agent aborts without pushing.
    # No pr_opened event → runner emits dev_failed.
    exit 1
    ;;
  push_fails)
    # AC-002: verify passes, but push fails (git shim returns auth error).
    # The "|| true" lets the script proceed to exit 1 regardless.
    git push origin "HEAD:refs/heads/$BRANCH" 2>&1 || true
    exit 1
    ;;
  pr_fails)
    # AC-003: verify passes, push succeeds (git shim redirects to bare repo),
    # but PR creation fails.  Branch is on origin; no pr_opened event.
    git push origin "HEAD:refs/heads/$BRANCH" 2>&1 || true
    exit 1
    ;;
  *)
    printf "pi shim: DEVFAIL_MODE not set or unrecognised: %s\n" "${DEVFAIL_MODE}" >&2
    exit 1
    ;;
esac
`
	dfWriteShim(t, filepath.Join(binDir, "pi"), script)
}

func dfWriteShim(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

// dfAssertNoPR reads the event log from the run recorded in stdout and asserts
// that no pr_opened event was written, verifying BR-001 (dev failure → no PR).
func dfAssertNoPR(t *testing.T, stdout, tempHome string) {
	t.Helper()
	runID := parseRunID(stdout)
	if runID == "" {
		// No run ID in stdout means the run ended very early (preflight failure),
		// so no PR could have been created.
		return
	}
	eventsPath := filepath.Join(tempHome, ".golemic", "golemic_e2e", "runs", runID, "events.jsonl")
	if hasEvent(eventsPath, "pr_opened") {
		t.Errorf("BR-001: pr_opened event found in %s despite dev failure — PR was created when it should not have been", eventsPath)
	}
}

// dfBranchInBare returns true if branchName exists as a branch in the local bare repo.
// Uses the real git binary directly (bypassing the shim) to inspect the bare repo.
func dfBranchInBare(t *testing.T, realGit, bareRepo, branchName string) bool {
	t.Helper()
	cmd := exec.Command(realGit, "ls-remote", "--heads", bareRepo, branchName)
	out, err := cmd.Output()
	if err != nil {
		// ls-remote failure against a local bare repo is unexpected; log but
		// return false (no branch) so the assertion below captures the state.
		t.Logf("dfBranchInBare: git ls-remote failed: %v", err)
		return false
	}
	return strings.Contains(string(out), branchName)
}
