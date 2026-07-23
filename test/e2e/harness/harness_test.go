//go:build e2e

package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// golemicBinary returns the path to the golemic binary for testing.
// It searches in order: GOLEMIC_BINARY env var, then the repo root.
func golemicBinary(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bin := FindBinary(dir, os.Getenv("GOLEMIC_BINARY"))
	if bin == "" {
		t.Fatal("cannot find golemic binary: set GOLEMIC_BINARY env var (or ensure a regular file named 'golemic' exists at the repo root)")
	}
	return bin
}

// TestHarnessInitialization verifies AC-001:
// Given: golemic_e2e directory exists, .golemic/config.json is valid,
// GOLEMIC_DEV_TOKEN and GOLEMIC_REVIEWER_TOKEN set
// When: NewRunner(golemic_e2e_path, golemic_binary) called
// Then: Runner instance created, config loaded, tokens available, ready to spawn
func TestHarnessInitialization(t *testing.T) {
	// Create a temporary golemic_e2e-like directory.
	tmpDir := t.TempDir()

	// Create .golemic/config.json
	configDir := filepath.Join(tmpDir, ".golemic")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	validConfig := `{"project":"golemic_e2e","verify_command":"echo ok","label":"ready-for-agent","models":{"dev":"test-model","reviewer":"test-model"},"timeout_minutes":1}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(validConfig), 0644); err != nil {
		t.Fatal(err)
	}

	// Set required env vars.
	t.Setenv("GOLEMIC_DEV_TOKEN", "test-dev-token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "test-reviewer-token")
	t.Setenv("HOME", tmpDir) // credentials loader uses HOME

	binary := golemicBinary(t)

	r, err := NewRunner(tmpDir, binary)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if r.Config() == nil {
		t.Error("config should not be nil")
	}
	if r.Credentials() == nil {
		t.Error("credentials should not be nil")
	}
	if r.Credentials().DevToken() != "test-dev-token" {
		t.Error("dev token should be loaded from env")
	}
	if r.Credentials().ReviewerToken() != "test-reviewer-token" {
		t.Error("reviewer token should be loaded from env")
	}
	if r.Config().Project != "golemic_e2e" {
		t.Errorf("project: got %q, want %q", r.Config().Project, "golemic_e2e")
	}
}

// TestHarnessPreflightValidation verifies BR-004: fail fast if config invalid.
func TestHarnessPreflightValidation(t *testing.T) {
	tests := []struct {
		name       string
		configJSON string
		wantErrSub string
	}{
		{
			name:       "missing config file",
			configJSON: "",
			wantErrSub: "config file not found",
		},
		{
			name:       "invalid JSON",
			configJSON: "not-json",
			wantErrSub: "invalid JSON",
		},
		{
			name:       "missing project",
			configJSON: `{"verify_command":"echo ok"}`,
			wantErrSub: "required field 'project'",
		},
		{
			name:       "missing verify_command",
			configJSON: `{"project":"test"}`,
			wantErrSub: "required field 'verify_command'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			if tc.configJSON != "" {
				configDir := filepath.Join(tmpDir, ".golemic")
				if err := os.MkdirAll(configDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(tc.configJSON), 0644); err != nil {
					t.Fatal(err)
				}
			}

			// Set HOME but not env tokens – we test config validation, not creds.
			t.Setenv("HOME", tmpDir)

			binary := golemicBinary(t)

			_, err := NewRunner(tmpDir, binary)
			if err == nil {
				t.Fatal("expected error for invalid config, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

// TestHarnessAuthErrorValidation verifies BR-004: fail fast if tokens missing (P2-3).
// Separate test because env var inheritance affects credentials loading.
func TestHarnessAuthErrorValidation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create valid config.
	configDir := filepath.Join(tmpDir, ".golemic")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	validConfig := `{"project":"golemic_e2e","verify_command":"echo ok","label":"ready-for-agent","models":{"dev":"test-model","reviewer":"test-model"},"timeout_minutes":1}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(validConfig), 0644); err != nil {
		t.Fatal(err)
	}

	// Set HOME to temp dir with no credentials.json file.
	// Don't set GOLEMIC_DEV_TOKEN or GOLEMIC_REVIEWER_TOKEN.
	t.Setenv("HOME", tmpDir)
	t.Setenv("GOLEMIC_DEV_TOKEN", "") // Force unset by setting empty.
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "")

	binary := golemicBinary(t)

	_, err := NewRunner(tmpDir, binary)
	if err == nil {
		t.Fatal("expected error for missing credentials, got nil")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error %q should indicate missing credentials", err.Error())
	}
}

