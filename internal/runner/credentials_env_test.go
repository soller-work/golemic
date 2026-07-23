package runner

import (
	"context"
	"strings"
	"testing"

	"golemic/internal/agent"
)

// TestRunnerEnv_CredTokensInjected_Dev verifies that GOLEMIC_DEV_TOKEN and
// GOLEMIC_REVIEWER_TOKEN are present in the dev agent subprocess environment.
func TestRunnerEnv_CredTokensInjected_Dev(t *testing.T) {
	t.Setenv("GOLEMIC_DEV_TOKEN", "ghp_dev_test_token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "ghp_rev_test_token")

	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	var capturedDevCfg agent.RoleConfig
	inner := makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil)
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if cfg.Role == "dev" {
			capturedDevCfg = cfg
		}
		return inner(ctx, cfg)
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	assertEnvContains(t, "dev", capturedDevCfg.Env, "GOLEMIC_DEV_TOKEN", "ghp_dev_test_token")
	assertEnvContains(t, "dev", capturedDevCfg.Env, "GOLEMIC_REVIEWER_TOKEN", "ghp_rev_test_token")
}

// TestRunnerEnv_CredTokensInjected_Reviewer verifies that GOLEMIC_DEV_TOKEN and
// GOLEMIC_REVIEWER_TOKEN are present in the reviewer agent subprocess environment.
func TestRunnerEnv_CredTokensInjected_Reviewer(t *testing.T) {
	t.Setenv("GOLEMIC_DEV_TOKEN", "ghp_dev_test_token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "ghp_rev_test_token")

	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	var capturedRevCfg agent.RoleConfig
	inner := makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil)
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if cfg.Role == "reviewer" {
			capturedRevCfg = cfg
		}
		return inner(ctx, cfg)
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	assertEnvContains(t, "reviewer", capturedRevCfg.Env, "GOLEMIC_DEV_TOKEN", "ghp_dev_test_token")
	assertEnvContains(t, "reviewer", capturedRevCfg.Env, "GOLEMIC_REVIEWER_TOKEN", "ghp_rev_test_token")
}

func assertEnvContains(t *testing.T, role string, env []string, key, wantVal string) {
	t.Helper()
	want := key + "=" + wantVal
	for _, e := range env {
		if e == want {
			return
		}
		if strings.HasPrefix(e, key+"=") {
			t.Errorf("role %q: %s=%q, want %q", role, key, strings.TrimPrefix(e, key+"="), wantVal)
			return
		}
	}
	t.Errorf("role %q: %s not found in Env %v", role, key, env)
}
