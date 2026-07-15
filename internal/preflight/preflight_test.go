package preflight

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
			wantDetail: "fehlt",
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
		if !strings.Contains(result.Details, "fehlt") {
			t.Errorf("checkConfig missing file should say 'fehlt', got: %s", result.Details)
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
			wantDetail: "denselben Account",
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
			wantDetail: "dev token ungültig",
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
			wantDetail: "reviewer token ungültig",
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
	exec := fakeExecutorOK()
	p, homeDir, repoRoot := setupPreflight(t, exec)

	// Create valid config.json
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
	// Check output format
	if !strings.Contains(output, "OK: gh installiert") {
		t.Errorf("output missing 'OK: gh installiert', got: %s", output)
	}
	if !strings.Contains(output, "OK: pi installiert") {
		t.Errorf("output missing 'OK: pi installiert', got: %s", output)
	}
	if !strings.Contains(output, "OK: git") {
		t.Errorf("output missing 'OK: git', got: %s", output)
	}
	if !strings.Contains(output, "OK: .golemic/ Scaffolding") {
		t.Errorf("output missing 'OK: .golemic/ Scaffolding', got: %s", output)
	}
	if !strings.Contains(output, "OK: config.json valide") {
		t.Errorf("output missing 'OK: config.json valide', got: %s", output)
	}
	if !strings.Contains(output, "OK: Credentials") {
		t.Errorf("output missing 'OK: Credentials', got: %s", output)
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

	// Should have exactly 6 results
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
		"FEHLT: gh installiert",
		"FEHLT: pi installiert",
		"FEHLT: git",
		"FEHLT: .golemic/ Scaffolding",
		"FEHLT: config.json valide",
		"FEHLT: Credentials",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q, got: %s", expected, output)
		}
	}

	// Must NOT contain SUCCESS
	if strings.Contains(output, "SUCCESS") {
		t.Errorf("output must not contain SUCCESS when checks fail, got: %s", output)
	}
}

func TestRunAllScaffoldingFailThenFix(t *testing.T) {
	// Simulate a repo without .golemic/ and without config
	exec := fakeExecutorOK()
	p, homeDir, repoRoot := setupPreflight(t, exec)

	// No .golemic/ directory, no config, no credentials
	// All checks except gh, pi, git should fail
	var buf bytes.Buffer
	p.SetStdout(&buf)
	results := p.RunAll()

	// Check scaffolding was created
	if results[3].Ok {
		t.Errorf("scaffolding check should report FEHLT (created), got OK")
	}

	// Check that config validation fails (config was just created but may be
	// invalid because verify_command is empty)
	if results[4].Ok {
		t.Errorf("config check should fail (empty verify_command in template), got OK")
	}

	// Now fix config.json
	golemicDir := filepath.Join(repoRoot, ".golemic")
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