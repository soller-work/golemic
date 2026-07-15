package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setEnv sets an env var and returns a cleanup function.
func setEnv(t *testing.T, key, value string) func() {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Setenv(key, value)
	return func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	}
}

// writeCredsFile creates the credentials directory and file with the given content and permissions.
func writeCredsFile(t *testing.T, homeDir, project, content string, perm os.FileMode) string {
	t.Helper()
	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credFile := filepath.Join(credDir, "credentials.json")
	if err := os.WriteFile(credFile, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	return credFile
}

func TestProjectNameValidation(t *testing.T) {
	tests := []struct {
		name    string
		project string
		wantOK  bool
	}{
		{"valid simple", "my-project", true},
		{"valid with dots", "my.project_v1", true},
		{"valid alphanumeric", "Project42", true},
		{"with underscore", "test_proj", true},
		{"path traversal ../", "../etc", false},
		{"absolute path", "/etc/passwd", false},
		{"empty string", "", false},
		{"with slash", "foo/bar", false},
		{"with null byte", "test\x00proj", false},
		{"relative dots", "..", false},
		{"single dot", ".", false},
		{"leading dot", ".hidden", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLoader(t.TempDir())
			_, err := l.Load(tt.project)
			if tt.wantOK && err != nil && strings.Contains(err.Error(), "invalid project name") {
				t.Errorf("valid project %q rejected: %v", tt.project, err)
			}
			if !tt.wantOK {
				if err == nil {
					t.Errorf("expected error for project %q, got nil", tt.project)
				} else if !strings.Contains(err.Error(), "invalid project name") {
					t.Errorf("expected 'invalid project name' in error, got: %v", err)
				}
			}
		})
	}
}

