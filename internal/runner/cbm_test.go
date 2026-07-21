package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
	"golemic/internal/cbmbroker"
	"golemic/internal/config"
	"golemic/internal/credentials"
)

// setupCBMRunner creates a runner with the given CodebaseMemory.Enabled value for CBM tests.
func setupCBMRunner(t *testing.T, exec *fakeExecutor, cbmEnabled bool) (*Runner, string) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	golemicCfgDir := filepath.Join(repoRoot, ".golemic")

	guidelinesDir := filepath.Join(golemicCfgDir, "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "reviewer.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	agentsDir := filepath.Join(golemicCfgDir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"dev", "reviewer"} {
		if err := os.WriteFile(filepath.Join(agentsDir, role+".md"), []byte("---\nmodel: test/model\n---\npersona body\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	r := New(exec, homeDir, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = project
	r.runID = "issue-42-cbm-test"
	r.branchName = "golemic/issue-42"
	r.creds = creds
	r.cfg = &config.Config{
		VerifyCommand:  "go test",
		TimeoutMinutes: 30,
		CodebaseMemory: config.CodebaseMemoryConfig{Enabled: cbmEnabled},
	}
	r.issue = &issueData{Number: 42, Title: "CBM test issue"}
	r.turnCounter = 1

	golemicDir := filepath.Join(homeDir, ".golemic", project)
	return r, golemicDir
}

// injectNoopBroker overrides startCBMBrokerFn to return an error so no real
// npx process is started. The test remains focused on the executor-level
// behavior without a real MCP process.
func injectNoopBroker(t *testing.T) {
	t.Helper()
	orig := startCBMBrokerFn
	startCBMBrokerFn = func(sockPath string, env map[string]string) (*cbmbroker.Broker, error) {
		return nil, fmt.Errorf("noop broker: not started in tests")
	}
	t.Cleanup(func() { startCBMBrokerFn = orig })
}

// startFakeBroker starts a minimal fake MCP broker backed by io.Pipe pairs so
// runner tests can assert CBM_SOCK and CBM_PROJECT are injected into RoleConfig.Env
// without running a real npx process.
func startFakeBroker(t *testing.T, sockPath string) *cbmbroker.Broker {
	t.Helper()

	childInR, childInW := io.Pipe()
	childOutR, childOutW := io.Pipe()

	// Fake MCP child: handle initialize, then echo an empty result for everything.
	go func() {
		reader := bufio.NewReaderSize(childInR, 4<<20)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var msg struct {
				ID     *int64 `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(line, &msg) != nil || msg.ID == nil {
				continue // notification
			}
			resp, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result":  map[string]interface{}{},
			})
			childOutW.Write(append(resp, '\n')) //nolint:errcheck
		}
	}()

	childDone := make(chan struct{})
	b, err := cbmbroker.StartWithIO(sockPath, childInW, bufio.NewReaderSize(childOutR, 4<<20),
		func(os.Signal) error { childInW.Close(); childOutW.Close(); close(childDone); return nil },
		func() error { return nil },
		childDone,
	)
	if err != nil {
		t.Fatalf("startFakeBroker: %v", err)
	}
	return b
}

// cbmGitExecutor handles git commands needed by the CBM runner and records npx calls.
type cbmGitExecutor struct {
	npxCalls [][]string
}

func (e *cbmGitExecutor) Run(name string, args ...string) (string, error) {
	return e.RunWithEnvInDir(nil, "", name, args...)
}

func (e *cbmGitExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	return e.RunWithEnvInDir(env, "", name, args...)
}

func (e *cbmGitExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	return e.RunWithEnvInDir(nil, dir, name, args...)
}

func (e *cbmGitExecutor) RunWithEnvInDir(_ map[string]string, _ string, name string, args ...string) (string, error) {
	if name == "npx" {
		e.npxCalls = append(e.npxCalls, args)
		return "", nil
	}
	if name == "git" {
		return handleGitCmd(args)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

// TestIndexWorktree_CallsNPX verifies that indexWorktree invokes npx with the expected args
// including the --name flag.
func TestIndexWorktree_CallsNPX(t *testing.T) {
	exec := &cbmGitExecutor{}
	r, golemicDir := setupCBMRunner(t, nil, true)
	r.executor = exec

	wtPath := t.TempDir()
	cbmCacheDir := filepath.Join(golemicDir, "cbm", "issue-42")

	r.indexWorktree(wtPath, cbmCacheDir, "golemic-issue-42-dev")

	if len(exec.npxCalls) == 0 {
		t.Fatal("expected npx to be called, got none")
	}
	joined := strings.Join(exec.npxCalls[0], " ")
	for _, want := range []string{"-y", "codebase-memory-mcp@0.9.0", "cli", "index_repository", "--repo-path", "--name", "golemic-issue-42-dev", "--mode", "fast"} {
		if !strings.Contains(joined, want) {
			t.Errorf("npx call missing %q; got: %v", want, exec.npxCalls[0])
		}
	}
}

// TestIndexWorktree_FailSoft verifies that a failed npx invocation logs a warning and does not panic.
func TestIndexWorktree_FailSoft(t *testing.T) {
	failExec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "", fmt.Errorf("npx: command not found")
		},
	}
	var stderr bytes.Buffer
	r, golemicDir := setupCBMRunner(t, failExec, true)
	r.executor = failExec
	r.stderr = &stderr

	r.indexWorktree(t.TempDir(), filepath.Join(golemicDir, "cbm", "issue-42"), "golemic-issue-42-dev")

	if !strings.Contains(stderr.String(), "Warning") {
		t.Error("expected a warning on stderr when indexing fails")
	}
}

func makePassthroughGitExec() *fakeExecutor {
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" {
				return handleGitCmd(args)
			}
			if name == "npx" {
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

func runDevAgentCapture(t *testing.T, r *Runner, golemicDir string) []agent.RoleConfig {
	t.Helper()
	var captured []agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		captured = append(captured, cfg)
		return 0, agent.TranscriptPaths{}, nil
	})
	var stderr bytes.Buffer
	r.stderr = &stderr
	eventLogPath := filepath.Join(t.TempDir(), "events.jsonl")
	r.runDevAgent(golemicDir, eventLogPath, 30*time.Second, "", 1)
	return captured
}

// TestCBMDevTools_FlagOff verifies that the dev allowlist is exactly read,bash,write,edit when CBM is off.
func TestCBMDevTools_FlagOff(t *testing.T) {
	exec := makePassthroughGitExec()
	r, golemicDir := setupCBMRunner(t, exec, false)
	injectNoopBroker(t)

	devWT := filepath.Join(golemicDir, "worktrees", "issue-42")
	if err := os.MkdirAll(devWT, 0755); err != nil {
		t.Fatal(err)
	}

	captured := runDevAgentCapture(t, r, golemicDir)
	if len(captured) == 0 {
		t.Fatal("agent was not called")
	}
	cfg := captured[0]
	wantTools := "read,bash,write,edit"
	gotTools := strings.Join(cfg.ToolAllowlist, ",")
	if gotTools != wantTools {
		t.Errorf("ToolAllowlist = %q, want %q", gotTools, wantTools)
	}
	// CBM_SOCK must not be set when CBM is off.
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "CBM_SOCK=") {
			t.Errorf("CBM_SOCK must not be in Env when CBM is off; got: %s", e)
		}
	}
}

func setupDevWTWithGit(t *testing.T, golemicDir string) string {
	t.Helper()
	devWT := filepath.Join(golemicDir, "worktrees", "issue-42")
	if err := os.MkdirAll(filepath.Join(devWT, ".git", "info"), 0755); err != nil {
		t.Fatal(err)
	}
	return devWT
}

// TestCBMDevTools_FlagOn verifies that the dev allowlist is exactly read,bash,write,edit even when CBM is on (BR-C7).
func TestCBMDevTools_FlagOn(t *testing.T) {
	exec := makePassthroughGitExec()
	r, golemicDir := setupCBMRunner(t, exec, true)
	injectNoopBroker(t)
	setupDevWTWithGit(t, golemicDir)

	captured := runDevAgentCapture(t, r, golemicDir)
	if len(captured) == 0 {
		t.Fatal("agent was not called")
	}
	cfg := captured[0]
	wantTools := "read,bash,write,edit"
	gotTools := strings.Join(cfg.ToolAllowlist, ",")
	if gotTools != wantTools {
		t.Errorf("ToolAllowlist = %q, want %q", gotTools, wantTools)
	}
	// CBM tools are no longer appended — agents access codebase-memory via golemic cbm <sub>.
	for _, cbmTool := range []string{"search_graph", "trace_call_path", "detect_changes"} {
		if strings.Contains(gotTools, cbmTool) {
			t.Errorf("CBM tool name %q must not appear in ToolAllowlist (BR-C7); got: %s", cbmTool, gotTools)
		}
	}
}

// TestCBMBrokerEnvInjection verifies that when the broker starts successfully,
// CBM_SOCK and CBM_PROJECT are injected into RoleConfig.Env.
func TestCBMBrokerEnvInjection(t *testing.T) {
	exec := makePassthroughGitExec()
	r, golemicDir := setupCBMRunner(t, exec, true)
	r.homeDir = "/tmp"
	r.project = "p"
	r.runID = "r"

	orig := startCBMBrokerFn
	t.Cleanup(func() { startCBMBrokerFn = orig })
	startCBMBrokerFn = func(sockPath string, env map[string]string) (*cbmbroker.Broker, error) {
		return startFakeBroker(t, sockPath), nil
	}

	setupDevWTWithGit(t, golemicDir)
	captured := runDevAgentCapture(t, r, golemicDir)
	if len(captured) == 0 {
		t.Fatal("agent was not called")
	}

	cfg := captured[0]
	hasSock := false
	hasProject := false
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "CBM_SOCK=") {
			hasSock = true
		}
		if e == "CBM_PROJECT=golemic-issue-42-dev" {
			hasProject = true
		}
	}
	if !hasSock {
		t.Errorf("CBM_SOCK not in RoleConfig.Env; got: %v", cfg.Env)
	}
	if !hasProject {
		t.Errorf("CBM_PROJECT=golemic-issue-42-dev not in RoleConfig.Env; got: %v", cfg.Env)
	}
}

// TestCBMBrokerSocketCleanup verifies that after runDevAgent returns, the broker
// socket file is removed (via Shutdown defer).
func TestCBMBrokerSocketCleanup(t *testing.T) {
	exec := makePassthroughGitExec()
	r, golemicDir := setupCBMRunner(t, exec, true)
	r.homeDir = "/tmp"
	r.project = "p"
	r.runID = "r"

	orig := startCBMBrokerFn
	t.Cleanup(func() { startCBMBrokerFn = orig })
	var capturedSock string
	startCBMBrokerFn = func(sp string, env map[string]string) (*cbmbroker.Broker, error) {
		capturedSock = sp
		return startFakeBroker(t, sp), nil
	}

	setupDevWTWithGit(t, golemicDir)
	runDevAgentCapture(t, r, golemicDir)

	if capturedSock == "" {
		t.Fatal("startCBMBrokerFn was not called")
	}
	// After runDevAgent returns, the defer Shutdown() must have removed the socket.
	if _, err := os.Stat(capturedSock); !os.IsNotExist(err) {
		t.Errorf("socket file still exists after runDevAgent returned; path: %s", capturedSock)
	}
}
