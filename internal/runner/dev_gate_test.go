package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/gmbroker"
)

func gateTestDevDoneParams() map[string]any {
	return map[string]any{
		"summary":   "Implement the feature",
		"commitMsg": "feat(test): implement feature (42)",
		"prTitle":   "feat: implement feature",
		"prBody":    "Closes #42",
	}
}

func gateTestCallGMTool(env []string, tool, callID string, params any) map[string]any {
	sockPath := gmSockFromEnv(env)
	if sockPath == "" {
		return nil
	}
	for i := 0; i < 50; i++ {
		if result := callGMTool(sockPath, tool, callID, params); result != nil {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

func gateTestAgent(t *testing.T, promptCapture *[]string, callGMProjectCheck bool, callGMDevDone bool) func(context.Context, agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	return func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if cfg.Role != "dev" {
			t.Fatalf("unexpected role %q", cfg.Role)
		}
		if promptCapture != nil {
			*promptCapture = append(*promptCapture, cfg.UserPrompt)
		}
		if callGMProjectCheck {
			result := gateTestCallGMTool(cfg.Env, "gm_project_check", "test-check", map[string]any{})
			if result == nil {
				t.Fatalf("gm_project_check did not respond")
			}
			if ok, _ := result["ok"].(bool); !ok {
				t.Fatalf("gm_project_check was rejected: %v", result)
			}
		}
		if callGMDevDone {
			result := gateTestCallGMTool(cfg.Env, "gm_dev_done", "test-done", gateTestDevDoneParams())
			if result == nil {
				t.Fatalf("gm_dev_done did not respond")
			}
		}
		return 0, agent.TranscriptPaths{}, nil
	}
}

func gateTestExecCalls(exec *fakeExecutor) string {
	parts := make([]string, 0, len(exec.calls))
	for _, c := range exec.calls {
		parts = append(parts, c.name+" "+strings.Join(c.args, " "))
	}
	return strings.Join(parts, "\n")
}

func gateTestCountCall(exec *fakeExecutor, name string) int {
	count := 0
	for _, c := range exec.calls {
		if c.name == name {
			count++
		}
	}
	return count
}

func worktreeFingerprintForTest(path string) (string, error) {
	if _, err := os.Stat(filepath.Join(path, "post-terminal.txt")); err == nil {
		return "present", nil
	} else if os.IsNotExist(err) {
		return "absent", nil
	} else {
		return "", err
	}
}

func gateTestReadPROpenedCount(t *testing.T, logPath string) int {
	t.Helper()
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == eventlog.EventPROpened {
			count++
		}
	}
	return count
}

func TestRunDevAgent_GateAccepted_CommitsPushesOpensPR_WritesEvent_AC001(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	r.SetRunAgentFn(gateTestAgent(t, nil, true, true))

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}

	calls := gateTestExecCalls(exec)
	for _, want := range []string{
		"git add -A",
		"git commit -m feat(test): implement feature (42)",
		"git push --set-upstream origin golemic/issue-42",
		"gh pr create --title feat: implement feature --body Closes #42",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing command %q in calls:\n%s", want, calls)
		}
	}
	if got := gateTestReadPROpenedCount(t, logPath); got != 1 {
		t.Fatalf("expected 1 pr_opened event, got %d", got)
	}
}

func TestRunDevAgent_GateRejected_NoPriorCheck_RestartsAndNoSideEffects_AC002(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	var prompts []string
	r.SetRunAgentFn(gateTestAgent(t, &prompts, false, true))

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	if got := gateTestCountCall(exec, "git"); got != 0 {
		t.Fatalf("expected no git side effects, got %d git calls", got)
	}
	if got := gateTestCountCall(exec, "gh"); got != 0 {
		t.Fatalf("expected no gh side effects, got %d gh calls", got)
	}
	if got := gateTestReadPROpenedCount(t, logPath); got != 0 {
		t.Fatalf("expected no pr_opened event, got %d", got)
	}
}

func TestRunDevAgent_GateRejected_TreeMutated_RestartsAndNoSideEffects_AC003(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-before"}, nil
		},
		func(string) (string, error) { return "fp-after", nil },
	)
	r.SetRunAgentFn(gateTestAgent(t, nil, true, true))

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	if got := gateTestCountCall(exec, "git"); got != 0 {
		t.Fatalf("expected no git side effects, got %d git calls", got)
	}
	if got := gateTestCountCall(exec, "gh"); got != 0 {
		t.Fatalf("expected no gh side effects, got %d gh calls", got)
	}
}

