package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"golemic/internal/preflight"
)

// fakeExecutor implements preflight.Executor for testing.
type fakeExecutor struct {
	runFunc        func(name string, args ...string) (string, error)
	runWithEnvFunc func(env map[string]string, name string, args ...string) (string, error)
}

func (f fakeExecutor) Run(name string, args ...string) (string, error) {
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (f fakeExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	if f.runWithEnvFunc != nil {
		return f.runWithEnvFunc(env, name, args...)
	}
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (f fakeExecutor) RunInDir(_ string, name string, args ...string) (string, error) {
	return f.Run(name, args...)
}

func (f fakeExecutor) RunWithEnvInDir(env map[string]string, _ string, name string, args ...string) (string, error) {
	return f.RunWithEnv(env, name, args...)
}

func TestRunDispatch(t *testing.T) { //nolint:funlen // table-driven test with multiple dispatch scenarios
	tests := []struct {
		name            string
		args            []string
		wantExit        int
		wantStdoutSubs  []string
		wantStderrSubs  []string
		stdoutMustEmpty bool
	}{
		{
			name:            "unknown command prints error to stderr",
			args:            []string{"golemic", "does-not-exist"},
			wantExit:        1,
			wantStderrSubs:  []string{"Unknown command: does-not-exist", "Usage: golemic"},
			stdoutMustEmpty: true,
		},
		{
			name:            "run without --issue prints usage error",
			args:            []string{"golemic", "run"},
			wantExit:        1,
			wantStderrSubs:  []string{"--issue must be a positive integer"},
			stdoutMustEmpty: true,
		},
		{
			name:            "removed open-pr command is unknown",
			args:            []string{"golemic", "open-pr"},
			wantExit:        1,
			wantStderrSubs:  []string{"Unknown command: open-pr", "Usage: golemic"},
			stdoutMustEmpty: true,
		},
		{
			name:            "removed cbm wrapper is unknown",
			args:            []string{"golemic", "cbm"},
			wantExit:        1,
			wantStderrSubs:  []string{"Unknown command: cbm", "Usage: golemic"},
			stdoutMustEmpty: true,
		},
		{
			name:            "run-loop subcommand is now unrecognized",
			args:            []string{"golemic", "run-loop"},
			wantExit:        1,
			wantStderrSubs:  []string{"Unknown command: run-loop", "Usage: golemic"},
			stdoutMustEmpty: true,
		},
		{
			name:           "--help prints usage to stdout and exits 0",
			args:           []string{"golemic", "--help"},
			wantExit:       0,
			wantStdoutSubs: []string{"Usage: golemic"},
		},
		{
			name:           "-h prints usage to stdout and exits 0",
			args:           []string{"golemic", "-h"},
			wantExit:       0,
			wantStdoutSubs: []string{"Usage: golemic"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := runDispatch(context.Background(), tc.args, &stdout, &stderr, fakeRunLoopExecutor{})
			if got != tc.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tc.wantExit)
			}
			if tc.stdoutMustEmpty && stdout.Len() != 0 {
				t.Errorf("stdout must be empty for error states, got: %q", stdout.String())
			}
			for _, sub := range tc.wantStdoutSubs {
				if !strings.Contains(stdout.String(), sub) {
					t.Errorf("stdout missing %q; got: %q", sub, stdout.String())
				}
			}
			for _, sub := range tc.wantStderrSubs {
				if !strings.Contains(stderr.String(), sub) {
					t.Errorf("stderr missing %q; got: %q", sub, stderr.String())
				}
			}
		})
	}
}

// TestRunDispatch_NoArgs verifies that bare golemic (no subcommand) starts the
// autonomous polling loop and exits 0 on clean cancellation.
func TestRunDispatch_NoArgs(t *testing.T) {
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
		cancel()
		return "", &preflight.ErrExit{ExitCode: 2, Stderr: "no issue"}
	})

	var stdout, stderr bytes.Buffer
	exitCode := runDispatch(ctx, []string{"golemic"}, &stdout, &stderr, exec)
	if exitCode != 0 {
		t.Errorf("exit code: want 0, got %d; stderr: %q", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "run-loop started") {
		t.Errorf("stderr should contain 'run-loop started', got: %q", stderr.String())
	}
}

var _ preflight.Executor = fakeExecutor{}
