package runner

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/gmbroker"
)

// setupGMRunner creates a minimal runner for GM broker tests.
func setupGMRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)

	golemicDir := filepath.Join(repoRoot, ".golemic")
	for _, dir := range []string{
		filepath.Join(golemicDir, "guidelines"),
		filepath.Join(golemicDir, "agents"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"dev.md", "reviewer.md"} {
		if err := os.WriteFile(filepath.Join(golemicDir, "guidelines", name), []byte("# guidelines"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(golemicDir, "agents", name), []byte("---\nmodel: test/model\n---\npersona\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	// Use /tmp as homeDir so that socket paths stay within the 104-byte unix
	// socket limit on macOS. Directories created under /tmp/.golemic/<project>
	// are cleaned up explicitly in t.Cleanup.
	const shortProject = "gmrp"
	shortHome := "/tmp"
	shortRunID := "gm42t"
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(shortHome, ".golemic", shortProject)) //nolint:errcheck
	})

	r := New(nil, shortHome, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = shortProject
	r.homeDir = shortHome
	r.runID = shortRunID
	r.branchName = "golemic/issue-42"
	r.creds = creds
	r.cfg = &config.Config{
		VerifyCommand:  "go test",
		TimeoutMinutes: 30,
	}
	r.issue = &issueData{Number: 42, Title: "GM test issue"}
	r.turnCounter = 1

	devWT := filepath.Join(r.homeDir, ".golemic", shortProject, "worktrees", "issue-42")
	if err := os.MkdirAll(devWT, 0755); err != nil {
		t.Fatal(err)
	}

	return r, filepath.Join(repoRoot, ".golemic")
}

// injectFakeGMBroker overrides startGMBrokerFn to start a real unix-socket broker
// (no gh calls since gm_slice_get uses lazy fetching and no agent calls it in tests).
func injectFakeGMBroker(t *testing.T) {
	t.Helper()
	orig := startGMBrokerFn
	t.Cleanup(func() { startGMBrokerFn = orig })
	startGMBrokerFn = func(sockPath string, _ int, _ string) (*gmbroker.Broker, error) {
		return gmbroker.StartWithFetcher(sockPath, func(_ context.Context) (string, error) {
			return "fake spec", nil
		})
	}
}

// TestGMToolsInAllowlist verifies that when the GM broker starts, gm_ tools are
// present in the dev agent RoleConfig.ToolAllowlist.
func TestGMToolsInAllowlist(t *testing.T) {
	injectNoopBroker(t) // disable CBM to avoid real npx calls
	injectFakeGMBroker(t)

	r, golemicDir := setupGMRunner(t)
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")
	if err := os.MkdirAll(filepath.Join(runsDir, r.runID), 0755); err != nil {
		t.Fatal(err)
	}

	var captured []agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		captured = append(captured, cfg)
		return 0, agent.TranscriptPaths{}, nil
	})

	r.runDevAgent(golemicDir, filepath.Join(runsDir, r.runID, "events.jsonl"), 30*time.Second, "", 1)

	if len(captured) == 0 {
		t.Fatal("agent was not called")
	}
	cfg := captured[0]

	tools := strings.Join(cfg.ToolAllowlist, ",")
	for _, want := range gmDevToolNames {
		if !strings.Contains(tools, want) {
			t.Errorf("ToolAllowlist missing %q; got: %s", want, tools)
		}
	}
}

// TestGMSockEnvInjected verifies that GOLEMIC_GM_SOCK is injected into the
// agent subprocess environment when the GM broker starts.
func TestGMSockEnvInjected(t *testing.T) {
	injectNoopBroker(t)
	injectFakeGMBroker(t)

	r, golemicDir := setupGMRunner(t)
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")
	if err := os.MkdirAll(filepath.Join(runsDir, r.runID), 0755); err != nil {
		t.Fatal(err)
	}

	var captured []agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		captured = append(captured, cfg)
		return 0, agent.TranscriptPaths{}, nil
	})

	r.runDevAgent(golemicDir, filepath.Join(runsDir, r.runID, "events.jsonl"), 30*time.Second, "", 1)

	if len(captured) == 0 {
		t.Fatal("agent was not called")
	}
	cfg := captured[0]

	hasSock := false
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "GOLEMIC_GM_SOCK=") {
			hasSock = true
			break
		}
	}
	if !hasSock {
		t.Errorf("GOLEMIC_GM_SOCK not in RoleConfig.Env; got: %v", cfg.Env)
	}
}

// TestGMBrokerSocketCleanup verifies that the GM broker socket is removed after
// runDevAgent returns.
func TestGMBrokerSocketCleanup(t *testing.T) {
	injectNoopBroker(t)

	r, golemicDir := setupGMRunner(t)
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs")
	if err := os.MkdirAll(filepath.Join(runsDir, r.runID), 0755); err != nil {
		t.Fatal(err)
	}

	var capturedSock string
	orig := startGMBrokerFn
	t.Cleanup(func() { startGMBrokerFn = orig })
	startGMBrokerFn = func(sockPath string, _ int, _ string) (*gmbroker.Broker, error) {
		capturedSock = sockPath
		return gmbroker.StartWithFetcher(sockPath, func(_ context.Context) (string, error) {
			return "spec", nil
		})
	}

	r.SetRunAgentFn(func(_ context.Context, _ agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, agent.TranscriptPaths{}, nil
	})

	r.runDevAgent(golemicDir, filepath.Join(runsDir, r.runID, "events.jsonl"), 30*time.Second, "", 1)

	if capturedSock == "" {
		t.Fatal("startGMBrokerFn was not called")
	}
	if _, err := os.Stat(capturedSock); !os.IsNotExist(err) {
		t.Errorf("GM socket still exists after runDevAgent returned; path: %s", capturedSock)
	}
}

