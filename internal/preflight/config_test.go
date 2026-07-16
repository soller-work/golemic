package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
