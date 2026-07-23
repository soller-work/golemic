package runner

import (
	"context"
	"testing"

	"golemic/internal/agent"
)

// TestRunnerEnv_CredTokensInjected_Dev verifies that DevToken and ReviewerToken
// are set in the dev agent RoleConfig so newPiCmd injects them as GOLEMIC_*_TOKEN env vars.
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

	if capturedDevCfg.DevToken != "ghp_dev_test_token" {
		t.Errorf("dev RoleConfig.DevToken = %q, want %q", capturedDevCfg.DevToken, "ghp_dev_test_token")
	}
	if capturedDevCfg.ReviewerToken != "ghp_rev_test_token" {
		t.Errorf("dev RoleConfig.ReviewerToken = %q, want %q", capturedDevCfg.ReviewerToken, "ghp_rev_test_token")
	}
}

// TestRunnerEnv_CredTokensInjected_Reviewer verifies that DevToken and ReviewerToken
// are set in the reviewer agent RoleConfig so newPiCmd injects them as GOLEMIC_*_TOKEN env vars.
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

	if capturedRevCfg.DevToken != "ghp_dev_test_token" {
		t.Errorf("reviewer RoleConfig.DevToken = %q, want %q", capturedRevCfg.DevToken, "ghp_dev_test_token")
	}
	if capturedRevCfg.ReviewerToken != "ghp_rev_test_token" {
		t.Errorf("reviewer RoleConfig.ReviewerToken = %q, want %q", capturedRevCfg.ReviewerToken, "ghp_rev_test_token")
	}
}
