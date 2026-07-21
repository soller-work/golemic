package preflight

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/agentfile"
)

func TestCheckScaffolding(t *testing.T) { //nolint:cyclop,gocognit // moved verbatim; cyclomatic 12 and cognitive 32 exceed thresholds on the pre-existing table body
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

			if tt.wantCreated { //nolint:nestif // moved verbatim; complexity pre-dates split
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

				// Check agents exist with model frontmatter and non-empty body
				agentsDir := filepath.Join(golemicDir, "agents")
				for _, role := range []string{"dev", "reviewer"} {
					chain, body, readErr := agentfile.Read(filepath.Join(agentsDir, role+".md"))
					if readErr != nil {
						t.Errorf("agents/%s.md: %v", role, readErr)
						continue
					}
					if len(chain) == 0 {
						t.Errorf("agents/%s.md: model chain must not be empty", role)
					}
					if body == "" {
						t.Errorf("agents/%s.md: body must not be empty", role)
					}
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