func TestLoadCredentials(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, homeDir string) (project string, cleanup func())
		wantErr     bool
		errContains []string
		check       func(t *testing.T, err error) // optional additional error assertions
		wantDev     string
		wantRev     string
	}{
		{
			name: "valid file with both tokens",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_dev123",
					"reviewer_token": "ghp_rev456"
				}`, 0600)
				return "test-proj", func() {}
			},
			wantDev: "ghp_dev123",
			wantRev: "ghp_rev456",
		},
		{
			name: "env var overrides file for dev_token",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_file_dev",
					"reviewer_token": "ghp_file_rev"
				}`, 0600)
				cleanup := setEnv(t, "GOLEMIC_DEV_TOKEN", "ghp_env_dev")
				return "test-proj", cleanup
			},
			wantDev: "ghp_env_dev",
			wantRev: "ghp_file_rev",
		},
		{
			name: "env var overrides file for reviewer_token",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_file_dev",
					"reviewer_token": "ghp_file_rev"
				}`, 0600)
				cleanup := setEnv(t, "GOLEMIC_REVIEWER_TOKEN", "ghp_env_rev")
				return "test-proj", cleanup
			},
			wantDev: "ghp_file_dev",
			wantRev: "ghp_env_rev",
		},
		{
			name: "both env vars override file",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_file_dev",
					"reviewer_token": "ghp_file_rev"
				}`, 0600)
				c1 := setEnv(t, "GOLEMIC_DEV_TOKEN", "ghp_env_dev")
				c2 := setEnv(t, "GOLEMIC_REVIEWER_TOKEN", "ghp_env_rev")
				return "test-proj", func() { c1(); c2() }
			},
			wantDev: "ghp_env_dev",
			wantRev: "ghp_env_rev",
		},
		{
			name: "only env vars no file",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				c1 := setEnv(t, "GOLEMIC_DEV_TOKEN", "ghp_env_dev")
				c2 := setEnv(t, "GOLEMIC_REVIEWER_TOKEN", "ghp_env_rev")
				return "test-proj", func() { c1(); c2() }
			},
			wantDev: "ghp_env_dev",
			wantRev: "ghp_env_rev",
		},
		{
			name: "file with group-readable permissions",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_dev123",
					"reviewer_token": "ghp_rev456"
				}`, 0640)
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"chmod 600", "credentials.json"},
		},
		{
			name: "file with world-readable permissions",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_dev123",
					"reviewer_token": "ghp_rev456"
				}`, 0644)
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"chmod 600"},
		},
		{
			name: "missing dev_token from both sources",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"reviewer_token": "ghp_rev_only"
				}`, 0600)
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"GOLEMIC_DEV_TOKEN", "dev_token"},
		},
		{
			name: "missing reviewer_token from both sources",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_dev_only"
				}`, 0600)
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"GOLEMIC_REVIEWER_TOKEN", "reviewer_token"},
		},
		{
			name: "missing both tokens from both sources",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("errors.Is(err, os.ErrNotExist) should be true when file is the only source and missing")
				}
			},
		},
		{
			name: "file missing with only dev env does not wrap ErrNotExist",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				c1 := setEnv(t, "GOLEMIC_DEV_TOKEN", "ghp_dev_only")
				return "test-proj", c1
			},
			wantErr:     true,
			errContains: []string{"GOLEMIC_REVIEWER_TOKEN", "reviewer_token"},
			check: func(t *testing.T, err error) {
				if errors.Is(err, os.ErrNotExist) {
					t.Errorf("errors.Is(err, os.ErrNotExist) should be false when a dev env var is set")
				}
			},
		},
		{
			name: "file missing with only reviewer env does not wrap ErrNotExist",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				c1 := setEnv(t, "GOLEMIC_REVIEWER_TOKEN", "ghp_rev_only")
				return "test-proj", c1
			},
			wantErr:     true,
			errContains: []string{"GOLEMIC_DEV_TOKEN", "dev_token"},
			check: func(t *testing.T, err error) {
				if errors.Is(err, os.ErrNotExist) {
					t.Errorf("errors.Is(err, os.ErrNotExist) should be false when a reviewer env var is set")
				}
			},
		},
		{
			name: "empty dev_token in file with no env var",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "",
					"reviewer_token": "ghp_rev_valid"
				}`, 0600)
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"GOLEMIC_DEV_TOKEN"},
		},
		{
			name: "empty reviewer_token in file with empty env var",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_dev_valid",
					"reviewer_token": ""
				}`, 0600)
				cleanup := setEnv(t, "GOLEMIC_REVIEWER_TOKEN", "")
				return "test-proj", cleanup
			},
			wantErr:     true,
			errContains: []string{"GOLEMIC_REVIEWER_TOKEN"},
		},
		{
			name: "unknown JSON field in credentials file",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_dev",
					"reviewer_token": "ghp_rev",
					"unknown_field": "should not be here"
				}`, 0600)
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"unknown_field"},
		},
		{
			name: "malformed JSON in credentials file",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_dev",
					"reviewer_token": "ghp_rev",
				`, 0600) // trailing comma, missing closing
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"invalid JSON"},
		},
		{
			name: "malformed JSON with token value does not leak",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				// Token value placed right before a syntax error to verify
				// the error message does not echo the token.
				writeCredsFile(t, homeDir, "test-proj", `{
					"dev_token": "ghp_my_secret_key",
					"reviewer_token": "ghp_another_secret",
					"broken": `, 0600) // unterminated value
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"invalid JSON"},
		},
		{
			name: "symlink to file with wrong permissions checks target",
			setup: func(t *testing.T, homeDir string) (string, func()) {
				// Create a target file with loose permissions outside the home dir
				targetDir := t.TempDir()
				targetFile := filepath.Join(targetDir, "target_creds.json")
				if err := os.WriteFile(targetFile, []byte(`{
					"dev_token": "ghp_sym_dev",
					"reviewer_token": "ghp_sym_rev"
				}`), 0644); err != nil {
					t.Fatal(err)
				}

				// Create symlink in the credentials location pointing to the target
				credDir := filepath.Join(homeDir, ".golemic", "test-proj")
				if err := os.MkdirAll(credDir, 0755); err != nil {
					t.Fatal(err)
				}
				linkPath := filepath.Join(credDir, "credentials.json")
				if err := os.Symlink(targetFile, linkPath); err != nil {
					t.Fatal(err)
				}
				return "test-proj", func() {}
			},
			wantErr:     true,
			errContains: []string{"chmod 600", "credentials.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := t.TempDir()
			project, cleanup := tt.setup(t, homeDir)
			if cleanup != nil {
				defer cleanup()
			}

			l := NewLoader(homeDir)
			creds, err := l.Load(project)

			if tt.wantErr {
				if err == nil {
					t.Fatal("Load() expected error, got nil")
				}
				if tt.errContains != nil {
					for _, substr := range tt.errContains {
						if !strings.Contains(err.Error(), substr) {
							t.Errorf("error should contain %q, got: %v", substr, err)
						}
					}
				}
				if tt.check != nil {
					tt.check(t, err)
				}
				// Verify no token values leak into error messages
				if strings.Contains(err.Error(), "ghp_") {
					t.Errorf("error must not contain token-like values, got: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if creds.DevToken() != tt.wantDev {
				t.Errorf("DevToken() = %q, want %q", creds.DevToken(), tt.wantDev)
			}
			if creds.ReviewerToken() != tt.wantRev {
				t.Errorf("ReviewerToken() = %q, want %q", creds.ReviewerToken(), tt.wantRev)
			}
		})
	}
}

