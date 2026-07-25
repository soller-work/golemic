package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// modelChainFakeExecutor records gh pr comment calls.
type modelChainFakeExecutor struct {
	ghPRCommentCalls []string
}

func (e *modelChainFakeExecutor) Run(name string, args ...string) (string, error) {
	return "", nil
}

func (e *modelChainFakeExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	return "", nil
}

func (e *modelChainFakeExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	return "", nil
}

func (e *modelChainFakeExecutor) RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error) {
	if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "comment" {
		body := ""
		for i, a := range args {
			if a == "--body" && i+1 < len(args) {
				body = args[i+1]
			}
		}
		e.ghPRCommentCalls = append(e.ghPRCommentCalls, body)
	}
	return "", nil
}

func chainExhaustedError(role string, models ...string) *agent.ModelChainExhaustedError {
	attempts := make([]agent.AttemptSummary, len(models))
	for i, m := range models {
		attempts[i] = agent.AttemptSummary{Model: m, Reason: "provider limit"}
	}
	return &agent.ModelChainExhaustedError{Role: role, Attempts: attempts}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRunDevAgent_ChainExhausted_NonZeroAgentCompleted verifies agent_completed
// is written with non-zero exit code when the dev model chain is exhausted.
func TestRunDevAgent_ChainExhausted_NonZeroAgentCompleted(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "dev")
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 1, fakeTranscriptPaths("/tmp", "dev"), chainExhaustedError("dev", "model-a", "model-b")
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome := r.runDevAgent(golemicDir, logPath, 5*time.Minute, "", 1)

	if outcome != outcomeDevFailed {
		t.Errorf("expected %q, got %q", outcomeDevFailed, outcome)
	}

	completedEvents := readAgentCompletedEvents(t, logPath)
	if len(completedEvents) != 1 {
		t.Fatalf("expected 1 agent_completed event, got %d", len(completedEvents))
	}
	var payload struct {
		ExitCode int `json:"exitCode"`
	}
	if err := json.Unmarshal(completedEvents[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.ExitCode == 0 {
		t.Errorf("agent_completed exitCode must be non-zero after exhausted chain")
	}

	if !strings.Contains(stderr.String(), "dev_failed") {
		t.Errorf("stderr should contain dev_failed diagnostic, got: %q", stderr.String())
	}
}

// TestRunReviewerAgent_ChainExhausted_NonZeroAgentCompleted mirrors the dev test.
func TestRunReviewerAgent_ChainExhausted_NonZeroAgentCompleted(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "reviewer")
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 1, fakeTranscriptPaths("/tmp", "reviewer"), chainExhaustedError("reviewer", "model-a", "model-b")
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome, _ := r.runReviewerAgent(golemicDir, logPath, 5*time.Minute, "", 1, 0, "", nil, "")

	if outcome != outcomeReviewFailed {
		t.Errorf("expected %q, got %q", outcomeReviewFailed, outcome)
	}

	completedEvents := readAgentCompletedEvents(t, logPath)
	if len(completedEvents) != 1 {
		t.Fatalf("expected 1 agent_completed event, got %d", len(completedEvents))
	}
	var payload struct {
		ExitCode int `json:"exitCode"`
	}
	if err := json.Unmarshal(completedEvents[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.ExitCode == 0 {
		t.Errorf("agent_completed exitCode must be non-zero after exhausted chain")
	}

	if !strings.Contains(stderr.String(), "review_failed") {
		t.Errorf("stderr should contain review_failed diagnostic, got: %q", stderr.String())
	}
}

// TestRunReviewerAgent_ChainExhausted_WithPR_PostsOneComment verifies BR-10.
func TestRunReviewerAgent_ChainExhausted_WithPR_PostsOneComment(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "reviewer")

	exec := &modelChainFakeExecutor{}
	r.executor = exec

	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 1, fakeTranscriptPaths("/tmp", "reviewer"), chainExhaustedError("reviewer", "model-a", "model-b")
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome, _ := r.runReviewerAgent(golemicDir, logPath, 5*time.Minute, "", 1, 0, "", nil, "")

	if outcome != outcomeReviewFailed {
		t.Errorf("expected %q, got %q", outcomeReviewFailed, outcome)
	}

	if len(exec.ghPRCommentCalls) != 1 {
		t.Fatalf("expected exactly 1 PR comment, got %d", len(exec.ghPRCommentCalls))
	}

	body := exec.ghPRCommentCalls[0]
	// Comment must mention models and failure category
	if !strings.Contains(body, "model-a") || !strings.Contains(body, "model-b") {
		t.Errorf("PR comment should mention attempted models, got: %q", body)
	}
	if !strings.Contains(body, "provider limit") {
		t.Errorf("PR comment should mention failure category, got: %q", body)
	}
	// No secrets
	for _, secret := range []string{"GH_TOKEN", "ghp_dev_test_token", "ghp_rev_test_token"} {
		if strings.Contains(body, secret) {
			t.Errorf("PR comment must not contain secret %q", secret)
		}
	}
}

// TestRunDevAgent_ChainExhausted_NoPR_NoComment verifies BR-10: no PR → no comment.
func TestRunDevAgent_ChainExhausted_NoPR_NoComment(t *testing.T) {
	// Use dev setup which has no pr_opened event in log
	r, logPath, _ := setupExitCodeRunner(t, "dev")

	exec := &modelChainFakeExecutor{}
	r.executor = exec

	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 1, fakeTranscriptPaths("/tmp", "dev"), chainExhaustedError("dev", "model-a", "model-b")
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	outcome := r.runDevAgent(golemicDir, logPath, 5*time.Minute, "", 1)

	if outcome != outcomeDevFailed {
		t.Errorf("expected %q, got %q", outcomeDevFailed, outcome)
	}

	if len(exec.ghPRCommentCalls) != 0 {
		t.Errorf("no PR comment should be posted when no PR exists, got %d calls", len(exec.ghPRCommentCalls))
	}
}

// TestRunDevAgent_ChainExhausted_DiagnosticsContainModels verifies BR-8.
func TestRunDevAgent_ChainExhausted_DiagnosticsContainModels(t *testing.T) {
	r, logPath, stderr := setupExitCodeRunner(t, "dev")
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 1, fakeTranscriptPaths("/tmp", "dev"), chainExhaustedError("dev", "model-a", "model-b")
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runDevAgent(golemicDir, logPath, 5*time.Minute, "", 1)

	msg := stderr.String()
	if !strings.Contains(msg, "model-a") || !strings.Contains(msg, "model-b") {
		t.Errorf("stderr should contain attempted model IDs, got: %q", msg)
	}
}

// TestRunDevAgent_ResolvesModelChainFromAgentFile verifies that a repo-level
// override file is used when present, and that its ordered model chain is preserved.
func TestRunDevAgent_ResolvesModelChainFromAgentFile(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "dev")

	agentsDir := filepath.Join(r.repoRoot, ".golemic", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	multiModelContent := "---\nmodel: model-a, model-b, model-c\n---\npersona body\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "dev.md"), []byte(multiModelContent), 0644); err != nil {
		t.Fatal(err)
	}

	var capturedCfg agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		capturedCfg = cfg
		return 0, fakeTranscriptPaths("/tmp", "dev"), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runDevAgent(golemicDir, logPath, 5*time.Minute, "", 1)

	expectedChain := "model-a, model-b, model-c"
	if capturedCfg.Model != expectedChain {
		t.Errorf("cfg.Model = %q, want %q (exact ordered chain not preserved)", capturedCfg.Model, expectedChain)
	}
}

// TestRunReviewerAgent_ResolvesModelChainFromAgentFile verifies that a repo-level
// override file is used when present, and that its ordered model chain is preserved.
func TestRunReviewerAgent_ResolvesModelChainFromAgentFile(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "reviewer")

	agentsDir := filepath.Join(r.repoRoot, ".golemic", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	multiModelContent := "---\nmodel: model-a, model-b, model-c\n---\npersona body\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.md"), []byte(multiModelContent), 0644); err != nil {
		t.Fatal(err)
	}

	var capturedCfg agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		capturedCfg = cfg
		return 0, fakeTranscriptPaths("/tmp", "reviewer"), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runReviewerAgent(golemicDir, logPath, 5*time.Minute, "", 1, 0, "", nil, "")

	expectedChain := "model-a, model-b, model-c"
	if capturedCfg.Model != expectedChain {
		t.Errorf("cfg.Model = %q, want %q (exact ordered chain not preserved)", capturedCfg.Model, expectedChain)
	}
}