// TestGMReviewerToolsExcludesProjectCheck verifies the reviewer allowlist never
// includes the dev-only gm_project_check tool.
func TestGMReviewerToolsExcludesProjectCheck(t *testing.T) {
	if containsTool(gmReviewerToolNames, "gm_project_check") {
		t.Fatalf("gmReviewerToolNames unexpectedly includes gm_project_check: %v", gmReviewerToolNames)
	}
}

// TestGMReviewerToolsExcludesReviewSubmit verifies the reviewer is not handed the
// skeleton gm_review_submit tool: it only echoes and never writes the
// review_submitted event, so the agent must use the golemic submit-review CLI.
func TestGMReviewerToolsExcludesReviewSubmit(t *testing.T) {
	if containsTool(gmReviewerToolNames, "gm_review_submit") {
		t.Fatalf("gmReviewerToolNames unexpectedly includes gm_review_submit: %v", gmReviewerToolNames)
	}
}

// TestGMDevToolsExcludesDevDone verifies the dev is not handed the skeleton
// gm_dev_done tool, which only echoes its params.
func TestGMDevToolsExcludesDevDone(t *testing.T) {
	if containsTool(gmDevToolNames, "gm_dev_done") {
		t.Fatalf("gmDevToolNames unexpectedly includes gm_dev_done: %v", gmDevToolNames)
	}
}

func containsTool(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Shared GM broker injection helpers for runner-level tests
// ---------------------------------------------------------------------------

// injectFakeGMBrokerWithConfig overrides startGMBrokerFn with a broker that
// uses the given projectCheckFn and computeFingerprintFn. Pass nil to use defaults.
// The broker includes gm_project_check in its allowlist.
func injectFakeGMBrokerWithConfig(
	t *testing.T,
	checkFn func(gmbroker.ProjectCheckConfig, string) (*gmbroker.ProjectCheckResult, error),
	computeFn func(string) (string, error),
) {
	t.Helper()
	orig := startGMBrokerFn
	t.Cleanup(func() { startGMBrokerFn = orig })
	devTools := []string{"gm_slice_get", "gm_project_check", "gm_dev_done"}
	startGMBrokerFn = func(sockPath string, _ int, _ string) (*gmbroker.Broker, error) {
		b, err := gmbroker.StartWithFetcherAndProjectCheck(
			sockPath,
			func(_ context.Context) (string, error) { return "fake spec", nil },
			gmbroker.ProjectCheckConfig{},
			devTools,
		)
		if err != nil {
			return nil, err
		}
		if checkFn != nil {
			b.SetProjectCheckFn(checkFn)
		}
		if computeFn != nil {
			b.SetComputeFingerprintFn(computeFn)
		}
		return b, nil
	}
}

// injectFakeGMBrokerPP injects a fake GM broker pre-configured for pingpong-style
// tests: project_check returns OK=true with fingerprint "pp-test-fp" and the
// fingerprint computation also returns "pp-test-fp", so gm_dev_done always passes
// after a single project_check call.
func injectFakeGMBrokerPP(t *testing.T) {
	t.Helper()
	injectFakeGMBrokerWithConfig(t,
		func(_ gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{
				OK:                     true,
				WorkingTreeFingerprint: "pp-test-fp",
				Summary:                "verify passed",
			}, nil
		},
		func(_ string) (string, error) { return "pp-test-fp", nil },
	)
}

// gmSockFromEnv extracts the GOLEMIC_GM_SOCK path from the env slice.
func gmSockFromEnv(env []string) string {
	for _, e := range env {
		if strings.HasPrefix(e, "GOLEMIC_GM_SOCK=") {
			return strings.TrimPrefix(e, "GOLEMIC_GM_SOCK=")
		}
	}
	return ""
}

// callGMTool sends a single gm_ tool call to the broker socket and returns the
// parsed result map. Returns nil on any error.
func callGMTool(sockPath, tool, callID string, params any) map[string]any {
	raw, _ := json.Marshal(params)
	req, _ := json.Marshal(map[string]any{
		"tool":   tool,
		"callId": callID,
		"params": json.RawMessage(raw),
	})
	req = append(req, '\n')
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil
	}
	defer conn.Close() //nolint:errcheck
	if _, err := conn.Write(req); err != nil {
		return nil
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil
	}
	var result map[string]any
	json.Unmarshal(resp.Result, &result) //nolint:errcheck
	return result
}

// sendGMProjectCheck sends gm_project_check via GOLEMIC_GM_SOCK in env.
// Returns true if the call returned ok==true.
func sendGMProjectCheck(env []string) bool {
	sockPath := gmSockFromEnv(env)
	if sockPath == "" {
		return false
	}
	result := callGMTool(sockPath, "gm_project_check", "test-check", map[string]any{})
	if result == nil {
		return false
	}
	ok, _ := result["ok"].(bool)
	return ok
}

// sendGMDevDone sends gm_dev_done via GOLEMIC_GM_SOCK in env.
// Returns true if the gate accepted (ok==true, accepted==true).
func sendGMDevDone(env []string) bool {
	sockPath := gmSockFromEnv(env)
	if sockPath == "" {
		return false
	}
	result := callGMTool(sockPath, "gm_dev_done", "test-done", map[string]string{
		"summary":   "Implement the feature",
		"commitMsg": "feat(test): implement feature (42)",
		"prTitle":   "feat: implement feature",
		"prBody":    "Closes #42",
	})
	if result == nil {
		return false
	}
	ok, _ := result["ok"].(bool)
	return ok
}
