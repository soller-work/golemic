package preflight

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCredentials(t *testing.T) { //nolint:cyclop,funlen,gocognit,maintidx // moved verbatim; cyclomatic 45 and cognitive 79 exceed thresholds on the pre-existing table body
	tests := []struct {
		name       string
		setupExec  func() *fakeExecutor
		setupFunc  func(t *testing.T, homeDir, repoRoot string)
		wantOk     bool
		wantDetail string
	}{
		{
			name: "valid credentials with different logins",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "gh version 2.0.0", nil
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
						}
						return "", fmt.Errorf("not mocked")
					},
				}
			},
			setupFunc: func(t *testing.T, homeDir, repoRoot string) {
				// Create config.json
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

				// Create credentials
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
			},
			wantOk: true,
		},
		{
			name: "missing config prevents credentials check",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{}
			},
			setupFunc: func(t *testing.T, homeDir, repoRoot string) {
				// No config.json — check should fail gracefully
			},
			wantOk:     false,
			wantDetail: "cannot load config",
		},
		{
			name: "same login for dev and reviewer",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "gh version 2.0.0", nil
					},
					runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
						if name == "gh" && len(args) >= 1 && args[0] == "api" && args[1] == "user" {
							return `{"login":"same-bot"}`, nil
						}
						return "", fmt.Errorf("not mocked")
					},
				}
			},
			setupFunc: func(t *testing.T, homeDir, repoRoot string) {
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

				credDir := filepath.Join(homeDir, ".golemic", "test-project")
				if err := os.MkdirAll(credDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
					"dev_token": "ghp_same_token",
					"reviewer_token": "ghp_same_token"
				}`), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantOk: false,
			// same-bot returned for both tokens
			wantDetail: "same account",
		},
		{
			name: "dev token invalid",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "gh version 2.0.0", nil
					},
					runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
						if name == "gh" && len(args) >= 1 && args[0] == "api" && args[1] == "user" {
							token := env["GH_TOKEN"]
							if strings.Contains(token, "rev") {
								return `{"login":"reviewer-bot"}`, nil
							}
							// dev token returns error (invalid)
							return "", &ErrExit{ExitCode: 1, Stderr: "HTTP 401"}
						}
						return "", fmt.Errorf("not mocked")
					},
				}
			},
			setupFunc: func(t *testing.T, homeDir, repoRoot string) {
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

				credDir := filepath.Join(homeDir, ".golemic", "test-project")
				if err := os.MkdirAll(credDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
					"dev_token": "ghp_bad_token",
					"reviewer_token": "ghp_rev_token"
				}`), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantOk:     false,
			wantDetail: "dev token invalid",
		},
		{
			name: "reviewer token invalid",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "gh version 2.0.0", nil
					},
					runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
						if name == "gh" && len(args) >= 1 && args[0] == "api" && args[1] == "user" {
							token := env["GH_TOKEN"]
							if strings.Contains(token, "dev") {
								return `{"login":"dev-bot"}`, nil
							}
							// reviewer token returns error (invalid)
							return "", &ErrExit{ExitCode: 1, Stderr: "HTTP 401"}
						}
						return "", fmt.Errorf("not mocked")
					},
				}
			},
			setupFunc: func(t *testing.T, homeDir, repoRoot string) {
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

				credDir := filepath.Join(homeDir, ".golemic", "test-project")
				if err := os.MkdirAll(credDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
					"dev_token": "ghp_dev_token",
					"reviewer_token": "ghp_bad_token"
				}`), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantOk:     false,
			wantDetail: "reviewer token invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := tt.setupExec()
			homeDir := t.TempDir()
			repoRoot := t.TempDir()

			// Init git repo
			if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0755); err != nil {
				t.Fatal(err)
			}

			p := New(exec, homeDir, repoRoot)
			// Isolate from real env: inject empty lookup so file values are used.
			p.SetLookupEnv(func(string) (string, bool) { return "", false })
			tt.setupFunc(t, homeDir, repoRoot)

			result := p.checkCredentials()
			if result.Ok != tt.wantOk {
				t.Errorf("checkCredentials().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkCredentials().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}

			// Verify no token values leak
			if !tt.wantOk && strings.Contains(result.Details, "ghp_") {
				t.Errorf("error message must not contain token values, got: %q", result.Details)
			}
		})
	}
}

func TestCheckCredentialsNoTokenLeak(t *testing.T) {
	exec := fakeExecutorOK()
	p, homeDir, repoRoot := setupPreflight(t, exec)

	// Create config
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

	// Invalid credentials file with token-like values that should never appear in errors
	credDir := filepath.Join(homeDir, ".golemic", "test-project")
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
		"dev_token": "ghp_my_secret_dev_token_12345",
		"reviewer_token": "ghp_my_secret_reviewer_token_67890"
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	result := p.checkCredentials()
	// The file has group-readable permissions (0644), so the credentials loader
	// should fail with a permission error. Verify no token value leaks.
	if !result.Ok {
		if strings.Contains(result.Details, "ghp_") {
			t.Errorf("error message must not contain token values, got: %q", result.Details)
		}
		// The error should mention chmod 600
		if !strings.Contains(result.Details, "chmod 600") {
			t.Errorf("error should mention chmod 600 for insecure permissions, got: %q", result.Details)
		}
	}
}

func TestCheckCredentialsSanitizeErr(t *testing.T) {
	// Verify that sanitizeErr never forwards raw stderr (which could contain tokens)
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "gh version 2.0.0", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			// Simulate a gh api error with stderr that looks like a token
			return "", &ErrExit{ExitCode: 1, Stderr: "HTTP 401: token ghp_leaked_secret is invalid"}
		},
	}
	p, homeDir, repoRoot := setupPreflight(t, exec)

	// Create config
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

	// Create valid credentials
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

	result := p.checkCredentials()
	if !result.Ok {
		// The error message should say "exit code 1" but NOT contain the raw stderr
		if strings.Contains(result.Details, "ghp_") {
			t.Errorf("error message must not contain token values, got: %q", result.Details)
		}
		if strings.Contains(result.Details, "HTTP 401") {
			t.Errorf("error message must not contain raw stderr, got: %q", result.Details)
		}
		if !strings.Contains(result.Details, "exit code 1") {
			t.Errorf("error message should mention exit code, got: %q", result.Details)
		}
	}
}

func TestCheckCredentialsSourceInDetails(t *testing.T) { //nolint:cyclop // moved verbatim; cyclomatic complexity 15 exceeds threshold
	// Verify that successful credentials check includes source info in Details.
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "gh version 2.0.0", nil
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
			}
			return "", fmt.Errorf("not mocked")
		},
	}
	homeDir := t.TempDir()
	repoRoot := t.TempDir()

	// Init git repo
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create config.json
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

	// AC-002: Mixed literal + template — dev=file_literal, reviewer=template_env
	// Set MY_REV_TOKEN for the reviewer template
	t.Setenv("MY_REV_TOKEN", "ghp_rev_token")

	credDir := filepath.Join(homeDir, ".golemic", "test-project")
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	// dev_token is literal, reviewer_token is template
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
		"dev_token": "ghp_literal_dev",
		"reviewer_token": "${MY_REV_TOKEN}"
	}`), 0600); err != nil {
		t.Fatal(err)
	}

	p := New(exec, homeDir, repoRoot)
	p.SetLookupEnv(emptyLookup)
	result := p.checkCredentials()

	if !result.Ok {
		t.Fatalf("checkCredentials() should be OK, got: %s", result.Details)
	}
	if !strings.Contains(result.Details, "dev=file_literal") {
		t.Errorf("Details should contain dev=file_literal, got: %s", result.Details)
	}
	if !strings.Contains(result.Details, "reviewer=template_env") {
		t.Errorf("Details should contain reviewer=template_env, got: %s", result.Details)
	}
}

