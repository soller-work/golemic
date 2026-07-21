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

// TestCBMDevTools_FlagOff verifies that the dev allowlist is exactly read,bash,write,edit when CBM is off.
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
	wantTools := "read,bash,write,edit"
	gotTools := strings.Join(cfg.ToolAllowlist, ",")
	if gotTools != wantTools {
		t.Errorf("ToolAllowlist = %q, want %q", gotTools, wantTools)
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
