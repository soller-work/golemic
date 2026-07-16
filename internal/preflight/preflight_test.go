package preflight

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

func TestCheckGhVersion(t *testing.T) {
	tests := []struct {
		name       string
		setupExec  func() *fakeExecutor
		wantOk     bool
		wantDetail string
	}{
		{
			name: "gh installed",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "gh version 2.0.0", nil
					},
				}
			},
			wantOk: true,
		},
		{
			name: "gh not found",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", fmt.Errorf("executable file not found")
					},
				}
			},
			wantOk:     false,
			wantDetail: "not found",
		},
		{
			name: "gh exits with error",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", &ErrExit{ExitCode: 1, Stderr: "some error"}
					},
				}
			},
			wantOk:     false,
			wantDetail: "exited with code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := setupPreflight(t, tt.setupExec())
			result := p.checkGhVersion()
			if result.Ok != tt.wantOk {
				t.Errorf("checkGhVersion().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkGhVersion().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}
		})
	}
}

func TestCheckPiVersion(t *testing.T) {
	tests := []struct {
		name       string
		setupExec  func() *fakeExecutor
		wantOk     bool
		wantDetail string
	}{
		{
			name: "pi installed",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "pi version 1.0.0", nil
					},
				}
			},
			wantOk: true,
		},
		{
			name: "pi not found",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", fmt.Errorf("executable file not found")
					},
				}
			},
			wantOk:     false,
			wantDetail: "not found",
		},
		{
			name: "pi exits with error",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", &ErrExit{ExitCode: 1, Stderr: "some error"}
					},
				}
			},
			wantOk:     false,
			wantDetail: "exited with code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := setupPreflight(t, tt.setupExec())
			result := p.checkPiVersion()
			if result.Ok != tt.wantOk {
				t.Errorf("checkPiVersion().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkPiVersion().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}
		})
	}
}

func TestCheckGit(t *testing.T) {
	tests := []struct {
		name       string
		setupExec  func() *fakeExecutor
		wantOk     bool
		wantDetail string
	}{
		{
			name: "git ok with HTTPS remote",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						switch {
						case name == "git" && len(args) >= 1 && args[0] == "config":
							return "https://github.com/owner/repo.git", nil
						case name == "git" && len(args) >= 1 && args[0] == "worktree":
							return "/tmp/repo (main)\n", nil
						default:
							return "git version 2.0.0", nil
						}
					},
				}
			},
			wantOk: true,
		},
		{
			name: "git not found",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", fmt.Errorf("executable file not found")
					},
				}
			},
			wantOk:     false,
			wantDetail: "not found",
		},
		{
			name: "git worktree list fails",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						if callCount == 1 {
							return "git version 2.0.0", nil // git --version ok
						}
						return "", &ErrExit{ExitCode: 128, Stderr: "fatal: not a git repository"} // worktree fails
					},
				}
			},
			wantOk:     false,
			wantDetail: "git worktree list failed",
		},
		{
			name: "no remote origin",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "", &ErrExit{ExitCode: 1, Stderr: "fatal: not in a git directory"}
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "no remote 'origin'",
		},
		{
			name: "SSH remote URL (git@)",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "git@github.com:owner/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "SSH",
		},
		{
			name: "SSH remote URL (ssh://)",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "ssh://git@github.com/owner/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "SSH",
		},
		{
			name: "SSH remote URL (git+ssh://)",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "git+ssh://git@github.com/owner/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "SSH",
		},
		{
			name: "non-HTTPS, non-SSH URL",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "file:///local/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "HTTPS",
		},
		{
			name: "HTTPS URL with embedded token passes",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "https://x-access-token:ghp_secret123@github.com/owner/repo.git", nil
						}
					},
				}
			},
			wantOk: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := setupPreflight(t, tt.setupExec())
			result := p.checkGit()
			if result.Ok != tt.wantOk {
				t.Errorf("checkGit().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkGit().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}

			// Verify no token in error output
			if !result.Ok && strings.Contains(result.Details, "ghp_") {
				t.Errorf("error message must not contain token values, got: %q", result.Details)
			}
		})
	}
}