func TestEmptyHomeDir(t *testing.T) {
	t.Run("NewLoader with empty homeDir returns error", func(t *testing.T) {
		l := NewLoader("")
		_, err := l.Load("test-proj")
		if err == nil {
			t.Fatal("expected error for empty home directory, got nil")
		}
		if !strings.Contains(err.Error(), "empty home directory") {
			t.Errorf("error should mention empty home directory, got: %v", err)
		}
	})

	t.Run("NewLoader with non-empty homeDir succeeds on empty home dir check", func(t *testing.T) {
		l := NewLoader(t.TempDir())
		_, err := l.Load("test-proj")
		if err == nil {
			return // fine, other error (missing tokens) expected
		}
		if strings.Contains(err.Error(), "empty home directory") {
			t.Errorf("non-empty homeDir should not trigger empty home directory error, got: %v", err)
		}
	})
}

func TestCredentialsString(t *testing.T) {
	t.Run("redacted when both tokens set", func(t *testing.T) {
		c := &Credentials{devToken: "secret-dev", reviewerToken: "secret-rev"}
		s := c.String()
		if strings.Contains(s, "secret") {
			t.Errorf("String() must not contain token values, got: %s", s)
		}
		if !strings.Contains(s, "***set***") {
			t.Errorf("String() should contain '***set***' for set tokens, got: %s", s)
		}
		if strings.Contains(s, "***unset***") {
			t.Errorf("String() should not contain '***unset***' when both are set, got: %s", s)
		}
	})

	t.Run("redacted when both tokens unset", func(t *testing.T) {
		c := &Credentials{}
		s := c.String()
		if strings.Contains(s, "***set***") {
			t.Errorf("String() should not contain '***set***' when unset, got: %s", s)
		}
		if !strings.Contains(s, "***unset***") {
			t.Errorf("String() should contain '***unset***' for unset tokens, got: %s", s)
		}
	})

	t.Run("redacted when one token set", func(t *testing.T) {
		c := &Credentials{devToken: "secret-dev"}
		s := c.String()
		if strings.Contains(s, "secret") {
			t.Errorf("String() must not contain token values, got: %s", s)
		}
		if !strings.Contains(s, "***set***") || !strings.Contains(s, "***unset***") {
			t.Errorf("String() should show mixed state, got: %s", s)
		}
	})
}

func TestErrorsIs(t *testing.T) {
	t.Run("missing credentials error wraps os.ErrNotExist", func(t *testing.T) {
		l := NewLoader(t.TempDir())
		_, err := l.Load("no-such-project")
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Load error should be errors.Is(err, os.ErrNotExist): got %T: %v", err, err)
		}
	})
}

// TestSymlinkToSecureFile verifies that a symlink to a properly permissioned
// target file succeeds.
func TestSymlinkToSecureFile(t *testing.T) {
	homeDir := t.TempDir()
	targetDir := t.TempDir()
	targetFile := filepath.Join(targetDir, "target_creds.json")
	if err := os.WriteFile(targetFile, []byte(`{
		"dev_token": "ghp_sym_dev",
		"reviewer_token": "ghp_sym_rev"
	}`), 0600); err != nil {
		t.Fatal(err)
	}

	credDir := filepath.Join(homeDir, ".golemic", "test-proj")
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(credDir, "credentials.json")
	if err := os.Symlink(targetFile, linkPath); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(homeDir)
	creds, err := l.Load("test-proj")
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if creds.DevToken() != "ghp_sym_dev" {
		t.Errorf("DevToken() = %q, want %q", creds.DevToken(), "ghp_sym_dev")
	}
	if creds.ReviewerToken() != "ghp_sym_rev" {
		t.Errorf("ReviewerToken() = %q, want %q", creds.ReviewerToken(), "ghp_sym_rev")
	}
}

func TestValidateProjectName(t *testing.T) {
	tests := []struct {
		name    string
		project string
		wantOK  bool
	}{
		{"valid simple", "my-project", true},
		{"valid with dots", "my.project_v1", true},
		{"valid alphanumeric", "Project42", true},
		{"with underscore", "test_proj", true},
		{"path traversal ../", "../etc", false},
		{"absolute path", "/etc/passwd", false},
		{"empty string", "", false},
		{"with slash", "foo/bar", false},
		{"leading dot", ".hidden", false},
		{"double dot", "..", false},
		{"single dot", ".", false},
		{"special chars", `my"repo`, false},
		{"backslash", `my\repo`, false},
		{"newline", "my\nrepo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectName(tt.project)
			if tt.wantOK && err != nil {
				t.Errorf("ValidateProjectName(%q) = %v, want nil", tt.project, err)
			}
			if !tt.wantOK && err == nil {
				t.Errorf("ValidateProjectName(%q) = nil, want error", tt.project)
			}
		})
	}
}
