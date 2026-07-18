package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name          string
		configContent string
		wantErr       bool
		errContains   string
		validate      func(*testing.T, *Config)
	}{
		{
			name: "valid minimal config - only required fields",
			configContent: `{
				"project": "test-project",
				"verify_command": "go test ./..."
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Project != "test-project" {
					t.Errorf("Project = %v, want test-project", cfg.Project)
				}
				if cfg.VerifyCommand != "go test ./..." {
					t.Errorf("VerifyCommand = %v, want go test ./...", cfg.VerifyCommand)
				}
				// Check defaults
				if cfg.Label != "ready-for-agent" {
					t.Errorf("Label = %v, want ready-for-agent", cfg.Label)
				}
				if cfg.Models.Dev != "z-ai/glm-4.6" {
					t.Errorf("Models.Dev = %v, want z-ai/glm-4.6", cfg.Models.Dev)
				}
				if cfg.Models.Reviewer != "deepseek/deepseek-v4-pro" {
					t.Errorf("Models.Reviewer = %v, want deepseek/deepseek-v4-pro", cfg.Models.Reviewer)
				}
				if cfg.TimeoutMinutes != 30 {
					t.Errorf("TimeoutMinutes = %v, want 30", cfg.TimeoutMinutes)
				}
			},
		},
		{
			name: "valid full config - all fields set",
			configContent: `{
				"project": "my-project",
				"verify_command": "npm test",
				"label": "custom-label",
				"models": {
					"dev": "custom/dev-model",
					"reviewer": "custom/reviewer-model"
				},
				"timeout_minutes": 45
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Project != "my-project" {
					t.Errorf("Project = %v, want my-project", cfg.Project)
				}
				if cfg.VerifyCommand != "npm test" {
					t.Errorf("VerifyCommand = %v, want npm test", cfg.VerifyCommand)
				}
				if cfg.Label != "custom-label" {
					t.Errorf("Label = %v, want custom-label", cfg.Label)
				}
				if cfg.Models.Dev != "custom/dev-model" {
					t.Errorf("Models.Dev = %v, want custom/dev-model", cfg.Models.Dev)
				}
				if cfg.Models.Reviewer != "custom/reviewer-model" {
					t.Errorf("Models.Reviewer = %v, want custom/reviewer-model", cfg.Models.Reviewer)
				}
				if cfg.TimeoutMinutes != 45 {
					t.Errorf("TimeoutMinutes = %v, want 45", cfg.TimeoutMinutes)
				}
			},
		},
		{
			name:          "missing config file",
			configContent: "",
			wantErr:       true,
			errContains:   "config file not found",
		},
		{
			name: "broken JSON - missing closing brace",
			configContent: `{
				"project": "test",
				"verify_command": "go test"
			`,
			wantErr:     true,
			errContains: "invalid JSON",
		},
		{
			name: "broken JSON - invalid syntax",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"extra": unclosed string
			}`,
			wantErr:     true,
			errContains: "invalid JSON",
		},
		{
			name: "unknown top-level field",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"unknown_field": "value"
			}`,
			wantErr:     true,
			errContains: "unknown_field",
		},
		{
			name: "missing project field",
			configContent: `{
				"verify_command": "go test"
			}`,
			wantErr:     true,
			errContains: "project",
		},
		{
			name: "empty project field",
			configContent: `{
				"project": "",
				"verify_command": "go test"
			}`,
			wantErr:     true,
			errContains: "project",
		},
		{
			name: "missing verify_command field",
			configContent: `{
				"project": "test"
			}`,
			wantErr:     true,
			errContains: "verify_command",
		},
		{
			name: "empty verify_command field",
			configContent: `{
				"project": "test",
				"verify_command": ""
			}`,
			wantErr:     true,
			errContains: "verify_command",
		},
		{
			name: "timeout_minutes explicitly set to 0",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"timeout_minutes": 0
			}`,
			wantErr:     true,
			errContains: "timeout_minutes",
		},
		{
			name: "timeout_minutes negative value",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"timeout_minutes": -5
			}`,
			wantErr:     true,
			errContains: "timeout_minutes",
		},
		{
			name: "timeout_minutes explicitly set to valid value",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"timeout_minutes": 60
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.TimeoutMinutes != 60 {
					t.Errorf("TimeoutMinutes = %v, want 60", cfg.TimeoutMinutes)
				}
			},
		},
		{
			name: "timeout_seconds valid overrides minutes",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"timeout_seconds": 5
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.TimeoutSeconds != 5 {
					t.Errorf("TimeoutSeconds = %v, want 5", cfg.TimeoutSeconds)
				}
				if cfg.TimeoutMinutes != 30 {
					t.Errorf("TimeoutMinutes = %v, want 30 (default)", cfg.TimeoutMinutes)
				}
			},
		},
		{
			name: "timeout_seconds zero is rejected",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"timeout_seconds": 0
			}`,
			wantErr:     true,
			errContains: "field 'timeout_seconds' must be > 0, got 0",
		},
		{
			name: "empty label gets default",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"label": ""
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Label != "ready-for-agent" {
					t.Errorf("Label = %v, want ready-for-agent (default)", cfg.Label)
				}
			},
		},
		{
			name: "empty models.dev gets default",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"models": {
					"dev": "",
					"reviewer": "custom/reviewer"
				}
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Models.Dev != "z-ai/glm-4.6" {
					t.Errorf("Models.Dev = %v, want z-ai/glm-4.6 (default)", cfg.Models.Dev)
				}
				if cfg.Models.Reviewer != "custom/reviewer" {
					t.Errorf("Models.Reviewer = %v, want custom/reviewer", cfg.Models.Reviewer)
				}
			},
		},
		{
			name: "empty models.reviewer gets default",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"models": {
					"dev": "custom/dev",
					"reviewer": ""
				}
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.Models.Dev != "custom/dev" {
					t.Errorf("Models.Dev = %v, want custom/dev", cfg.Models.Dev)
				}
				if cfg.Models.Reviewer != "deepseek/deepseek-v4-pro" {
					t.Errorf("Models.Reviewer = %v, want deepseek/deepseek-v4-pro (default)", cfg.Models.Reviewer)
				}
			},
		},
		// AC-008: ci_timeout_minutes config tests
		{
			name: "ci_timeout_minutes absent defaults to 15",
			configContent: `{
				"project": "test",
				"verify_command": "go test"
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.CITimeoutMinutes != 15 {
					t.Errorf("CITimeoutMinutes = %d, want 15 (default)", cfg.CITimeoutMinutes)
				}
			},
		},
		{
			name: "ci_timeout_minutes zero is rejected",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"ci_timeout_minutes": 0
			}`,
			wantErr:     true,
			errContains: "ci_timeout_minutes",
		},
		{
			name: "ci_timeout_minutes negative is rejected",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"ci_timeout_minutes": -5
			}`,
			wantErr:     true,
			errContains: "ci_timeout_minutes",
		},
		{
			name: "ci_timeout_minutes valid value is accepted",
			configContent: `{
				"project": "test",
				"verify_command": "go test",
				"ci_timeout_minutes": 30
			}`,
			wantErr: false,
			validate: func(t *testing.T, cfg *Config) {
				if cfg.CITimeoutMinutes != 30 {
					t.Errorf("CITimeoutMinutes = %d, want 30", cfg.CITimeoutMinutes)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory for test
			tmpDir := t.TempDir()

			// Create .golemic subdirectory
			golemicDir := filepath.Join(tmpDir, ".golemic")
			if err := os.MkdirAll(golemicDir, 0755); err != nil {
				t.Fatalf("failed to create .golemic directory: %v", err)
			}

			// Write config file if content is provided
			if tt.configContent != "" {
				configPath := filepath.Join(golemicDir, "config.json")
				if err := os.WriteFile(configPath, []byte(tt.configContent), 0644); err != nil {
					t.Fatalf("failed to write config file: %v", err)
				}
			}

			// Load config
			cfg, err := Load(tmpDir)

			// Check error expectations
			if tt.wantErr {
				if err == nil {
					t.Errorf("Load() expected error, got nil")
					return
				}
				if tt.errContains != "" {
					// Check that the error message contains the expected substring
					errMsg := err.Error()
					if !strings.Contains(errMsg, tt.errContains) {
						t.Errorf("Load() error = %v, expected to contain %q", err, tt.errContains)
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Load() unexpected error: %v", err)
				return
			}

			// Run validation function if provided
			if tt.validate != nil {
				tt.validate(t, cfg)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig("my-project")
	if cfg.Project != "my-project" {
		t.Errorf("Project = %q, want %q", cfg.Project, "my-project")
	}
	if cfg.VerifyCommand != "" {
		t.Errorf("VerifyCommand = %q, want empty", cfg.VerifyCommand)
	}
	if cfg.Label != "ready-for-agent" {
		t.Errorf("Label = %q, want %q", cfg.Label, "ready-for-agent")
	}
	if cfg.Models.Dev != "z-ai/glm-4.6" {
		t.Errorf("Models.Dev = %q, want %q", cfg.Models.Dev, "z-ai/glm-4.6")
	}
	if cfg.Models.Reviewer != "deepseek/deepseek-v4-pro" {
		t.Errorf("Models.Reviewer = %q, want %q", cfg.Models.Reviewer, "deepseek/deepseek-v4-pro")
	}
	if cfg.TimeoutMinutes != 30 {
		t.Errorf("TimeoutMinutes = %d, want %d", cfg.TimeoutMinutes, 30)
	}
	if cfg.CITimeoutMinutes != 15 {
		t.Errorf("CITimeoutMinutes = %d, want %d", cfg.CITimeoutMinutes, 15)
	}
}

func TestLoadErrorsIsNotExist(t *testing.T) {
	// Verify that Load returns an error that wraps os.ErrNotExist
	tmpDir := t.TempDir()
	_, err := Load(tmpDir)
	if err == nil {
		t.Fatal("Load() expected error for missing config, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("errors.Is(err, os.ErrNotExist) = false for missing config file; err: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Telemetry config tests
// ---------------------------------------------------------------------------

func TestLoad_TelemetryEnabled_Default(t *testing.T) {
	dir := t.TempDir()
	cfg := writeAndLoad(t, dir, `{"project":"p","verify_command":"go test"}`)
	if !cfg.Telemetry.Enabled {
		t.Error("Telemetry.Enabled must default to true when field is absent")
	}
}

func TestLoad_TelemetryEnabled_ExplicitTrue(t *testing.T) {
	dir := t.TempDir()
	cfg := writeAndLoad(t, dir, `{"project":"p","verify_command":"go test","telemetry":{"enabled":true}}`)
	if !cfg.Telemetry.Enabled {
		t.Error("Telemetry.Enabled must be true when explicitly set to true")
	}
}

func TestLoad_TelemetryEnabled_ExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	cfg := writeAndLoad(t, dir, `{"project":"p","verify_command":"go test","telemetry":{"enabled":false}}`)
	if cfg.Telemetry.Enabled {
		t.Error("Telemetry.Enabled must be false when explicitly set to false")
	}
}

func TestDefaultConfig_TelemetryEnabled(t *testing.T) {
	cfg := DefaultConfig("proj")
	if !cfg.Telemetry.Enabled {
		t.Error("DefaultConfig must set Telemetry.Enabled to true")
	}
}

// writeAndLoad writes configJSON into dir/.golemic/config.json and calls Load.
func writeAndLoad(t *testing.T, dir, configJSON string) *Config {
	t.Helper()
	golemicDir := filepath.Join(dir, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}
