package runner

import (
	"context"
	"strings"
	"testing"

	"golemic/internal/agent"
)

// TestRunnerEnv_CredTokensNotInjected_Dev verifies that the dev agent RoleConfig
// does not carry GitHub credential env vars.
func TestRunnerEnv_CredTokensNotInjected_Dev(t *testing.T) {
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

	joinedEnv := strings.Join(capturedDevCfg.Env, "\n")
	for _, banned := range []string{"GH_TOKEN=", "GOLEMIC_DEV_TOKEN=", "GOLEMIC_REVIEWER_TOKEN="} {
		if strings.Contains(joinedEnv, banned) {
			t.Errorf("dev agent env unexpectedly contains %q: %v", banned, capturedDevCfg.Env)
		}
	}
}

// TestRunnerEnv_CredTokensNotInjected_Reviewer verifies that the reviewer agent
// RoleConfig does not carry GitHub credential env vars.
func TestRunnerEnv_CredTokensNotInjected_Reviewer(t *testing.T) {
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

	joinedEnv := strings.Join(capturedRevCfg.Env, "\n")
	for _, banned := range []string{"GH_TOKEN=", "GOLEMIC_DEV_TOKEN=", "GOLEMIC_REVIEWER_TOKEN="} {
		if strings.Contains(joinedEnv, banned) {
			t.Errorf("reviewer agent env unexpectedly contains %q: %v", banned, capturedRevCfg.Env)
		}
	}
}
