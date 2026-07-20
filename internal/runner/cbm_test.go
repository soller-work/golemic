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

	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "reviewer.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
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
		Models:         config.Models{Dev: "claude-3-5-sonnet-20241022", Reviewer: "claude-3-5-sonnet-20241022"},
		CodebaseMemory: config.CodebaseMemoryConfig{Enabled: cbmEnabled},
	}
	r.issue = &issueData{Number: 42, Title: "CBM test issue"}
	r.turnCounter = 1

	golemicDir := filepath.Join(homeDir, ".golemic", project)
	return r, golemicDir
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

// TestIndexWorktree_CallsNPX verifies that indexWorktree invokes npx with the expected args.
func TestIndexWorktree_CallsNPX(t *testing.T) {
	exec := &cbmGitExecutor{}
	r, golemicDir := setupCBMRunner(t, nil, true)
	r.executor = exec

	wtPath := t.TempDir()
	cbmCacheDir := filepath.Join(golemicDir, "cbm", "issue-42")

	r.indexWorktree(wtPath, cbmCacheDir)

	if len(exec.npxCalls) == 0 {
		t.Fatal("expected npx to be called, got none")
	}
	joined := strings.Join(exec.npxCalls[0], " ")
	for _, want := range []string{"-y", "codebase-memory-mcp@0.9.0", "cli", "index_repository", "--repo-path", "--mode", "fast"} {
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

	r.indexWorktree(t.TempDir(), filepath.Join(golemicDir, "cbm", "issue-42"))

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

// TestCBMDevTools_FlagOff verifies that CBM tools and Approve are absent when flag is off.
func TestCBMDevTools_FlagOff(t *testing.T) {
	exec := makePassthroughGitExec()
	r, golemicDir := setupCBMRunner(t, exec, false)

	devWT := filepath.Join(golemicDir, "worktrees", "issue-42")
	if err := os.MkdirAll(devWT, 0755); err != nil {
		t.Fatal(err)
	}

	captured := runDevAgentCapture(t, r, golemicDir)
	if len(captured) == 0 {
		t.Fatal("agent was not called")
	}
	cfg := captured[0]
	if cfg.Approve {
		t.Error("Approve must be false when CBM is disabled")
	}
	tools := strings.Join(cfg.ToolAllowlist, ",")
	for _, forbidden := range cbmDevTools {
		if strings.Contains(tools, forbidden) {
			t.Errorf("CBM tool %q must not appear when disabled; tools: %s", forbidden, tools)
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

// TestCBMDevTools_FlagOn verifies that CBM tools and Approve=true are present when flag is on.
func TestCBMDevTools_FlagOn(t *testing.T) {
	exec := makePassthroughGitExec()
	r, golemicDir := setupCBMRunner(t, exec, true)
	setupDevWTWithGit(t, golemicDir)

	captured := runDevAgentCapture(t, r, golemicDir)
	if len(captured) == 0 {
		t.Fatal("agent was not called")
	}
	cfg := captured[0]
	if !cfg.Approve {
		t.Error("Approve must be true when CBM is enabled")
	}
	tools := strings.Join(cfg.ToolAllowlist, ",")
	for _, expected := range cbmDevTools {
		if !strings.Contains(tools, expected) {
			t.Errorf("expected CBM tool %q in ToolAllowlist; got: %s", expected, tools)
		}
	}
	if strings.Contains(tools, "detect_changes") {
		t.Error("detect_changes must not be in dev ToolAllowlist (reviewer-only per BR-4)")
	}
}
