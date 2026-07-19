package e2e

// AC→test mapping (spec 006_e2e-test-reviewer-role-failures):
//
//	AC-001 "verify_command fails; no review" → TestReviewerFailure/verify_command_fails
//	AC-002 "Token invalid; review fails"     → TestReviewerFailure/token_invalid
//	AC-003 "Review submission fails"         → TestReviewerFailure/review_submission_fails

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReviewerFailure verifies that each reviewer-role failure mode causes golemic
// to exit 1 with outcome review_failed, while the dev step (PR creation) is
// confirmed to have succeeded (pr_opened event present, no review_submitted event).
//
// Each subtest uses a fully hermetic sandbox: local bare git repo as origin,
// script shims for git/gh/pi in a restricted PATH, no real GitHub access.
//
// Business rules verified:
//
//	BR-001: any reviewer-step failure ends the run with exit 1, review_failed
//	BR-002: verify_command must pass before review submission; if it fails, no review
func TestReviewerFailure(t *testing.T) {
	binary := findBinary()
	if binary == "" {
		t.Skip("golemic binary not found — run `go build ./cmd/golemic` or set GOLEMIC_BINARY")
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available in test environment")
	}

	tests := []struct {
		name      string
		rfailMode string // RFAIL_MODE env var for pi shim (reviewer role behaviour)
	}{
		{
			// AC-001 / BR-002: reviewer verify_command fails → pi exits 1 before
			// any submission; no review_submitted event, exit 1, review_failed.
			name:      "verify_command_fails",
			rfailMode: "verify_fails",
		},
		{
			// AC-002 / BR-001: reviewer token invalid → GraphQL auth error;
			// pi exits 1; no review_submitted event, exit 1, review_failed.
			name:      "token_invalid",
			rfailMode: "token_invalid",
		},
		{
			// AC-003 / BR-001: GitHub API error during review submission →
			// GraphQL returns server error; pi exits 1; no review_submitted event,
			// exit 1, review_failed.
			name:      "review_submission_fails",
			rfailMode: "submission_fails",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			bareRepo, sandbox := rfSetupSandbox(t, realGit)
			binDir := t.TempDir()
			tempHome := t.TempDir()

			rfWriteConfig(t, sandbox)
			rfWriteGitShim(t, binDir, realGit)
			rfWriteGhShim(t, binDir)
			rfWritePiShim(t, binDir)

			env := map[string]string{
				"GOLEMIC_DEV_TOKEN":      "fake-dev-token",
				"GOLEMIC_REVIEWER_TOKEN": "fake-reviewer-token",
				"RFAIL_MODE":             tc.rfailMode,
				// Expose bare repo path so the pi shim can push to it directly.
				"RFAIL_BARE_REPO": bareRepo,
			}
			result := rfInvoke(t, binary, sandbox, binDir, tempHome, env)

			// BR-001: reviewer failure must abort the run with exit 1.
			if result.exitCode != 1 {
				t.Errorf("want exit 1, got %d\nstdout: %s\nstderr: %s",
					result.exitCode, result.stdout, result.stderr)
			}

			// Runner writes "review_failed" to stderr in all reviewer-failure paths.
			if !strings.Contains(result.stderr, "review_failed") {
				t.Errorf("want 'review_failed' in stderr, got:\n%s", result.stderr)
			}

			// Locate the run's event log.
			runID := parseRunID(result.stdout)
			if runID == "" {
				t.Fatalf("could not extract run ID from stdout — dev step may have failed before run_started\nstdout: %s\nstderr: %s",
					result.stdout, result.stderr)
			}
			eventsPath := filepath.Join(tempHome, ".golemic", "golemic_e2e", "runs", runID, "events.jsonl")

			// PS-001 / AC-*: dev step succeeded → pr_opened must be present.
			if !hasEvent(eventsPath, "pr_opened") {
				t.Errorf("want pr_opened event (dev succeeded and PR exists), not found in %s\nstderr: %s",
					eventsPath, result.stderr)
			}

			// BR-001 / BR-002: no review must have been submitted in any failure case.
			if hasEvent(eventsPath, "review_submitted") {
				t.Errorf("want no review_submitted event (reviewer failed before submission), found one in %s",
					eventsPath)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers (rf- prefix to avoid collisions with helpers in other e2e tests)
// ---------------------------------------------------------------------------

type rfResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// rfInvoke runs `golemic run --issue 1` in workDir with a hermetic environment.
// PATH is restricted to binDir; HOME is tempHome; extraEnv keys are appended.
// Ambient GH_TOKEN / GOLEMIC_* / RFAIL_* vars are NOT inherited.
func rfInvoke(t *testing.T, binary, workDir, binDir, homeDir string, extraEnv map[string]string) rfResult {
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
	return rfResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

// rfSetupSandbox creates:
//   - a local bare git repo (the "origin")
//   - a working git repo with origin pointing to the bare repo
//   - .golemic/guidelines/dev.md and .golemic/guidelines/reviewer.md (required by the runner's prompt renderer)
//
// prompts/dev.md and prompts/reviewer.md must exist next to the golemic binary
// (BR-003: never in the fixture repo).
//
// Returns (bareRepoPath, sandboxRepoPath).
func rfSetupSandbox(t *testing.T, realGit string) (string, string) {
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

	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatal(err)
	}
	run(bareRepo, realGit, "init", "--bare", bareRepo)

	run(workDir, realGit, "init")
	run(workDir, realGit, "config", "user.email", "test@example.com")
	run(workDir, realGit, "config", "user.name", "Test User")

	readme := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(readme, []byte("test repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(workDir, realGit, "add", "README.md")
	run(workDir, realGit, "commit", "-m", "init")

	run(workDir, realGit, "remote", "add", "origin", bareRepo)
	run(workDir, realGit, "push", "origin", "HEAD:refs/heads/main")
	run(workDir, realGit, "fetch", "origin")

	guidelinesDir := filepath.Join(workDir, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"),
		[]byte("# Guidelines\nFollow best practices.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "reviewer.md"),
		[]byte("# Guidelines\nFollow best practices.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	return bareRepo, workDir
}

// rfWriteConfig creates .golemic/config.json in the sandbox repo.
func rfWriteConfig(t *testing.T, repoDir string) {
	t.Helper()
	golemicDir := filepath.Join(repoDir, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
  "project": "golemic_e2e",
  "verify_command": "echo 'Verification passed'",
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

// rfWriteGitShim writes a git wrapper to binDir.
//
// The shim intercepts one command to enable hermetic testing:
//
//   - `git config --get remote.origin.url` — returns a fake HTTPS URL so the
//     preflight origin-URL check passes even though the real origin is a local
//     bare repo.
//
// All other git commands (fetch, worktree add, commit, push, rev-parse, …) are
// delegated to the real git binary so the full dev+reviewer worktree lifecycle
// works against the local bare repo without any network access.
func rfWriteGitShim(t *testing.T, binDir, realGit string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
REAL_GIT=%q

# Preflight HTTPS check: return a fake HTTPS URL for the origin-URL assertion.
if [ "$1" = "config" ] && [ "$2" = "--get" ] && [ "$3" = "remote.origin.url" ]; then
  echo "https://github.com/example/golemic_e2e.git"
  exit 0
fi

exec "$REAL_GIT" "$@"
`, realGit)
	rfWriteShim(t, filepath.Join(binDir, "git"), script)
}

// rfWriteGhShim writes a gh CLI shim to binDir.
//
// Handled subcommands:
//   - gh --version           → version string (preflight)
//   - gh issue view N …      → minimal JSON issue (runner issue load)
//   - gh pr list --head …    → empty JSON array (collision check)
//   - gh api graphql …       → always fails (simulates API/auth error for
//     token_invalid and submission_fails modes; never called for verify_fails)
func rfWriteGhShim(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "gh version 2.0.0"
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  printf '{"title":"Reviewer Failure Test Issue","body":"Automated reviewer failure E2E test.","state":"OPEN"}'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '[]'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "checks" ]; then
  printf '[]'
  exit 0
fi
if [ "$1" = "label" ] && [ "$2" = "list" ]; then
  printf '[{"name":"in-progress"},{"name":"needs-human"}]'
  exit 0
fi
if [ "$1" = "api" ] && [ "$2" = "graphql" ]; then
  case "${RFAIL_MODE}" in
    token_invalid)
      printf "error: HTTP 401: Credentials missing or invalid.\n" >&2
      exit 1
      ;;
    *)
      printf "error: HTTP 500: Internal Server Error.\n" >&2
      exit 1
      ;;
  esac
fi
printf "gh shim: unhandled command: %s\n" "$*" >&2
exit 1
`
	rfWriteShim(t, filepath.Join(binDir, "gh"), script)
}

// rfWritePiShim writes the pi agent shim to binDir.
//
// The shim distinguishes dev from reviewer invocation by inspecting its working
// directory: the dev worktree ends in "issue-<N>", the reviewer worktree in
// "issue-<N>-review".
//
// Dev role (always succeeds):
//  1. Creates a file, commits it, and pushes to origin.
//  2. Writes a pr_opened event to GOLEMIC_EVENT_LOG so the runner proceeds to
//     the reviewer step.
//
// Reviewer role (fails based on RFAIL_MODE):
//   - "verify_fails":    exits 1 immediately (verify_command check failed).
//   - "token_invalid":   calls `gh api graphql` → gh shim returns 401 → exits 1.
//   - "submission_fails": calls `gh api graphql` → gh shim returns 500 → exits 1.
//
// Uses only POSIX shell built-ins (no sed/awk/grep/jq); RFAIL_MODE is inherited
// from the golemic process environment via agent.RunRole's os.Environ() pass-through.
func rfWritePiShim(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "pi 1.0.0"
  exit 0
fi

# Determine role by checking if the CWD ends in "-review" (reviewer worktree)
# or not (dev worktree). Worktree paths are:
#   dev:      <golemicDir>/worktrees/issue-<N>
#   reviewer: <golemicDir>/worktrees/issue-<N>-review
case "$PWD" in
  *-review)
    # ---- Reviewer role ----
    case "${RFAIL_MODE}" in
      verify_fails)
        # AC-001 / BR-002: verify_command failed; no review submission attempted.
        exit 1
        ;;
      token_invalid)
        # AC-002 / BR-001: token invalid → GraphQL auth error (gh shim returns 401).
        gh api graphql -f query='mutation{__typename}' 2>&1 || true
        exit 1
        ;;
      submission_fails)
        # AC-003 / BR-001: GitHub API error → GraphQL fails (gh shim returns 500).
        gh api graphql -f query='mutation{__typename}' 2>&1 || true
        exit 1
        ;;
      *)
        printf "pi shim: RFAIL_MODE not set or unrecognised: %s\n" "${RFAIL_MODE}" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    # ---- Dev role: always succeed ----
    # Extract issue number from GOLEMIC_RUN_ID (format: issue-N-TIMESTAMP).
    # Uses only shell built-ins; sed is not available in the restricted PATH.
    RUN_ID="${GOLEMIC_RUN_ID:-0}"
    NO_PREFIX="${RUN_ID#issue-}"
    ISSUE_NUM="${NO_PREFIX%%-*}"
    BRANCH="golemic/issue-${ISSUE_NUM}"

    # Create a file and commit it so there is something to push.
    printf "dev e2e change\n" > .golemic-dev-change.txt
    git add .golemic-dev-change.txt
    git commit -m "dev: automated e2e change"

    # Push the branch to origin (the local bare repo; no auth needed for local paths).
    git push origin "HEAD:refs/heads/${BRANCH}"

    # Write pr_opened event so the runner proceeds to the reviewer step.
    # Format: {"type":"pr_opened","ts":"...","runId":"...","payload":{"prNumber":"42"}}
    printf '{"type":"pr_opened","ts":"2024-01-01T00:00:00Z","runId":"%s","payload":{"prNumber":"42"}}\n' \
      "${GOLEMIC_RUN_ID}" >> "${GOLEMIC_EVENT_LOG}"

    exit 0
    ;;
esac
`
	rfWriteShim(t, filepath.Join(binDir, "pi"), script)
}

func rfWriteShim(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}
