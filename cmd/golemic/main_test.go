package main

import (
	"bytes"
	"fmt"
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

func TestRun(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantExit       int
		wantStdoutSub  string
		wantStderrSubs []string
	}{
		{
			name:           "no arguments prints usage to stderr",
			args:           []string{"golemic"},
			wantExit:       1,
			wantStderrSubs: []string{"Usage: golemic"},
		},
		{
			name:           "unknown command prints error to stderr",
			args:           []string{"golemic", "does-not-exist"},
			wantExit:       1,
			wantStderrSubs: []string{"Unknown command: does-not-exist", "Usage: golemic"},
		},
		{
			name:           "run without --issue prints usage error",
			args:           []string{"golemic", "run"},
			wantExit:       1,
			wantStderrSubs: []string{"--issue must be a positive integer"},
		},
		{
			name:           "removed open-pr command is unknown",
			args:           []string{"golemic", "open-pr"},
			wantExit:       1,
			wantStderrSubs: []string{"Unknown command: open-pr", "Usage: golemic"},
		},
		{
			name:           "removed cbm wrapper is unknown",
			args:           []string{"golemic", "cbm"},
			wantExit:       1,
			wantStderrSubs: []string{"Unknown command: cbm", "Usage: golemic"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tc.args, &stdout, &stderr)
			if got != tc.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tc.wantExit)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout must be empty for error states, got: %q", stdout.String())
			}
			for _, sub := range tc.wantStderrSubs {
				if !strings.Contains(stderr.String(), sub) {
					t.Errorf("stderr missing %q; got: %q", sub, stderr.String())
				}
			}
		})
	}
}

var _ preflight.Executor = fakeExecutor{}
