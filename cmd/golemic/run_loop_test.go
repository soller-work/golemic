package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/preflight"
	"golemic/internal/runloop"
)

// fakeRunLoopExecutor implements runloop.Executor for integration tests.
type fakeRunLoopExecutor struct {
	fakeExecutor
	startFunc func(env map[string]string, dir, name string, args ...string) (runloop.ProcessHandle, error)
}

func (f fakeRunLoopExecutor) StartWithEnvInDir(env map[string]string, dir, name string, args ...string) (runloop.ProcessHandle, error) {
	if f.startFunc != nil {
		return f.startFunc(env, dir, name, args...)
	}
	return nil, fmt.Errorf("not mocked: %s %v", name, args)
}

// fakeProcessHandle is a minimal runloop.ProcessHandle for tests.
type fakeProcessHandle struct{}

func (h fakeProcessHandle) Wait() error            { return nil }
func (h fakeProcessHandle) Signal(os.Signal) error { return nil }

// runLoopFixture creates a temp homeDir and repoRoot with minimal config and credentials.
// It also unsets GOLEMIC_DEV_TOKEN and GOLEMIC_REVIEWER_TOKEN for the duration of the test
// so the credentials file is the authoritative source (CI may have these set to real tokens).
func runLoopFixture(t *testing.T) (homeDir, repoRoot string) {
	t.Helper()
	homeDir = t.TempDir()
	repoRoot = t.TempDir()

	cfgDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"),
		[]byte(`{"project":"test-proj","verify_command":"go test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	credsDir := filepath.Join(homeDir, ".golemic", "test-proj")
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"),
		[]byte(`{"dev_token":"ghp_test","reviewer_token":"ghp_rev"}`), 0600); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"} {
		k, orig := k, os.Getenv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() { _ = os.Setenv(k, orig) })
	}

	return homeDir, repoRoot
}

// makeRunLoopExec builds a fakeRunLoopExecutor with standard startup stubs
// (git rev-parse -> repoRoot, golemic preflight -> pass) plus a custom next-issue handler.
func makeRunLoopExec(repoRoot string, nextIssueFunc func() (string, error)) fakeRunLoopExecutor {
	return fakeRunLoopExecutor{
		fakeExecutor: fakeExecutor{
			runFunc: func(name string, args ...string) (string, error) {
				if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
					return repoRoot + "\n", nil
				}
				if len(args) > 0 && args[0] == "preflight" {
					return "", nil
				}
				if len(args) > 0 && args[0] == "next-issue" {
					return nextIssueFunc()
				}
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			},
		},
	}
}

// TestRunLoopDispatcher_SingleTickSmoke verifies that the dispatcher wires executor
// and env correctly via a single-tick smoke test using a mock executor.
func TestRunLoopDispatcher_SingleTickSmoke(t *testing.T) {
	homeDir, repoRoot := runLoopFixture(t)

	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", origHome) }()

	origDir, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	ctx, cancel := context.WithCancel(context.Background())

	exec := makeRunLoopExec(repoRoot, func() (string, error) {
		cancel() // stop loop after first tick
		return "", &preflight.ErrExit{ExitCode: 2, Stderr: "no issue"}
	})

	var stdout, stderr bytes.Buffer
	exitCode := runRunLoop(ctx, []string{"golemic", "run-loop"}, &stdout, &stderr, exec)
	if exitCode != 0 {
		t.Errorf("exit code: want 0, got %d; stderr: %q", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "run-loop started") {
		t.Errorf("stderr should contain 'run-loop started', got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "run-loop terminated") {
		t.Errorf("stderr should contain 'run-loop terminated', got: %q", stderr.String())
	}
}

// TestRunLoopDispatcher_StartupFailsWhenLabelsMissing verifies AC-009:
// startup exits 1 when the preflight label check reports a missing label.
func TestRunLoopDispatcher_StartupFailsWhenLabelsMissing(t *testing.T) { //nolint:cyclop // linear startup validation scenario
	homeDir, repoRoot := runLoopFixture(t)

	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", origHome) }()

	origDir, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	exec := fakeRunLoopExecutor{
		fakeExecutor: fakeExecutor{
			runFunc: func(name string, args ...string) (string, error) {
				if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
					return repoRoot + "\n", nil
				}
				if len(args) > 0 && args[0] == "preflight" {
					return "", &preflight.ErrExit{ExitCode: 1, Stderr: "missing labels: in-progress"}
				}
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			},
		},
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	exitCode := runRunLoop(ctx, []string{"golemic", "run-loop"}, &stdout, &stderr, exec)
	if exitCode != 1 {
		t.Errorf("exit code: want 1, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "startup failed") {
		t.Errorf("stderr should mention startup failed, got: %q", stderr.String())
	}
}

// TestRunLoopDispatcher_EnvWiredCorrectly verifies that the tick env vars are set
// on subcommand calls (GOLEMIC_RUN_ID, GOLEMIC_EVENT_LOG, GOLEMIC_TURN_ID).
func TestRunLoopDispatcher_EnvWiredCorrectly(t *testing.T) { //nolint:cyclop,gocognit,funlen // env wiring assertions; multi-field capture requires coordinated mock setup
	homeDir, repoRoot := runLoopFixture(t)

	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", origHome) }()

	origDir, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	ctx, cancel := context.WithCancel(context.Background())

	var capturedRunEnv map[string]string
	views := 0
	exec := fakeRunLoopExecutor{
		fakeExecutor: fakeExecutor{
			runFunc: func(name string, args ...string) (string, error) {
				if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
					return repoRoot + "\n", nil
				}
				if len(args) > 0 && args[0] == "preflight" {
					return "", nil
				}
				if len(args) > 0 && args[0] == "next-issue" {
					return `{"number":7,"title":"T","url":"u","labels":[]}`, nil
				}
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			},
			runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
				if name != "gh" {
					return "", fmt.Errorf("not mocked: %s %v", name, args)
				}
				if len(args) >= 2 && args[0] == "api" && args[1] == "user" {
					return `{"login":"golemic-dev"}`, nil
				}
				if len(args) >= 2 && args[0] == "issue" && args[1] == "view" {
					views++
					if views == 1 {
						return `{"labels":[{"name":"ready-for-agent"}],"assignees":[]}`, nil
					}
					return `{"labels":[{"name":"in-progress"}],"assignees":[{"login":"golemic-dev"}]}`, nil
				}
				if len(args) >= 2 && args[0] == "issue" && args[1] == "edit" {
					return "", nil
				}
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			},
		},
		startFunc: func(env map[string]string, dir, name string, args ...string) (runloop.ProcessHandle, error) {
			cp := make(map[string]string, len(env))
			for k, v := range env {
				cp[k] = v
			}
			capturedRunEnv = cp
			cancel() // stop loop after one tick completes
			return fakeProcessHandle{}, nil
		},
	}

	var stdout, stderr bytes.Buffer

	done := make(chan int, 1)
	go func() {
		done <- runRunLoop(ctx, []string{"golemic", "run-loop"}, &stdout, &stderr, exec)
	}()

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code: want 0, got %d; stderr: %q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out")
	}

	if capturedRunEnv == nil {
		t.Fatal("run env was not captured")
	}
	if capturedRunEnv["GOLEMIC_RUN_ID"] == "" {
		t.Error("run: GOLEMIC_RUN_ID not set")
	}
	if capturedRunEnv["GOLEMIC_EVENT_LOG"] == "" {
		t.Error("run: GOLEMIC_EVENT_LOG not set")
	}
	if capturedRunEnv["GOLEMIC_TURN_ID"] != "0" {
		t.Errorf("run: GOLEMIC_TURN_ID: want 0, got %q", capturedRunEnv["GOLEMIC_TURN_ID"])
	}
}