func TestCheckCredentialsTemplateErrorNoLeak(t *testing.T) {
	// When a template resolution error occurs during checkCredentials,
	// the result Details must not leak token values.
	exec := fakeExecutorOK()
	homeDir := t.TempDir()
	repoRoot := t.TempDir()

	// Init git repo
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create config.json
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

	// Create credentials with malformed template in dev_token
	credDir := filepath.Join(homeDir, ".golemic", "test-project")
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{
		"dev_token": "${UNCLOSED",
		"reviewer_token": "ghp_valid"
	}`), 0600); err != nil {
		t.Fatal(err)
	}

	p := New(exec, homeDir, repoRoot)
	p.SetLookupEnv(emptyLookup)
	result := p.checkCredentials()

	if result.Ok {
		t.Fatal("checkCredentials() should fail for malformed template")
	}
	if strings.Contains(result.Details, "ghp_") {
		t.Errorf("result Details must not contain token values, got: %q", result.Details)
	}
}

// TestCheck_IdenticalTokensRejectedLocally covers AC-002: in check mode, identical
// token values are rejected by local comparison with no gh api user call.
func TestCheck_IdenticalTokensRejectedLocally_AC002(t *testing.T) { //nolint:cyclop,gocognit // moved verbatim; cyclomatic 21 and cognitive 22 exceed thresholds on the pre-existing body
	ghApiCalled := false
	exec := &fakeExecutor{
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
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user" {
				ghApiCalled = true
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)

	// Create valid config
	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{"project":"test-project","verify_command":"go test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Credentials with identical token values
	credDir := filepath.Join(homeDir, ".golemic", "test-project")
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{"dev_token":"ghp_same_token","reviewer_token":"ghp_same_token"}`), 0600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	p.SetStdout(&buf)
	p.SetLookupEnv(func(string) (string, bool) { return "", false })
	results := p.Check()

	// Credentials check must fail
	if results[5].Ok {
		t.Error("credentials check should fail with identical tokens")
	}
	if !strings.Contains(results[5].Details, "tokens identical") {
		t.Errorf("details should mention 'tokens identical', got: %q", results[5].Details)
	}

	// gh api user must NOT have been called
	if ghApiCalled {
		t.Error("gh api user must not be called in check mode for token distinctness")
	}

	// Output must contain FAILED for credentials and final 'failed'
	out := buf.String()
	if !strings.Contains(out, "FAILED: Credentials") {
		t.Errorf("output missing FAILED: Credentials, got: %s", out)
	}
	if !strings.Contains(out, "\nfailed\n") {
		t.Errorf("output missing final 'failed' summary, got: %s", out)
	}
}

// TestCheck_MissingGolemicDir_WritesNothing covers AC-004: check mode with missing
// .golemic/ exits with FAILED and writes no files.
func TestCheck_MissingGolemicDir_WritesNothing_AC004(t *testing.T) { //nolint:cyclop // moved verbatim; cyclomatic complexity 12 exceeds threshold
	exec := &fakeExecutor{
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
	p := New(exec, homeDir, repoRoot)

	var buf bytes.Buffer
	p.SetStdout(&buf)
	results := p.Check()

	// Scaffolding check must fail
	if results[3].Ok {
		t.Error("scaffolding check should fail when .golemic/ is missing in check mode")
	}
	if !strings.Contains(results[3].Details, "missing") {
		t.Errorf("details should mention 'missing', got: %q", results[3].Details)
	}

	// .golemic/ must NOT have been created
	if _, err := os.Stat(filepath.Join(repoRoot, ".golemic")); !os.IsNotExist(err) {
		t.Error(".golemic/ must not be created in check mode")
	}

	// Output must contain FAILED for scaffolding
	out := buf.String()
	if !strings.Contains(out, "FAILED: .golemic/ Scaffolding") {
		t.Errorf("output missing FAILED: .golemic/ Scaffolding, got: %s", out)
	}
}
