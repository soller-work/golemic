package preflight

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unsetEnv ensures an env var is not set for the duration of the test.
func unsetEnvPreflight(t *testing.T, key string) func() {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Unsetenv(key)
	return func() {
		if had {
			os.Setenv(key, old)
		}
	}
}

// fakeExecutor simulates external commands with configurable results.
type fakeExecutor struct {
	runFunc        func(name string, args ...string) (string, error)
	runWithEnvFunc func(env map[string]string, name string, args ...string) (string, error)
}

func (f *fakeExecutor) Run(name string, args ...string) (string, error) {
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (f *fakeExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	if f.runWithEnvFunc != nil {
		return f.runWithEnvFunc(env, name, args...)
	}
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

// fakeExecutorOK returns an executor that always succeeds.
func fakeExecutorOK() *fakeExecutor {
	return &fakeExecutor{
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
				case len(args) >= 1 && args[0] == "config" && args[1] == "--get":
					return "https://github.com/owner/repo.git", nil
				case len(args) >= 1 && args[0] == "worktree":
					return "/tmp/repo (main)\n", nil
				default:
					return "git version 2.0.0", nil
				}
			}
			return "", fmt.Errorf("unknown command: %s", name)
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
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
}

// setupPreflight creates a Preflight instance with a fake executor and temp dirs.
func setupPreflight(t *testing.T, exec *fakeExecutor) (*Preflight, string, string) {
	t.Helper()
	homeDir := t.TempDir()
	repoRoot := t.TempDir()

	// Init git repo for repoRoot
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	p := New(exec, homeDir, repoRoot)
	return p, homeDir, repoRoot
}

func TestRunAllAllChecksPass(t *testing.T) {
	// Ensure env vars match mock-recognizable tokens (env vars take precedence
	// over file values in credentials.Loader)
	t.Setenv("GOLEMIC_DEV_TOKEN", "ghp_dev_token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "ghp_rev_token")

	exec := fakeExecutorOK()
	p, homeDir, repoRoot := setupPreflight(t, exec)

	projectName := filepath.Base(repoRoot)

	// Create valid config.json
	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{
		"project": "`+projectName+`",
		"verify_command": "go test"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create valid credentials (so both checkCredentialsScaffolding finds it
	// and checkCredentials validates it)
	credDir := filepath.Join(homeDir, ".golemic", projectName)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
		"dev_token": "ghp_dev_token",
		"reviewer_token": "ghp_rev_token"
	}`), 0600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	p.SetStdout(&buf)

	results := p.RunAll()

	if !results.AllOK() {
		t.Errorf("RunAll() should return all OK, got failures:")
		for _, r := range results {
			if !r.Ok {
				t.Errorf("  - %s: %s", r.Name, r.Details)
			}
		}
	}

	output := buf.String()
	// Check output format — 6 checks (credentials scaffolding is transparent, not a separate check)
	for _, expected := range []string{
		"OK: gh installiert",
		"OK: pi installiert",
		"OK: git",
		"OK: .golemic/ Scaffolding",
		"OK: config.json valide",
		"OK: Credentials",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q, got: %s", expected, output)
		}
	}
	if !strings.Contains(output, "\nok\n") {
		t.Errorf("output missing final 'ok' summary, got: %s", output)
	}
}

func TestRunAllRunsAllChecksEvenOnFailure(t *testing.T) {
	// Setup: no gh, no pi, no git, no scaffolding, no config, no credentials
	// All checks should fail but still run.
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "", fmt.Errorf("not found")
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", fmt.Errorf("not found")
		},
	}
	p, _, _ := setupPreflight(t, exec)

	var buf bytes.Buffer
	p.SetStdout(&buf)

	results := p.RunAll()

	// Should have exactly 6 results (gh, pi, git, scaffolding, config, credentials)
	if len(results) != 6 {
		t.Errorf("RunAll() returned %d results, want 6", len(results))
	}

	// All should fail
	if results.AllOK() {
		t.Errorf("RunAll() should fail when all checks fail")
	}

	output := buf.String()
	// All 6 checks should appear in output
	for _, expected := range []string{
		"FAILED: gh installiert",
		"FAILED: pi installiert",
		"FAILED: git",
		"FAILED: .golemic/ Scaffolding",
		"FAILED: config.json valide",
		"FAILED: Credentials",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q, got: %s", expected, output)
		}
	}

	// Must contain final 'failed' summary
	if !strings.Contains(output, "\nfailed\n") {
		t.Errorf("output missing final 'failed' summary, got: %s", output)
	}
}

func TestRunAllScaffoldingFailThenFix(t *testing.T) {
	// Ensure env vars match mock-recognizable tokens (env vars take precedence
	// over file values in credentials.Loader)
	t.Setenv("GOLEMIC_DEV_TOKEN", "ghp_dev_token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "ghp_rev_token")

	// Simulate a repo without .golemic/ and without config
	exec := fakeExecutorOK()
	p, homeDir, repoRoot := setupPreflight(t, exec)

	projectName := filepath.Base(repoRoot)

	// No .golemic/ directory, no config, no credentials
	// All checks except gh, pi, git should fail
	var buf bytes.Buffer
	p.SetStdout(&buf)
	results := p.RunAll()

	// results[3] = checkScaffolding (should be FEHLT — created)
	if results[3].Ok {
		t.Errorf("scaffolding check should report FEHLT (created), got OK")
	}

	// results[4] = checkConfig (should be FEHLT — empty verify_command in template)
	if results[4].Ok {
		t.Errorf("config check should fail (empty verify_command in template), got OK")
	}

	// Now fix config.json with proper project name
	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{
		"project": "`+projectName+`",
		"verify_command": "go test"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create valid credentials (checked by checkCredentialsScaffolding AND checkCredentials)
	credDir := filepath.Join(homeDir, ".golemic", projectName)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
		"dev_token": "ghp_dev_token",
		"reviewer_token": "ghp_rev_token"
	}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Second run — should now pass
	var buf2 bytes.Buffer
	p2 := New(exec, homeDir, repoRoot)
	p2.SetStdout(&buf2)
	results2 := p2.RunAll()

	if !results2.AllOK() {
		t.Errorf("second run should pass all checks, got failures:")
		for _, r := range results2 {
			if !r.Ok {
				t.Errorf("  - %s: %s", r.Name, r.Details)
			}
		}
	}
}

func TestSanitizeErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "ErrExit with stderr",
			err:  &ErrExit{ExitCode: 1, Stderr: "HTTP 401: token ghp_secret is invalid"},
			want: "exit code 1",
		},
		{
			name: "ErrExit without stderr",
			err:  &ErrExit{ExitCode: 127},
			want: "exit code 127",
		},
		{
			name: "plain error",
			err:  fmt.Errorf("something went wrong"),
			want: "something went wrong",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeErr(tt.err)
			if got != tt.want {
				t.Errorf("sanitizeErr(%v) = %q, want %q", tt.err, got, tt.want)
			}
			// Verify no token values leak
			if strings.Contains(got, "ghp_") || strings.Contains(got, "secret") {
				t.Errorf("sanitizeErr must not leak raw stderr, got: %q", got)
			}
		})
	}
}

func TestErrExit(t *testing.T) {
	err := &ErrExit{ExitCode: 1, Stderr: "something went wrong"}
	msg := err.Error()
	if !strings.Contains(msg, "exit code 1") {
		t.Errorf("ErrExit.Error() should contain 'exit code 1', got: %s", msg)
	}
	if !strings.Contains(msg, "something went wrong") {
		t.Errorf("ErrExit.Error() should contain stderr, got: %s", msg)
	}

	err2 := &ErrExit{ExitCode: 127}
	msg2 := err2.Error()
	if !strings.Contains(msg2, "exit code 127") {
		t.Errorf("ErrExit.Error() should contain 'exit code 127', got: %s", msg2)
	}
}

func TestPreflightCheckOrder(t *testing.T) {
	// AC-005: checkCredentialsScaffolding runs before checkCredentials
	// Ensure env vars match mock-recognizable tokens
	t.Setenv("GOLEMIC_DEV_TOKEN", "ghp_dev_token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "ghp_rev_token")

	exec := fakeExecutorOK()
	p, homeDir, repoRoot := setupPreflight(t, exec)

	projectName := filepath.Base(repoRoot)

	// Create config.json and credentials so all checks pass
	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{
		"project": "`+projectName+`",
		"verify_command": "go test"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	credDir := filepath.Join(homeDir, ".golemic", projectName)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
		"dev_token": "ghp_dev_token",
		"reviewer_token": "ghp_rev_token"
	}`), 0600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	p.SetStdout(&buf)
	results := p.RunAll()

	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d", len(results))
	}

	// Verify exact order (credentials scaffolding is transparent, not a separate check)
	expectedNames := []string{
		"gh installiert",
		"pi installiert",
		"git",
		".golemic/ Scaffolding",
		"config.json valide",
		"Credentials",
	}

	for i, expected := range expectedNames {
		if results[i].Name != expected {
			t.Errorf("result[%d].Name = %q, want %q", i, results[i].Name, expected)
		}
	}

	// Verify output order matches
	output := buf.String()
	prevIdx := -1
	for _, expected := range expectedNames {
		// Search for exact line: "OK: <name>\n" or "FAILED: <name> -"
		// Using " -" suffix for FAILED avoids "Credentials" matching
		// inside other check names.
		okLine := "OK: " + expected + "\n"
		failedLine := "FAILED: " + expected + " -"
		idxOK := strings.Index(output, okLine)
		idxFAILED := strings.Index(output, failedLine)
		idx := idxOK
		if idx < 0 || (idxFAILED >= 0 && idxFAILED < idx) {
			idx = idxFAILED
		}
		if idx < 0 {
			t.Errorf("output missing %q", expected)
			continue
		}
		if idx <= prevIdx {
			t.Errorf("output order violation: %q appears before previous check", expected)
		}
		prevIdx = idx
	}
}