func TestRunDevAgent_GateRejected_LastCheckRed_RestartsAndNoSideEffects_AC004(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	var checkCount int
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			checkCount++
			if checkCount == 1 {
				return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
			}
			return &gmbroker.ProjectCheckResult{OK: false, WorkingTreeFingerprint: "fp-red"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if cfg.Role != "dev" {
			t.Fatalf("unexpected role %q", cfg.Role)
		}
		if !sendGMProjectCheck(cfg.Env) {
			t.Fatalf("first gm_project_check was rejected")
		}
		sendGMProjectCheck(cfg.Env)
		if !sendGMDevDone(cfg.Env) {
			return 0, agent.TranscriptPaths{}, nil
		}
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	if got := gateTestCountCall(exec, "git"); got != 0 {
		t.Fatalf("expected no git side effects, got %d git calls", got)
	}
}

func TestRunDevAgent_GateRetryBound_ThreeInvocationsThenDevFailed_AC005(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	var prompts []string
	r.SetRunAgentFn(gateTestAgent(t, &prompts, false, true))

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	if got := gateTestCountCall(exec, "git"); got != 0 {
		t.Fatalf("expected no git side effects, got %d git calls", got)
	}
}

func TestRunDevAgent_GMBrokerFailureFailsClosed_AC007(t *testing.T) {
	t.Run("missing creds", func(t *testing.T) {
		exec := pingPongExecutor(false, nil)
		r, _ := setupGMRunner(t)
		r.executor = exec
		r.creds = nil
		golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
		logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
		called := false
		r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
			called = true
			return 0, agent.TranscriptPaths{}, nil
		})
		if outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1); outcome != outcomeDevFailed {
			t.Fatalf("expected dev_failed, got %q", outcome)
		}
		if called {
			t.Fatal("agent must not run when GM broker is unavailable")
		}
	})

	t.Run("broker startup error", func(t *testing.T) {
		exec := pingPongExecutor(false, nil)
		r, _ := setupGMRunner(t)
		r.executor = exec
		golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
		logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
		orig := startGMBrokerFn
		t.Cleanup(func() { startGMBrokerFn = orig })
		startGMBrokerFn = func(string, int, string) (*gmbroker.Broker, error) {
			return nil, fmt.Errorf("broker boom")
		}
		called := false
		r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
			called = true
			return 0, agent.TranscriptPaths{}, nil
		})
		if outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1); outcome != outcomeDevFailed {
			t.Fatalf("expected dev_failed, got %q", outcome)
		}
		if called {
			t.Fatal("agent must not run when GM broker startup fails")
		}
	})
}

func TestRunDevAgent_PostTerminalMutationAfterAcceptedFailsClosed_AC008(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	orig := startGMBrokerFn
	t.Cleanup(func() { startGMBrokerFn = orig })
	startGMBrokerFn = func(sockPath string, _ int, _ string) (*gmbroker.Broker, error) {
		b, err := gmbroker.StartWithFetcherAndProjectCheck(
			sockPath,
			func(_ context.Context) (string, error) { return "fake spec", nil },
			gmbroker.ProjectCheckConfig{WorktreePath: filepath.Join(r.homeDir, ".golemic", r.project, "worktrees", "issue-42")},
			[]string{"gm_slice_get", "gm_project_check", "gm_dev_done"},
		)
		if err != nil {
			return nil, err
		}
		b.SetProjectCheckFn(func(cfg gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			fp, err := worktreeFingerprintForTest(cfg.WorktreePath)
			if err != nil {
				return nil, err
			}
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: fp}, nil
		})
		b.SetComputeFingerprintFn(func(path string) (string, error) {
			return worktreeFingerprintForTest(path)
		})
		return b, nil
	}
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if cfg.Role != "dev" {
			t.Fatalf("unexpected role %q", cfg.Role)
		}
		if !sendGMProjectCheck(cfg.Env) {
			t.Fatal("gm_project_check was rejected")
		}
		if !sendGMDevDone(cfg.Env) {
			t.Fatal("gm_dev_done was rejected")
		}
		if err := os.WriteFile(filepath.Join(cfg.WorktreeDir, "post-terminal.txt"), []byte("mutated"), 0644); err != nil {
			t.Fatalf("write post-terminal file: %v", err)
		}
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	if got := gateTestCountCall(exec, "git"); got != 0 {
		t.Fatalf("expected no git side effects, got %d git calls", got)
	}
	if got := gateTestCountCall(exec, "gh"); got != 0 {
		t.Fatalf("expected no gh side effects, got %d gh calls", got)
	}
}

func TestRunDevAgent_TerminalSchemaFailureReportsSchemaError_AC009(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	r.stderr = &bytes.Buffer{}
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if cfg.Role != "dev" {
			t.Fatalf("unexpected role %q", cfg.Role)
		}
		result := gateTestCallGMTool(cfg.Env, "gm_dev_done", "test-done", map[string]any{
			"summary":   "Implement the feature",
			"commitMsg": "feat(test): implement feature (42)",
			"prTitle":   "feat: implement feature",
		})
		if result == nil {
			t.Fatal("gm_dev_done did not respond")
		}
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := r.runDevAgent(golemicDir, logPath, 30*time.Second, "", 1)
	if outcome != outcomeDevFailed {
		t.Fatalf("expected dev_failed, got %q", outcome)
	}
	stderr := r.stderr.(*bytes.Buffer).String()
	if !strings.Contains(stderr, "gm_dev_done: prBody is required") {
		t.Fatalf("expected schema error in stderr, got %q", stderr)
	}
	if strings.Contains(stderr, "did not call gm_dev_done") {
		t.Fatalf("expected schema failure, got generic missing-call message: %q", stderr)
	}
	if got := gateTestCountCall(exec, "git"); got != 0 {
		t.Fatalf("expected no git side effects, got %d git calls", got)
	}
}

func TestRunDevRetryAgent_ExistingPROpened_CommitsPushesNoSecondPR_AC006(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, _ := setupGMRunner(t)
	r.executor = exec
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	logPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")
	writePROpenedEvent(t, logPath, 99)
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-ok"}, nil
		},
		func(string) (string, error) { return "fp-ok", nil },
	)
	r.SetRunAgentFn(gateTestAgent(t, nil, true, true))

	outcome := r.runDevRetryAgent(golemicDir, logPath, 30*time.Second, "Fix the typo", "", "", 1)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}

	calls := gateTestExecCalls(exec)
	if !strings.Contains(calls, "git push --force-with-lease") {
		t.Fatalf("expected retry push, got calls:\n%s", calls)
	}
	if strings.Contains(calls, "gh pr create") {
		t.Fatalf("expected no second PR creation, got calls:\n%s", calls)
	}
	if got := gateTestReadPROpenedCount(t, logPath); got != 1 {
		t.Fatalf("expected one pr_opened event, got %d", got)
	}
}
