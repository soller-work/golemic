package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

func TestRun(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantExit       int
		wantStdoutSub  string // empty means stdout must be empty
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
			name:           "run not implemented",
			args:           []string{"golemic", "run"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
		{
			name:           "emit not implemented",
			args:           []string{"golemic", "emit"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
		{
			name:           "open-pr not implemented",
			args:           []string{"golemic", "open-pr"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
		{
			name:           "submit-review not implemented",
			args:           []string{"golemic", "submit-review"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
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

func TestRunPreflight(t *testing.T) {
	tests := []struct {
		name          string
		exec          preflight.Executor
		homeDir       string // empty = create temp
		repoRoot      string // empty = create temp
		wantExit      int
		wantStdout    string // exact expected stdout
	}{
		{
			name: "all checks pass",
			exec: fakeExecutor{
				runFunc: func(name string, args ...string) (string, error) {
					switch name {
					case "gh":
						if len(args) >= 1 && args[0] == "api" && args[1] == "user" {
							return `{"login":"dev-bot"}`, nil
						}
						return "gh version 2.0.0", nil
					case "pi":
						return "pi version 1.0.0", nil
					case "git":
						switch {
						case len(args) >= 1 && args[0] == "config":
							return "https://github.com/owner/repo.git", nil
						case len(args) >= 1 && args[0] == "worktree":
							return "/tmp/repo (main)\n", nil
						default:
							return "git version 2.0.0", nil
						}
					}
					return "", fmt.Errorf("unknown: %s", name)
				},
				runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
					if name == "gh" && len(args) >= 1 && args[0] == "api" && args[1] == "user" {
						token := env["GH_TOKEN"]
						if strings.Contains(token, "dev") {
							return `{"login":"dev-bot"}`, nil
						}
						if strings.Contains(token, "rev") {
							return `{"login":"reviewer-bot"}`, nil
						}
						return `{"login":"unknown"}`, nil
					}
					return "", fmt.Errorf("not mocked")
				},
			},
			wantExit: 0,
			wantStdout: "OK: gh installiert\n" +
				"OK: pi installiert\n" +
				"OK: git\n" +
				"OK: .golemic/ Scaffolding\n" +
				"OK: config.json valide\n" +
				"OK: Credentials\n" +
				"SUCCESS\n",
		},
		{
			name: "gh missing",
			exec: fakeExecutor{
				runFunc: func(name string, args ...string) (string, error) {
					return "", fmt.Errorf("executable file not found")
				},
				runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
					return "", fmt.Errorf("not found")
				},
			},
			wantExit: 1,
			wantStdout: "FEHLT: gh installiert — gh not found: executable file not found\n" +
				"FEHLT: pi installiert — pi not found: executable file not found\n" +
				"FEHLT: git — git not found: executable file not found\n",
			// Remaining lines (scaffolding, config, credentials) depend on temp dir
			// path and are checked via prefix/contains below.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := tt.homeDir
			if homeDir == "" {
				homeDir = t.TempDir()
			}
			repoRoot := tt.repoRoot
			if repoRoot == "" {
				repoRoot = t.TempDir()
			}

			// For success case: pre-create valid config and credentials
			if tt.wantExit == 0 {
				// Valid config
				golemicDir := filepath.Join(repoRoot, ".golemic")
				if err := os.MkdirAll(golemicDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{
					"project": "test-project",
					"verify_command": "go test"
				}`), 0644); err != nil {
					t.Fatal(err)
				}
				// Valid credentials (must be 0600)
				credDir := filepath.Join(homeDir, ".golemic", "test-project")
				if err := os.MkdirAll(credDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
					"dev_token": "ghp_dev_token",
					"reviewer_token": "ghp_rev_token"
				}`), 0600); err != nil {
					t.Fatal(err)
				}
			}

			var stdout, stderr bytes.Buffer
			got := runPreflight(tt.exec, homeDir, repoRoot, &stdout, &stderr)

			if got != tt.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tt.wantExit)
			}

			if tt.wantExit == 0 {
				// Success case: exact match
				if stdout.String() != tt.wantStdout {
					t.Errorf("stdout:\n  got:  %q\n  want: %q", stdout.String(), tt.wantStdout)
				}
			} else {
				// Failure case: check prefix (first 3 FEHLT lines are predictable)
				out := stdout.String()
				if !strings.HasPrefix(out, tt.wantStdout) {
					t.Errorf("stdout prefix mismatch:\n  got:  %q\n  want prefix: %q", out, tt.wantStdout)
				}
				// Verify the remaining lines are FEHLT
				if !strings.Contains(out, "FEHLT: .golemic/ Scaffolding") {
					t.Errorf("stdout missing FEHLT: .golemic/ Scaffolding\n  got: %q", out)
				}
				if !strings.Contains(out, "FEHLT: config.json valide") {
					t.Errorf("stdout missing FEHLT: config.json valide\n  got: %q", out)
				}
				if !strings.Contains(out, "FEHLT: Credentials") {
					t.Errorf("stdout missing FEHLT: Credentials\n  got: %q", out)
				}
				// Must NOT contain SUCCESS
				if strings.Contains(out, "SUCCESS") {
					t.Errorf("stdout must not contain SUCCESS when checks fail, got: %q", out)
				}
			}

			if stderr.Len() > 0 {
				t.Errorf("stderr should be empty, got: %q", stderr.String())
			}
		})
	}
}