// TestSubprocessExecution verifies AC-003:
// Given: Harness ready, issue created (simulated)
// When: runner.Exec() (no args)
// Then: golemic binary spawned, stdout and stderr captured, exit code available,
// output does not contain tokens (redacted)
func TestSubprocessExecution(t *testing.T) {
	// Create a minimal golemic_e2e-like environment.
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, ".golemic")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	validConfig := `{"project":"golemic_e2e","verify_command":"echo ok","label":"ready-for-agent","models":{"dev":"test-model","reviewer":"test-model"},"timeout_minutes":1}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(validConfig), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOLEMIC_DEV_TOKEN", "test-dev-token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "test-reviewer-token")
	t.Setenv("HOME", tmpDir)

	binary := golemicBinary(t)

	r, err := NewRunner(tmpDir, binary)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run golemic without args — should print usage to stderr and exit 1.
	result, err := r.Exec(ctx)
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	if result.ExitCode != 1 {
		t.Errorf("exit code: got %d, want 1", result.ExitCode)
	}

	// Verify stdout is empty (usage goes to stderr for no-arg case).
	if result.Stdout != "" {
		t.Errorf("stdout should be empty, got: %q", result.Stdout)
	}

	if !strings.Contains(result.Stderr, "Usage: golemic") {
		t.Errorf("stderr should contain usage, got: %q", result.Stderr)
	}

	// BR-003: Tokens must never appear in output.
	if strings.Contains(result.Stdout, "test-dev-token") || strings.Contains(result.Stderr, "test-dev-token") {
		t.Error("dev token leaked in output")
	}
	if strings.Contains(result.Stdout, "test-reviewer-token") || strings.Contains(result.Stderr, "test-reviewer-token") {
		t.Error("reviewer token leaked in output")
	}
}

// TestSubprocessExecutionWithArgs verifies spawning with specific arguments.
func TestSubprocessExecutionWithArgs(t *testing.T) {
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, ".golemic")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	validConfig := `{"project":"golemic_e2e","verify_command":"echo ok","label":"ready-for-agent","models":{"dev":"test-model","reviewer":"test-model"},"timeout_minutes":1}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(validConfig), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOLEMIC_DEV_TOKEN", "test-dev-token")
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", "test-reviewer-token")
	t.Setenv("HOME", tmpDir)

	binary := golemicBinary(t)

	r, err := NewRunner(tmpDir, binary)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run "golemic does-not-exist" — should print unknown command and exit 1.
	result, err := r.Exec(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	if result.ExitCode != 1 {
		t.Errorf("exit code: got %d, want 1", result.ExitCode)
	}

	if !strings.Contains(result.Stderr, "Unknown command: does-not-exist") {
		t.Errorf("stderr should mention unknown command, got: %q", result.Stderr)
	}

	// BR-003: Tokens must never appear in output.
	if strings.Contains(result.Stdout, "test-dev-token") || strings.Contains(result.Stderr, "test-dev-token") {
		t.Error("dev token leaked in output")
	}
}

// TestTokenRedaction verifies BR-003: tokens never appear in logs/output.
func TestTokenRedaction(t *testing.T) {
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, ".golemic")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	validConfig := `{"project":"golemic_e2e","verify_command":"echo ok","label":"ready-for-agent","models":{"dev":"test-model","reviewer":"test-model"},"timeout_minutes":1}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(validConfig), 0644); err != nil {
		t.Fatal(err)
	}

	// Use realistic-looking tokens.
	devToken := "ghp_testDevToken123456"
	reviewerToken := "ghp_testReviewerToken789012"
	t.Setenv("GOLEMIC_DEV_TOKEN", devToken)
	t.Setenv("GOLEMIC_REVIEWER_TOKEN", reviewerToken)
	t.Setenv("HOME", tmpDir)

	binary := golemicBinary(t)

	r, err := NewRunner(tmpDir, binary)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run a command that will fail — captures stderr.
	result, err := r.Exec(ctx, "run", "--issue", "99999999")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	// BR-003: The actual token values must not appear anywhere.
	combined := result.Stdout + result.Stderr
	if strings.Contains(combined, devToken) {
		t.Error("dev token leaked in combined output")
	}
	if strings.Contains(combined, reviewerToken) {
		t.Error("reviewer token leaked in combined output")
	}
}
