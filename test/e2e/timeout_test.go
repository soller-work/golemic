package e2e

// AC→test mapping (spec 007_e2e-test-timeout-scenarios):
//
//	AC-001 "Dev role timeout"      → TestTimeout/dev_role_timeout
//	AC-002 "Reviewer role timeout" → TestTimeout/reviewer_role_timeout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/eventlog"
)

// TestTimeout verifies that role timeouts abort golemic with exit 1 and
// outcome=timeout. Uses timeout_seconds: 2 in config (approach 2) so each
// subtest completes in ≤ ~5s rather than the 60s that timeout_minutes: 1 would
// require.
//
// Business rules verified:
//
//	BR-001: dev exceeds timeout → exit 1, outcome=timeout
//	BR-002: reviewer exceeds timeout → exit 1, outcome=timeout
func TestTimeout(t *testing.T) {
	binary := findBinary()
	if binary == "" {
		t.Skip("golemic binary not found — run `go build ./cmd/golemic` or set GOLEMIC_BINARY")
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available in test environment")
	}

	tests := []struct {
		name         string
		timeoutMode  string
		wantStderr   string
		wantPROpened bool
	}{
		{
			// AC-001 / BR-001: dev agent hangs → dev timeout fires.
			name:         "dev_role_timeout",
			timeoutMode:  "dev_hangs",
			wantStderr:   "dev_failed: dev agent exceeded timeout",
			wantPROpened: false,
		},
		{
			// AC-002 / BR-002: dev succeeds, reviewer hangs → reviewer timeout fires.
			name:         "reviewer_role_timeout",
			timeoutMode:  "reviewer_hangs",
			wantStderr:   "review_failed: reviewer agent exceeded timeout",
			wantPROpened: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			bareRepo, sandbox := toSetupSandbox(t, realGit)
			binDir := t.TempDir()
			tempHome := t.TempDir()

			toWriteConfig(t, sandbox)
			toWriteGitShim(t, binDir, realGit)
			toWriteGhShim(t, binDir)
			toWritePiShim(t, binDir)

			env := map[string]string{
				"GOLEMIC_DEV_TOKEN":      "fake-dev-token",
				"GOLEMIC_REVIEWER_TOKEN": "fake-reviewer-token",
				"TIMEOUT_MODE":           tc.timeoutMode,
				"TIMEOUT_BARE_REPO":      bareRepo,
			}
			result := toInvoke(t, binary, sandbox, binDir, tempHome, env)

			// BR-001/BR-002: timeout must abort with exit 1.
			if result.exitCode != 1 {
				t.Errorf("want exit 1, got %d\nstdout: %s\nstderr: %s",
					result.exitCode, result.stdout, result.stderr)
			}

			// Assert correct role's timeout message in stderr.
			if !strings.Contains(result.stderr, tc.wantStderr) {
				t.Errorf("want %q in stderr, got:\n%s", tc.wantStderr, result.stderr)
			}

			// Assert outcome=timeout in run_finished event (authoritative).
			runID := parseRunID(result.stdout)
			if runID == "" {
				t.Fatalf("could not extract run ID from stdout\nstdout: %s\nstderr: %s",
					result.stdout, result.stderr)
			}
			eventsPath := filepath.Join(tempHome, ".golemic", "golemic_e2e", "runs", runID, "events.jsonl")
			outcome := toOutcomeFromEvents(t, eventsPath)
			if outcome != "timeout" {
				t.Errorf("want outcome=timeout in run_finished event, got %q", outcome)
			}

			// AC-002: pr_opened must exist when dev succeeded before reviewer timeout.
			// AC-001: pr_opened must not exist when dev itself timed out.
			if tc.wantPROpened && !hasEvent(eventsPath, "pr_opened") {
				t.Errorf("want pr_opened event (dev succeeded before reviewer timeout), not found in %s", eventsPath)
			}
			if !tc.wantPROpened && hasEvent(eventsPath, "pr_opened") {
				t.Errorf("want no pr_opened event (dev timed out before push), but found one in %s", eventsPath)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers (to- prefix to avoid collisions with helpers in other e2e tests)
// ---------------------------------------------------------------------------

type toResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func toInvoke(t *testing.T, binary, workDir, binDir, homeDir string, extraEnv map[string]string) toResult {
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
	return toResult{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		exitCode: exitCode,
	}
}

// toSetupSandbox creates a hermetic git sandbox with a local bare origin.
// prompts/dev.md and prompts/reviewer.md must exist next to the golemic binary
// (BR-003: never in the fixture repo).
func toSetupSandbox(t *testing.T, realGit string) (bareRepo, workDir string) {
	t.Helper()

	bareParent := t.TempDir()
	bareRepo = filepath.Join(bareParent, "repo.git")
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

// toWriteConfig creates .golemic/config.json with timeout_seconds: 2 so the
// test timeout fires within ~2s rather than waiting the default 30 minutes.
func toWriteConfig(t *testing.T, repoDir string) {
	t.Helper()
	golemicDir := filepath.Join(repoDir, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
  "project": "golemic_e2e",
  "verify_command": "echo ok",
  "label": "ready-for-agent",
  "timeout_seconds": 2,
  "models": {
    "dev": "test/dev-model",
    "reviewer": "test/reviewer-model"
  }
}`
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
}

// toWriteGitShim returns a fake HTTPS URL for preflight and delegates everything
// else to the real git binary so worktree/push/fetch work against the local bare repo.
func toWriteGitShim(t *testing.T, binDir, realGit string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
REAL_GIT=%q

if [ "$1" = "config" ] && [ "$2" = "--get" ] && [ "$3" = "remote.origin.url" ]; then
  echo "https://github.com/example/golemic_e2e.git"
  exit 0
fi

exec "$REAL_GIT" "$@"
`, realGit)
	toWriteShim(t, filepath.Join(binDir, "git"), script)
}

func toWriteGhShim(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "gh version 2.0.0"
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  printf '{"title":"Timeout Test Issue","body":"Automated timeout E2E test."}'
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
printf "gh shim: unhandled command: %s\n" "$*" >&2
exit 1
`
	toWriteShim(t, filepath.Join(binDir, "gh"), script)
}

// toWritePiShim writes the pi shim.
//
// TIMEOUT_MODE controls behaviour:
//   - "dev_hangs":      pi always sleeps; runner kills it when the 2s timeout fires (AC-001).
//   - "reviewer_hangs": dev role succeeds (commit + push + pr_opened event); reviewer
//     role sleeps until killed (AC-002).
func toWritePiShim(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "pi 1.0.0"
  exit 0
fi

case "${TIMEOUT_MODE}" in
  dev_hangs)
    # AC-001: dev times out; busy-wait using only shell builtins until the
    # runner kills the process group (sleep is not in the restricted PATH).
    while :; do :; done
    ;;
  reviewer_hangs)
    # AC-002: dev succeeds; reviewer hangs.
    case "$PWD" in
      *-review)
        # Reviewer worktree: busy-wait until timeout kills the process group.
        while :; do :; done
        ;;
      *)
        # Dev worktree: succeed by committing, pushing, and writing pr_opened event.
        RUN_ID="${GOLEMIC_RUN_ID:-0}"
        NO_PREFIX="${RUN_ID#issue-}"
        ISSUE_NUM="${NO_PREFIX%%-*}"
        BRANCH="golemic/issue-${ISSUE_NUM}"

        printf "dev e2e change\n" > .golemic-dev-change.txt
        git add .golemic-dev-change.txt
        git commit -m "dev: e2e timeout test change"
        git push origin "HEAD:refs/heads/${BRANCH}"

        printf '{"type":"pr_opened","ts":"2024-01-01T00:00:00Z","runId":"%s","payload":{"prNumber":"42"}}\n' \
          "${GOLEMIC_RUN_ID}" >> "${GOLEMIC_EVENT_LOG}"
        exit 0
        ;;
    esac
    ;;
  *)
    printf "pi shim: TIMEOUT_MODE not set or unrecognised: %s\n" "${TIMEOUT_MODE}" >&2
    exit 1
    ;;
esac
`
	toWriteShim(t, filepath.Join(binDir, "pi"), script)
}

func toWriteShim(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

// toOutcomeFromEvents reads the last run_finished event from the event log and
// returns its outcome field. Returns empty string if the event is not found.
func toOutcomeFromEvents(t *testing.T, eventsPath string) string {
	t.Helper()
	events, err := eventlog.Reader{}.Read(eventsPath)
	if err != nil {
		t.Logf("toOutcomeFromEvents: could not read %s: %v", eventsPath, err)
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != eventlog.EventRunFinished {
			continue
		}
		var payload struct {
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil {
			t.Logf("toOutcomeFromEvents: could not parse run_finished payload: %v", err)
			continue
		}
		return payload.Outcome
	}
	return ""
}
