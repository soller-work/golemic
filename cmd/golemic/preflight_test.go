package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/preflight"
)

func TestRunPreflight(t *testing.T) { //nolint:cyclop,funlen,gocognit // moved verbatim; cyclomatic 35 and cognitive 75 exceed thresholds on the pre-existing table body
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
				"ok\n",
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
			wantStdout: "FAILED: gh installiert - gh not found: executable file not found\n" +
				"FAILED: pi installiert - pi not found: executable file not found\n" +
				"FAILED: git - git not found: executable file not found\n",
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
				// Isolate from environment: env vars take precedence over file values.
				// Set them to tokens the mock executor recognises.
				t.Setenv("GOLEMIC_DEV_TOKEN", "ghp_dev_token")
				t.Setenv("GOLEMIC_REVIEWER_TOKEN", "ghp_rev_token")

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
			got := runPreflight(tt.exec, homeDir, repoRoot, &stdout, &stderr, false)

			if got != tt.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tt.wantExit)
			}

			if tt.wantExit == 0 { //nolint:nestif // moved verbatim; complexity pre-dates split
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
				// Verify the remaining lines are FAILED
				if !strings.Contains(out, "FAILED: .golemic/ Scaffolding") {
					t.Errorf("stdout missing FAILED: .golemic/ Scaffolding\n  got: %q", out)
				}
				if !strings.Contains(out, "FAILED: config.json valide") {
					t.Errorf("stdout missing FAILED: config.json valide\n  got: %q", out)
				}
				if !strings.Contains(out, "FAILED: Credentials") {
					t.Errorf("stdout missing FAILED: Credentials\n  got: %q", out)
				}
				// Must contain final 'failed' summary
				if !strings.Contains(out, "\nfailed\n") {
					t.Errorf("stdout missing final 'failed' summary, got: %q", out)
				}
			}

			if stderr.Len() > 0 {
				t.Errorf("stderr should be empty, got: %q", stderr.String())
			}
		})
	}
}

// TestRunPreflightCheckFlag_MissingGolemic covers AC-004: check mode with missing
// .golemic/ exits 1 and writes nothing.
func TestRunPreflightCheckFlag_MissingGolemic_AC004(t *testing.T) { //nolint:cyclop // moved verbatim; cyclomatic complexity 12 exceeds threshold
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch name {
			case "gh":
				return "gh version 2.0.0", nil
			case "pi":
				return "pi version 1.0.0", nil
			case "git":
				if len(args) >= 1 && args[0] == "config" {
					return "https://github.com/owner/repo.git", nil
				}
				if len(args) >= 1 && args[0] == "worktree" {
					return "/tmp/repo (main)\n", nil
				}
				return "git version 2.0.0", nil
			}
			return "", fmt.Errorf("not mocked: %s", name)
		},
	}
	homeDir := t.TempDir()
	repoRoot := t.TempDir() // no .golemic/

	var stdout bytes.Buffer
	exitCode := runPreflight(exec, homeDir, repoRoot, &stdout, io.Discard, true)

	if exitCode != 1 {
		t.Errorf("exit code: got %d, want 1", exitCode)
	}

	out := stdout.String()
	if !strings.Contains(out, "FAILED: .golemic/ Scaffolding") {
		t.Errorf("stdout missing FAILED for scaffolding, got: %q", out)
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("stdout missing final 'failed' summary, got: %q", out)
	}

	// .golemic/ must not have been created
	if _, err := os.Stat(filepath.Join(repoRoot, ".golemic")); !os.IsNotExist(err) {
		t.Error(".golemic/ must not be created in check mode")
	}
}

// TestRunPreflightSetupMode_Scaffolds covers AC-005: setup mode (no --check flag)
// still scaffolds .golemic/ when missing.
func TestRunPreflightSetupMode_Scaffolds_AC005(t *testing.T) { //nolint:cyclop // moved verbatim; cyclomatic complexity 11 exceeds threshold
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch name {
			case "gh":
				return "gh version 2.0.0", nil
			case "pi":
				return "pi version 1.0.0", nil
			case "git":
				if len(args) >= 1 && args[0] == "config" {
					return "https://github.com/owner/repo.git", nil
				}
				if len(args) >= 1 && args[0] == "worktree" {
					return "/tmp/repo (main)\n", nil
				}
				return "git version 2.0.0", nil
			}
			return "", fmt.Errorf("not mocked: %s", name)
		},
	}
	homeDir := t.TempDir()
	repoRoot := t.TempDir() // no .golemic/

	var stdout bytes.Buffer
	exitCode := runPreflight(exec, homeDir, repoRoot, &stdout, io.Discard, false)

	// Setup mode scaffolds, but still FAILED (template placeholder not filled)
	if exitCode != 1 {
		t.Errorf("exit code: got %d, want 1 (template not filled)", exitCode)
	}

	// .golemic/ must have been created
	if _, err := os.Stat(filepath.Join(repoRoot, ".golemic")); os.IsNotExist(err) {
		t.Error(".golemic/ should be created in setup mode")
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".golemic", "config.json")); os.IsNotExist(err) {
		t.Error(".golemic/config.json should be scaffolded in setup mode")
	}
}