// TestRunDevAgent_EmbeddedDefaultUsedWhenNoOverride verifies that when no repo
// override file exists, the embedded canonical persona is used and the model chain is non-empty.
func TestRunDevAgent_EmbeddedDefaultUsedWhenNoOverride(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "dev")

	var capturedCfg agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		capturedCfg = cfg
		return 0, fakeTranscriptPaths("/tmp", "dev"), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runDevAgent(golemicDir, logPath, 5*time.Minute, "", 1)

	if capturedCfg.Model == "" {
		t.Error("model chain must be non-empty when using embedded default")
	}
	if capturedCfg.SystemPromptFile == "" {
		t.Error("system prompt file must be set when using embedded default")
	}
}

// TestRunReviewerAgent_EmbeddedDefaultUsedWhenNoOverride verifies the same for the reviewer role.
func TestRunReviewerAgent_EmbeddedDefaultUsedWhenNoOverride(t *testing.T) {
	r, logPath, _ := setupExitCodeRunner(t, "reviewer")

	var capturedCfg agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		capturedCfg = cfg
		return 0, fakeTranscriptPaths("/tmp", "reviewer"), nil
	})

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	r.runReviewerAgent(golemicDir, logPath, 5*time.Minute, "", 1, 0, "", nil, "")

	if capturedCfg.Model == "" {
		t.Error("model chain must be non-empty when using embedded default")
	}
	if capturedCfg.SystemPromptFile == "" {
		t.Error("system prompt file must be set when using embedded default")
	}
}
