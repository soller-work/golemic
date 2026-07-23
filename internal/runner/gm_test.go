package runner

import (
	"context"
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
	for _, want := range gmToolNames {
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
