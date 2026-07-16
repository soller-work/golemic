// Package harness provides the test harness for spawning and managing
// golemic subprocesses in E2E tests.
//
// IF-001: GollemicRunner loads golemic_e2e config and tokens, then spawns
// the golemic binary as a subprocess, capturing output while redacting tokens.
package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golemic/internal/config"
	"golemic/internal/credentials"
)

// GollemicRunner manages golemic subprocess execution for E2E tests.
// It loads configuration and credentials from the golemic_e2e sandbox
// and enforces token redaction (BR-003).
type GollemicRunner struct {
	e2ePath       string
	golemicBinary string
	cfg           *config.Config
	creds         *credentials.Credentials
	homeDir       string
}

// RunResult holds the captured output and exit code from a subprocess execution.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// NewRunner creates a new GollemicRunner.
//
// It performs preflight validation (BR-004):
//   - golemic_e2e directory must exist and contain .golemic/config.json
//   - config.json must be valid
//   - GOLEMIC_DEV_TOKEN and GOLEMIC_REVIEWER_TOKEN must be set (env or credentials file)
//
// Returns an error if any validation fails.
func NewRunner(golemicE2EPath, golemicBinary string) (*GollemicRunner, error) {
	// Validate golemic_e2e path.
	info, err := os.Stat(golemicE2EPath)
	if err != nil {
		return nil, fmt.Errorf("golemic_e2e path not accessible: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("golemic_e2e path is not a directory: %s", golemicE2EPath)
	}

	// Validate golemic binary.
	if _, err := os.Stat(golemicBinary); err != nil {
		return nil, fmt.Errorf("golemic binary not found at %s: %w", golemicBinary, err)
	}

	// Load config (BR-004: fail fast if invalid).
	cfg, err := config.Load(golemicE2EPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Determine home directory for credentials loading.
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory: %w", err)
		}
	}

	// Load credentials.
	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(cfg.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials: %w", err)
	}

	return &GollemicRunner{
		e2ePath:       golemicE2EPath,
		golemicBinary: golemicBinary,
		cfg:           cfg,
		creds:         creds,
		homeDir:       homeDir,
	}, nil
}

// Config returns the loaded configuration.
func (r *GollemicRunner) Config() *config.Config { return r.cfg }

// Credentials returns the loaded credentials (tokens).
func (r *GollemicRunner) Credentials() *credentials.Credentials { return r.creds }

// E2EPath returns the golemic_e2e working directory path.
func (r *GollemicRunner) E2EPath() string { return r.e2ePath }

// HomeDir returns the home directory used for credential resolution.
func (r *GollemicRunner) HomeDir() string { return r.homeDir }

// Exec spawns the golemic binary as a subprocess with the given arguments.
//
// It sets up the environment:
//   - Working directory: golemic_e2e path
//   - HOME: runner's home directory
//   - GOLEMIC_DEV_TOKEN and GOLEMIC_REVIEWER_TOKEN: from credentials
//   - GH_TOKEN: unset (golemic manages token switching internally)
//
// If ctx has no deadline and config specifies a timeout, wraps ctx with
// context.WithTimeout using config.TimeoutMinutes (IC-002 compliance).
//
// Returns RunResult with captured stdout, stderr, and exit code.
// The output is redacted (BR-003): token values are replaced with ***REDACTED***.
func (r *GollemicRunner) Exec(ctx context.Context, args ...string) (*RunResult, error) {
	// Apply config timeout if context has no deadline (IC-002).
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && r.cfg.TimeoutMinutes > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(r.cfg.TimeoutMinutes)*time.Minute)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, r.golemicBinary, args...)

	// Set working directory to golemic_e2e.
	cmd.Dir = r.e2ePath

	// Build environment: inherit current env and add/override required vars.
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env,
		"HOME="+r.homeDir,
		"GOLEMIC_DEV_TOKEN="+r.creds.DevToken(),
		"GOLEMIC_REVIEWER_TOKEN="+r.creds.ReviewerToken(),
	)

	// Remove GH_TOKEN from environment to avoid conflict with golemic's
	// internal token management (the runner sets GH_TOKEN per-role).
	cmd.Env = filterEnv(cmd.Env, "GH_TOKEN")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("failed to execute golemic: %w", err)
		}
	}

	// Redact tokens from output (BR-003).
	stdoutStr := redactTokens(stdout.String(), r.creds.DevToken(), r.creds.ReviewerToken())
	stderrStr := redactTokens(stderr.String(), r.creds.DevToken(), r.creds.ReviewerToken())

	return &RunResult{
		Stdout:   stdoutStr,
		Stderr:   stderrStr,
		ExitCode: exitCode,
	}, nil
}

// filterEnv removes entries from env that have the given key prefix.
func filterEnv(env []string, key string) []string {
	var filtered []string
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// redactTokens replaces occurrences of token values in s with "***REDACTED***".
func redactTokens(s, devToken, reviewerToken string) string {
	if devToken != "" {
		s = strings.ReplaceAll(s, devToken, "***REDACTED***")
	}
	if reviewerToken != "" && reviewerToken != devToken {
		s = strings.ReplaceAll(s, reviewerToken, "***REDACTED***")
	}
	return s
}

// WriteFixtureConfig writes a valid config.json to the given golemic_e2e path.
// Used to bootstrap a test environment.
func WriteFixtureConfig(golemicE2EPath, project string) error {
	configPath := filepath.Join(golemicE2EPath, ".golemic")
	if err := os.MkdirAll(configPath, 0755); err != nil {
		return fmt.Errorf("failed to create .golemic directory: %w", err)
	}
	cfg := ValidConfigJSON()
	if err := os.WriteFile(filepath.Join(configPath, "config.json"), []byte(cfg), 0644); err != nil {
		return fmt.Errorf("failed to write config.json: %w", err)
	}
	return nil
}