func TestCheckGitTokenLeak(t *testing.T) {
	// Special test: ensure that a token in an HTTPS URL with credentials
	// is masked in the error output, and that a plain HTTPS URL with token
	// still passes.
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "config":
				return "https://x-access-token:ghp_my_secret@github.com/owner/repo.git", nil
			case name == "git" && len(args) >= 1 && args[0] == "worktree":
				return "/tmp/repo (main)\n", nil
			default:
				return "git version 2.0.0", nil
			}
		},
	}
	p, _, _ := setupPreflight(t, exec)
	result := p.checkGit()
	if !result.Ok {
		t.Errorf("HTTPS URL with embedded token should pass, got FEHLT: %s", result.Details)
	}
	if result.Ok && strings.Contains(result.Details, "ghp_") {
		t.Errorf("error message must not contain token values, got: %q", result.Details)
	}
}

func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://github.com/owner/repo.git", false},
		{"git@github.com:owner/repo.git", true},
		{"ssh://git@github.com/owner/repo.git", true},
		{"git://github.com/owner/repo.git", true},
		{"git+ssh://git@github.com/owner/repo.git", true},
		{"http://github.com/owner/repo.git", false},
		{"file:///local/repo.git", false},
		{"", false},
		{"https://x-access-token:secret@github.com/owner/repo.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := isSSHURL(tt.url)
			if got != tt.want {
				t.Errorf("isSSHURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestMaskURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"https://user:pass@github.com/owner/repo.git", "https://***@github.com/owner/repo.git"},
		{"git@github.com:owner/repo.git", "git@github.com:owner/repo.git"},
		{"https://token@github.com/owner/repo.git", "https://***@github.com/owner/repo.git"},
		{"https://x-access-token:ghp_secret@github.com/owner/repo.git", "https://***@github.com/owner/repo.git"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := maskURL(tt.url)
			if got != tt.want {
				t.Errorf("maskURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestCheckScaffolding(t *testing.T) {
	tests := []struct {
		name        string
		setupRepo   func(t *testing.T, repoRoot string)
		wantOk      bool
		wantCreated bool // true if we expect scaffolding to be created
	}{
		{
			name:        "scaffolding missing - creates it",
			setupRepo:   func(t *testing.T, repoRoot string) {},
			wantOk:      false,
			wantCreated: true,
		},
		{
			name: "config.json already exists",
			setupRepo: func(t *testing.T, repoRoot string) {
				golemicDir := filepath.Join(repoRoot, ".golemic")
				if err := os.MkdirAll(golemicDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{"project":"test"}`), 0644); err != nil {
					t.Fatal(err)
				}
			},
			wantOk:      true,
			wantCreated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := fakeExecutorOK()
			p, _, repoRoot := setupPreflight(t, exec)
			tt.setupRepo(t, repoRoot)

			result := p.checkScaffolding()

			if result.Ok != tt.wantOk {
				t.Errorf("checkScaffolding().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}

			// Verify created files
			golemicDir := filepath.Join(repoRoot, ".golemic")
			configPath := filepath.Join(golemicDir, "config.json")
			devPath := filepath.Join(golemicDir, "guidelines", "dev.md")
			revPath := filepath.Join(golemicDir, "guidelines", "reviewer.md")

			if tt.wantCreated {
				// Check config.json exists and is valid JSON
				data, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("config.json should exist: %v", err)
				}
				// Verify it's valid JSON
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Errorf("config.json is not valid JSON: %v\ncontent: %s", err, string(data))
				}
				// Verify project field exists
				project, ok := parsed["project"]
				if !ok || project == "" {
					t.Errorf("config.json should contain project field, got: %s", string(data))
				}

				// Check guidelines exist
				if _, err := os.Stat(devPath); err != nil {
					t.Errorf("guidelines/dev.md should exist: %v", err)
				}
				if _, err := os.Stat(revPath); err != nil {
					t.Errorf("guidelines/reviewer.md should exist: %v", err)
				}
			}
		})
	}
}

func TestCheckScaffoldingInvalidProjectName(t *testing.T) {
	tests := []struct {
		name       string
		repoRoot   string // repo root path (may not exist, basename is what matters)
		wantDetail string
	}{
		{name: "empty name", repoRoot: "", wantDetail: "cannot determine project name"},
		{name: "leading dot", repoRoot: "/tmp/.foo", wantDetail: "invalid project name"},
		{name: "space in name", repoRoot: "/tmp/my repo", wantDetail: "invalid project name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := fakeExecutorOK()
			homeDir := t.TempDir()

			p := New(exec, homeDir, tt.repoRoot)
			result := p.checkScaffolding()
			if result.Ok {
				t.Errorf("checkScaffolding() should fail for repo root %q", tt.repoRoot)
			}
			if !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkScaffolding().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}

			// Verify no files were created (for the empty-root case, .golemic
			// would be relative to CWD; skip that check)
			if tt.repoRoot != "" {
				golemicDir := filepath.Join(tt.repoRoot, ".golemic")
				if _, err := os.Stat(golemicDir); err == nil {
					t.Errorf("no .golemic directory should be created for invalid project name")
				}
			}
		})
	}
}

func TestCheckScaffoldingIdempotent(t *testing.T) {
	// First run: create scaffolding
	exec := fakeExecutorOK()
	p, _, repoRoot := setupPreflight(t, exec)

	result1 := p.checkScaffolding()
	if result1.Ok {
		t.Errorf("first run should report FEHLT (scaffolding created), got OK")
	}

	// Verify files were created
	golemicDir := filepath.Join(repoRoot, ".golemic")
	configPath := filepath.Join(golemicDir, "config.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("first run should create config.json: %v", err)
	}

	// Second run: should report OK (idempotent)
	result2 := p.checkScaffolding()
	if !result2.Ok {
		t.Errorf("second run should report OK (idempotent), got: %s", result2.Details)
	}

	// Verify files were NOT overwritten
	configData2, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json should still exist: %v", err)
	}
	if string(configData) != string(configData2) {
		t.Errorf("config.json was overwritten on second run")
	}
}

func TestCheckScaffoldingDoesNotOverwriteExistingFiles(t *testing.T) {
	exec := fakeExecutorOK()
	p, _, repoRoot := setupPreflight(t, exec)

	// Pre-create .golemic/config.json with different content
	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	originalContent := `{"project":"custom-project","verify_command":"make test"}`
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(originalContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-create guidelines/dev.md with human content
	guidelinesDir := filepath.Join(golemicDir, "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	devContent := "# Custom Dev Guidelines\n\nManually edited by human."
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte(devContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := p.checkScaffolding()
	if !result.Ok {
		t.Errorf("scaffolding check should be OK when all files exist, got: %s", result.Details)
	}

	// Verify config.json was NOT overwritten
	data, err := os.ReadFile(filepath.Join(golemicDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != originalContent {
		t.Errorf("config.json was overwritten:\noriginal: %s\nnow: %s", originalContent, string(data))
	}

	// Verify dev.md was NOT overwritten
	devData, err := os.ReadFile(filepath.Join(guidelinesDir, "dev.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(devData) != devContent {
		t.Errorf("guidelines/dev.md was overwritten")
	}
}

func TestCheckConfig(t *testing.T) {
	tests := []struct {
		name       string
		setupRepo  func(t *testing.T, repoRoot string)
		wantOk     bool
		wantDetail string
	}{
		{
			name: "valid config.json",
			setupRepo: func(t *testing.T, repoRoot string) {
				golemicDir := filepath.Join(repoRoot, ".golemic")
				if err := os.MkdirAll(golemicDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{
					"project": "test-project",
					"verify_command": "go test ./..."
				}`), 0644); err != nil {
					t.Fatal(err)
				}
			},
			wantOk: true,
		},
		{
			name: "missing config.json",
			setupRepo: func(t *testing.T, repoRoot string) {
				// No .golemic directory
			},
			wantOk:     false,
			wantDetail: "missing",
		},
		{
			name: "invalid config.json",
			setupRepo: func(t *testing.T, repoRoot string) {
				golemicDir := filepath.Join(repoRoot, ".golemic")
				if err := os.MkdirAll(golemicDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{
					"project": "",
					"verify_command": ""
				}`), 0644); err != nil {
					t.Fatal(err)
				}
			},
			wantOk:     false,
			wantDetail: "project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := fakeExecutorOK()
			p, _, repoRoot := setupPreflight(t, exec)
			tt.setupRepo(t, repoRoot)

			result := p.checkConfig()
			if result.Ok != tt.wantOk {
				t.Errorf("checkConfig().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkConfig().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}
		})
	}
}

func TestCheckConfigErrorsIsNotExist(t *testing.T) {
	// Verify that config.Load properly wraps os.ErrNotExist so errors.Is works
	p, _, _ := setupPreflight(t, fakeExecutorOK())
	result := p.checkConfig()
	if !result.Ok {
		if !strings.Contains(result.Details, "missing") {
			t.Errorf("checkConfig missing file should say 'missing', got: %s", result.Details)
		}
	}
}

func TestCheckCredentials(t *testing.T) {
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
			// Isolate from environment: env vars take precedence over file values.
			t.Setenv("GOLEMIC_DEV_TOKEN", "ghp_dev_token")
			t.Setenv("GOLEMIC_REVIEWER_TOKEN", "ghp_rev_token")

			exec := tt.setupExec()
			homeDir := t.TempDir()
			repoRoot := t.TempDir()

			// Init git repo
			if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0755); err != nil {
				t.Fatal(err)
			}

			p := New(exec, homeDir, repoRoot)
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

// =============================================================================
// writeFileAtomic tests (shared helper)
// =============================================================================

func TestWriteFileAtomicCreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")
	content := []byte(`{"key": "value"}`)

	if err := writeFileAtomic(path, content, 0644); err != nil {
		t.Fatalf("writeFileAtomic() unexpected error: %v", err)
	}

	// Verify file exists with correct content
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("file content = %q, want %q", string(got), string(content))
	}
}

func TestWriteFileAtomicDoesNotOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")
	original := []byte(`"original content"`)

	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	// Second write should fail with fs.ErrExist
	err := writeFileAtomic(path, []byte(`"new content"`), 0644)
	if err == nil {
		t.Fatal("writeFileAtomic() should fail when file exists")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("writeFileAtomic() error should wrap fs.ErrExist, got: %v", err)
	}

	// Verify original content was not overwritten
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("file was overwritten: got %q, want %q", string(got), string(original))
	}
}

func TestWriteFileAtomicPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")

	if err := writeFileAtomic(path, []byte(`{}`), 0600); err != nil {
		t.Fatalf("writeFileAtomic() unexpected error: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	perm := fi.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = 0%o, want 0600", perm)
	}
}

func TestWriteFileAtomicCreatesMissingParent(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "a", "b", "c")
	path := filepath.Join(nestedDir, "test.json")

	// Verify parent does NOT exist before write
	if _, err := os.Stat(nestedDir); err == nil {
		t.Fatal("nested dir should not exist before write")
	}

	if err := writeFileAtomic(path, []byte(`{}`), 0600); err != nil {
		t.Fatalf("writeFileAtomic() unexpected error: %v", err)
	}

	// Verify parent directories were created with 0755
	dirInfo, err := os.Stat(nestedDir)
	if err != nil {
		t.Fatalf("parent directory should exist after write: %v", err)
	}
	if !dirInfo.IsDir() {
		t.Errorf("parent path should be a directory")
	}
	// 0755 on macOS often reports as drwxr-xr-x; check the permission bits
	if dirInfo.Mode().Perm()&0755 != 0755 {
		t.Errorf("parent directory permissions too restrictive: 0%o", dirInfo.Mode().Perm())
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("created file should exist: %v", err)
	}
}

func TestWriteFileAtomicReused(t *testing.T) {
	// AC-007: createConfig uses the shared writeFileAtomic helper.
	// Credentials scaffolding is now inline in checkCredentials (transparent side effect),
	// no longer a separate function to test here.

	exec := fakeExecutorOK()
	p, _, repoRoot := setupPreflight(t, exec)

	// ===== config.json via createConfig (uses writeFileAtomic with 0644) =====
	golemicDir := filepath.Join(repoRoot, ".golemic")
	configPath := filepath.Join(golemicDir, "config.json")
	projectName := filepath.Base(repoRoot)

	if err := p.createConfig(golemicDir, configPath, projectName); err != nil {
		t.Fatalf("createConfig() unexpected error: %v", err)
	}

	// Verify config.json permissions are 0644
	cfgFi, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgFi.Mode().Perm() != 0644 {
		t.Errorf("config.json perms = 0%o, want 0644", cfgFi.Mode().Perm())
	}

	// Verify idempotency: second call should not overwrite
	if err := p.createConfig(golemicDir, configPath, projectName); err != nil {
		t.Errorf("createConfig() second call (idempotent) should succeed, got: %v", err)
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
func TestCheckCredentialsSourceInDetails(t *testing.T) {
	// Verify that successful credentials check includes source info in Details.
	cu1 := unsetEnvPreflight(t, "GOLEMIC_DEV_TOKEN")
	cu2 := unsetEnvPreflight(t, "GOLEMIC_REVIEWER_TOKEN")
	defer cu1()
	defer cu2()

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
	cu1 := unsetEnvPreflight(t, "GOLEMIC_DEV_TOKEN")
	cu2 := unsetEnvPreflight(t, "GOLEMIC_REVIEWER_TOKEN")
	defer cu1()
	defer cu2()

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
func TestCheck_IdenticalTokensRejectedLocally_AC002(t *testing.T) {
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
func TestCheck_MissingGolemicDir_WritesNothing_AC004(t *testing.T) {
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
