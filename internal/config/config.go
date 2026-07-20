package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config represents the structure of .golemic/config.json
type Config struct {
	Project          string               `json:"project"`
	VerifyCommand    string               `json:"verify_command"`
	Label            string               `json:"label"`
	Models           Models               `json:"models"`
	TimeoutMinutes   int                  `json:"timeout_minutes"`
	TimeoutSeconds   int                  `json:"timeout_seconds,omitempty"`
	CITimeoutMinutes int                  `json:"ci_timeout_minutes,omitempty"`
	RequireCIChecks  bool                 `json:"require_ci_checks,omitempty"`
	Telemetry        TelemetryConfig      `json:"telemetry"`
	CodebaseMemory   CodebaseMemoryConfig `json:"codebase_memory"`
}

// TelemetryConfig controls span telemetry emission.
type TelemetryConfig struct {
	Enabled bool `json:"enabled"`
}

// CodebaseMemoryConfig controls the optional codebase-memory indexing feature.
type CodebaseMemoryConfig struct {
	Enabled bool `json:"enabled"`
}

// configRaw is used for parsing to detect missing vs zero values
type configRaw struct {
	Project          *string              `json:"project"`
	VerifyCommand    *string              `json:"verify_command"`
	Label            *string              `json:"label"`
	Models           *modelsRaw           `json:"models"`
	TimeoutMinutes   *int                 `json:"timeout_minutes"`
	TimeoutSeconds   *int                 `json:"timeout_seconds"`
	CITimeoutMinutes *int                 `json:"ci_timeout_minutes"`
	RequireCIChecks  *bool                `json:"require_ci_checks"`
	Telemetry        *telemetryRaw        `json:"telemetry"`
	CodebaseMemory   *codebaseMemoryRaw   `json:"codebase_memory"`
}

// telemetryRaw is used for parsing the telemetry config block.
type telemetryRaw struct {
	Enabled *bool `json:"enabled"`
}

// codebaseMemoryRaw is used for parsing the codebase_memory config block.
type codebaseMemoryRaw struct {
	Enabled *bool `json:"enabled"`
}

// modelsRaw is used for parsing models to detect missing vs zero values
type modelsRaw struct {
	Dev      *string `json:"dev"`
	Reviewer *string `json:"reviewer"`
}

// Models contains the model configurations for different roles
type Models struct {
	Dev      string `json:"dev"`
	Reviewer string `json:"reviewer"`
}

// DefaultConfig returns a Config with default values, using the given project name.
// This is the single source of truth for default values, used both by the Loader
// and by preflight scaffolding.
func DefaultConfig(project string) *Config {
	return &Config{
		Project:          project,
		VerifyCommand:    "",
		Label:            "ready-for-agent",
		Models: Models{
			Dev:      "z-ai/glm-4.6",
			Reviewer: "deepseek/deepseek-v4-pro",
		},
		TimeoutMinutes:   30,
		CITimeoutMinutes: 15,
		Telemetry:        TelemetryConfig{Enabled: true},
	}
}

// Load loads and validates the config file from the given repository root
func Load(repoRoot string) (*Config, error) {
	configPath := filepath.Join(repoRoot, ".golemic", "config.json")

	// Check if file exists
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config file not found: %s: %w", configPath, os.ErrNotExist)
	}

	// Read file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Parse with strict field checking into raw struct (with pointers for missing detection)
	var raw configRaw
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return nil, fmt.Errorf("invalid JSON in config file %s at offset %d: %w",
				configPath, syntaxErr.Offset, err)
		}
		var unmarshalErr *json.UnmarshalTypeError
		if errors.As(err, &unmarshalErr) {
			return nil, fmt.Errorf("invalid type in config file %s at field %s: %w",
				configPath, unmarshalErr.Field, err)
		}
		return nil, fmt.Errorf("invalid JSON in config file %s: %w", configPath, err)
	}

	// Build final config
	config := &Config{}

	// Extract project (required)
	if raw.Project != nil {
		config.Project = *raw.Project
	}

	// Extract verify_command (required)
	if raw.VerifyCommand != nil {
		config.VerifyCommand = *raw.VerifyCommand
	}

	// Extract label (optional)
	if raw.Label != nil {
		config.Label = *raw.Label
	}

	// Extract models (optional)
	if raw.Models != nil {
		if raw.Models.Dev != nil {
			config.Models.Dev = *raw.Models.Dev
		}
		if raw.Models.Reviewer != nil {
			config.Models.Reviewer = *raw.Models.Reviewer
		}
	}

	// Extract timeout_minutes (optional)
	if raw.TimeoutMinutes != nil {
		config.TimeoutMinutes = *raw.TimeoutMinutes
	}

	// Extract timeout_seconds (optional; if > 0 overrides timeout_minutes in runner)
	if raw.TimeoutSeconds != nil {
		config.TimeoutSeconds = *raw.TimeoutSeconds
	}

	// Validate required fields
	if config.Project == "" {
		return nil, fmt.Errorf("required field 'project' is missing or empty in config file %s", configPath)
	}

	if config.VerifyCommand == "" {
		return nil, fmt.Errorf("required field 'verify_command' is missing or empty in config file %s", configPath)
	}

	// Apply defaults for missing/empty optional fields
	if config.Label == "" {
		config.Label = "ready-for-agent"
	}

	if config.Models.Dev == "" {
		config.Models.Dev = "z-ai/glm-4.6"
	}

	if config.Models.Reviewer == "" {
		config.Models.Reviewer = "deepseek/deepseek-v4-pro"
	}

	// Validate timeout_minutes if set (distinguish missing vs explicitly set to 0)
	if raw.TimeoutMinutes != nil {
		if config.TimeoutMinutes <= 0 {
			return nil, fmt.Errorf("field 'timeout_minutes' must be > 0, got %d in config file %s",
				config.TimeoutMinutes, configPath)
		}
	} else {
		// Field not present, apply default
		config.TimeoutMinutes = 30
	}

	// Validate timeout_seconds if explicitly set
	if raw.TimeoutSeconds != nil && config.TimeoutSeconds <= 0 {
		return nil, fmt.Errorf("field 'timeout_seconds' must be > 0, got %d in config file %s",
			config.TimeoutSeconds, configPath)
	}

	// Extract telemetry.enabled (optional; default true per BR-003 / D-007)
	if raw.Telemetry != nil && raw.Telemetry.Enabled != nil {
		config.Telemetry.Enabled = *raw.Telemetry.Enabled
	} else {
		config.Telemetry.Enabled = true
	}

	// Extract codebase_memory.enabled (optional; default false per BR-2)
	if raw.CodebaseMemory != nil && raw.CodebaseMemory.Enabled != nil {
		config.CodebaseMemory.Enabled = *raw.CodebaseMemory.Enabled
	}

	// Extract require_ci_checks (optional; default false)
	if raw.RequireCIChecks != nil {
		config.RequireCIChecks = *raw.RequireCIChecks
	}

	// Extract ci_timeout_minutes (optional)
	if raw.CITimeoutMinutes != nil {
		config.CITimeoutMinutes = *raw.CITimeoutMinutes
		if config.CITimeoutMinutes <= 0 {
			return nil, fmt.Errorf("field 'ci_timeout_minutes' must be > 0, got %d in config file %s",
				config.CITimeoutMinutes, configPath)
		}
	} else {
		config.CITimeoutMinutes = 15
	}

	return config, nil
}
