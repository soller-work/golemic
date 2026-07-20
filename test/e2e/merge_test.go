//go:build e2e

// Package e2e — merge phase harness tests.
//
// These tests exercise the auto-merge gate, skip path, and rebase-conflict
// failure path with hermetic sandboxes: a local bare repo as origin,
// script shims for git/gh/pi, no real GitHub access.
//
// AC→test mapping (issue-15 spec):
//
//	AC-001 (gate proceeds → rebase → CI green → pr_merged) → TestMergePhase/happy_path
//	AC-003 (confidence:low → automerge_skipped) → TestMergePhase/skip_risk_high
//	AC-006 (rebase conflict → automerge_failed) → TestMergePhase/rebase_conflict
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMergePhase verifies the deterministic merge-phase outcomes using hermetic
// script shims. Each subtest shares the same sandbox setup (local bare repo +
// shim binaries) but injects a different MERGE_MODE to control gh/git/pi behaviour.
func TestMergePhase(t *testing.T) {
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
		mergeMode string // MERGE_MODE env var controlling shim behaviour
		// expected assertions
		wantExitCode      int
		wantEvent         string // event type that must appear in the log
		wantNoEvent       string // event type that must NOT appear
		wantDevWTRemoved  bool   // assert dev worktree cleaned up after success
	}{
		{
			// AC-001: gate proceeds (risk:low + confidence high), branch stale,
			// rebase succeeds, CI green, pr_merged event, exit 0, dev worktree removed.
			name:             "happy_path",
			mergeMode:        "happy_path",
			wantExitCode:     0,
			wantEvent:        "pr_merged",
			wantNoEvent:      "automerge_failed",
			wantDevWTRemoved: true,
		},
		{
			// AC-003: mergeConfidence=low → gate skips auto-merge.
			// Run succeeds (exit 0); automerge_skipped event present; no pr_merged.
			name:         "skip_risk_high",
			mergeMode:    "skip_risk_high",
			wantExitCode: 0,
			wantEvent:    "automerge_skipped",
			wantNoEvent:  "pr_merged",
		},
		{
			// AC-006 / BR-005: gate passes (risk:low + confidence high) but git
			// rebase origin/main exits non-zero → automerge_failed, exit 1.
			name:         "rebase_conflict",
			mergeMode:    "rebase_conflict",
			wantExitCode: 1,
			wantEvent:    "automerge_failed",
			wantNoEvent:  "pr_merged",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			bareRepo, sandbox := mpSetupSandbox(t, realGit)
			binDir := t.TempDir()
			tempHome := t.TempDir()

			mpWriteConfig(t, sandbox)
			mpWriteGitShim(t, binDir, realGit)
			mpWriteGhShim(t, binDir)
			mpWritePiShim(t, binDir, bareRepo)

			env := map[string]string{
				"GOLEMIC_DEV_TOKEN":      "fake-dev-token",
				"GOLEMIC_REVIEWER_TOKEN": "fake-reviewer-token",
				"MERGE_MODE":             tc.mergeMode,
				"MERGE_BARE_REPO":        bareRepo,
			}
			result := mpInvoke(t, binary, sandbox, binDir, tempHome, env)

			if result.exitCode != tc.wantExitCode {
				t.Errorf("exit code: got %d, want %d\nstdout: %s\nstderr: %s",
					result.exitCode, tc.wantExitCode, result.stdout, result.stderr)
			}

			runID := parseRunID(result.stdout)
			if runID == "" {
				t.Fatalf("could not extract run ID from stdout\nstdout: %s\nstderr: %s",
					result.stdout, result.stderr)
			}
			eventsPath := filepath.Join(tempHome, ".golemic", "golemic_e2e", "runs", runID, "events.jsonl")

			if tc.wantEvent != "" && !hasEvent(eventsPath, tc.wantEvent) {
				t.Errorf("want %q event in %s, not found\nstdout: %s\nstderr: %s", tc.wantEvent, eventsPath, result.stdout, result.stderr)
			}
			if tc.wantNoEvent != "" && hasEvent(eventsPath, tc.wantNoEvent) {
				t.Errorf("must NOT have %q event in %s, but found one", tc.wantNoEvent, eventsPath)
			}
			if tc.wantDevWTRemoved {
				devWTPath := filepath.Join(tempHome, ".golemic", "golemic_e2e", "worktrees", "issue-1")
				if _, err := os.Stat(devWTPath); !os.IsNotExist(err) {
					t.Errorf("dev worktree should be removed after successful merge, still exists: %s", devWTPath)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers (mp- prefix to avoid collisions with other e2e helpers)
// ---------------------------------------------------------------------------

type mpResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func mpInvoke(t *testing.T, binary, workDir, binDir, homeDir string, extraEnv map[string]string) mpResult {
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
	return mpResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

// mpSetupSandbox creates a local bare git repo and a working repo with origin
// pointing to it, plus the required .golemic/guidelines files.
func mpSetupSandbox(t *testing.T, realGit string) (bareRepo, workDir string) {
	t.Helper()

	bareRepo = filepath.Join(t.TempDir(), "repo.git")
	workDir = t.TempDir()

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
	for _, f := range []string{"dev.md", "reviewer.md"} {
		if err := os.WriteFile(filepath.Join(guidelinesDir, f),
			[]byte("# Guidelines\nFollow best practices.\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return bareRepo, workDir
}

// mpWriteConfig writes .golemic/config.json in the sandbox repo.
func mpWriteConfig(t *testing.T, repoDir string) {
	t.Helper()
	golemicDir := filepath.Join(repoDir, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
  "project": "golemic_e2e",
  "verify_command": "echo 'ok'",
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

// mpWriteGitShim writes a git wrapper that:
//   - fakes the remote origin URL so the preflight HTTPS check passes.
//   - in "rebase_conflict" mode: makes the freshness check appear stale (so the
//     runner enters the rebase path) and then makes `git rebase` exit non-zero
//     so the runner falls into the BR-005 conflict path.
//   - delegates everything else to the real git binary.
func mpWriteGitShim(t *testing.T, binDir, realGit string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
REAL_GIT=%q

if [ "$1" = "config" ] && [ "$2" = "--get" ] && [ "$3" = "remote.origin.url" ]; then
  echo "https://github.com/example/golemic_e2e.git"
  exit 0
fi

if [ "${MERGE_MODE}" = "happy_path" ]; then
  # Report branch as stale so the runner enters the rebase path (BR-003).
  if [ "$1" = "merge-base" ] && [ "$2" = "--is-ancestor" ]; then
    exit 1  # non-ancestor = branch is behind origin/main
  fi
  # Intercept push --force-with-lease: avoid needing remote tracking configured.
  if [ "$1" = "push" ] && [ "$2" = "--force-with-lease" ]; then
    exit 0
  fi
fi

if [ "${MERGE_MODE}" = "rebase_conflict" ]; then
  # Report branch as stale so the runner enters the rebase path (BR-003).
  if [ "$1" = "merge-base" ] && [ "$2" = "--is-ancestor" ]; then
    exit 1  # non-ancestor = branch is behind origin/main
  fi
  # Fail the actual rebase to exercise BR-005.
  if [ "$1" = "rebase" ] && [ "$2" != "--abort" ]; then
    printf "CONFLICT (content): Merge conflict in README.md\n" >&2
    exit 1
  fi
fi

exec "$REAL_GIT" "$@"
`, realGit)
	mpWriteShim(t, filepath.Join(binDir, "git"), script)
}

// mpWriteGhShim handles the gh CLI calls made by the runner during the merge phase.
//
// MERGE_MODE controls shim behaviour. The issue label returned by `gh issue view`
// is always plain (no risk label) because the gate no longer considers risk labels.
// The skip_risk_high scenario injects mergeConfidence=low via the pi shim instead.
//
// All other gh calls (pr list, pr checks, pr review, pr merge, pr comment)
// are handled with minimal success responses.
func mpWriteGhShim(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "gh version 2.0.0"
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  printf '{"title":"Merge Phase Test","body":"Test issue.","state":"OPEN","labels":[]}'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '[]'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "checks" ]; then
  case "${MERGE_MODE}" in
    happy_path)
      # Return a passing check so the runner takes the CI-present path and merges.
      printf '[{"name":"build","bucket":"pass","link":""}]'
      ;;
    *)
      printf '[]'
      ;;
  esac
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "review" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "comment" ]; then
  exit 0
fi
if [ "$1" = "label" ] && [ "$2" = "list" ]; then
  printf '[{"name":"in-progress"},{"name":"needs-human"}]'
  exit 0
fi
printf "gh shim: unhandled command: %s\n" "$*" >&2
exit 1
`
	mpWriteShim(t, filepath.Join(binDir, "gh"), script)
}

// mpWritePiShim writes a pi agent shim that:
//   - Dev role: commits a file, pushes the branch, writes pr_opened event.
//   - Reviewer role: writes a review_submitted event with verdict=approved and
//     mergeConfidence=high, then calls golemic submit-review so the runner records it.
//
// The shim distinguishes dev from reviewer by checking whether the CWD ends in "-review".
func mpWritePiShim(t *testing.T, binDir, bareRepo string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "pi 1.0.0"
  exit 0
fi

case "$PWD" in
  *-review)
    # ---- Reviewer role: approve; confidence=low for skip_risk_high, else high ----
    CONFIDENCE="high"
    if [ "${MERGE_MODE}" = "skip_risk_high" ]; then
      CONFIDENCE="low"
    fi
    printf '{"type":"review_submitted","ts":"2024-01-01T00:00:00Z","runId":"%s","payload":{"verdict":"approved","mergeConfidence":"%s","body":"LGTM"}}\n' \
      "${GOLEMIC_RUN_ID}" "${CONFIDENCE}" >> "${GOLEMIC_EVENT_LOG}"
    exit 0
    ;;
  *)
    # ---- Dev role: create branch, commit, push, write pr_opened event ----
    RUN_ID="${GOLEMIC_RUN_ID:-0}"
    NO_PREFIX="${RUN_ID#issue-}"
    ISSUE_NUM="${NO_PREFIX%%-*}"
    BRANCH="golemic/issue-${ISSUE_NUM}"

    printf "dev e2e change\n" > .golemic-dev-change.txt
    git add .golemic-dev-change.txt
    git commit -m "dev: automated e2e change"
    git push origin "HEAD:refs/heads/${BRANCH}"

    printf '{"type":"pr_opened","ts":"2024-01-01T00:00:00Z","runId":"%s","payload":{"prNumber":"1"}}\n' \
      "${GOLEMIC_RUN_ID}" >> "${GOLEMIC_EVENT_LOG}"
    exit 0
    ;;
esac
`
	_ = bareRepo // available to the git shim via MERGE_BARE_REPO if needed
	mpWriteShim(t, filepath.Join(binDir, "pi"), script)
}

func mpWriteShim(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}
