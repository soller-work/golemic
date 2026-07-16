package template

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// setEnv sets an environment variable for the duration of the test.
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

func TestEnvResolverPlainLiteral(t *testing.T) {
	r := NewEnvResolver()
	val, source, err := r.Resolve("ghp_plain_token")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "ghp_plain_token" {
		t.Errorf("Resolve() value = %q, want %q", val, "ghp_plain_token")
	}
	if source != SourceFileLiteral {
		t.Errorf("Resolve() source = %q, want %q", source, SourceFileLiteral)
	}
}

func TestEnvResolverTemplateFromEnv(t *testing.T) {
	cleanup := setEnv(t, "MY_TOKEN", "ghp_from_env")
	defer cleanup()

	r := NewEnvResolver()
	val, source, err := r.Resolve("${MY_TOKEN}")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "ghp_from_env" {
		t.Errorf("Resolve() value = %q, want %q", val, "ghp_from_env")
	}
	if source != SourceTemplateEnv {
		t.Errorf("Resolve() source = %q, want %q", source, SourceTemplateEnv)
	}
}

func TestEnvResolverTemplateWithDefaultUnset(t *testing.T) {
	// Ensure MISSING_VAR is not set
	os.Unsetenv("MISSING_VAR_FOR_DEFAULT")

	r := NewEnvResolver()
	val, source, err := r.Resolve("${MISSING_VAR_FOR_DEFAULT:fallback}")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "fallback" {
		t.Errorf("Resolve() value = %q, want %q", val, "fallback")
	}
	if source != SourceTemplateDefault {
		t.Errorf("Resolve() source = %q, want %q", source, SourceTemplateDefault)
	}
}

func TestEnvResolverTemplateWithDefaultEmptyVar(t *testing.T) {
	cleanup := setEnv(t, "EMPTY_VAR_FOR_DEFAULT", "")
	defer cleanup()

	r := NewEnvResolver()
	val, source, err := r.Resolve("${EMPTY_VAR_FOR_DEFAULT:fallback}")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "fallback" {
		t.Errorf("Resolve() value = %q, want %q", val, "fallback")
	}
	if source != SourceTemplateDefault {
		t.Errorf("Resolve() source = %q, want %q", source, SourceTemplateDefault)
	}
}

func TestEnvResolverTemplateEmptyDefault(t *testing.T) {
	os.Unsetenv("UNSET_VAR")

	r := NewEnvResolver()
	val, source, err := r.Resolve("${UNSET_VAR:}")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "" {
		t.Errorf("Resolve() value = %q, want %q", val, "")
	}
	if source != SourceTemplateDefault {
		t.Errorf("Resolve() source = %q, want %q", source, SourceTemplateDefault)
	}
}

func TestEnvResolverMissingEnvVar(t *testing.T) {
	os.Unsetenv("DEFINITELY_MISSING_VAR")

	r := NewEnvResolver()
	_, _, err := r.Resolve("${DEFINITELY_MISSING_VAR}")
	if err == nil {
		t.Fatal("Resolve() expected error, got nil")
	}

	var missingErr *MissingEnvVarError
	if !errors.As(err, &missingErr) {
		t.Errorf("error should be *MissingEnvVarError, got %T: %v", err, err)
	}
	if missingErr.VarName != "DEFINITELY_MISSING_VAR" {
		t.Errorf("VarName = %q, want %q", missingErr.VarName, "DEFINITELY_MISSING_VAR")
	}
	if !strings.Contains(err.Error(), "DEFINITELY_MISSING_VAR") {
		t.Errorf("error message should contain env var name, got: %v", err)
	}
}

func TestEnvResolverUnclosedTemplate(t *testing.T) {
	r := NewEnvResolver()
	_, _, err := r.Resolve("prefix ${UNCLOSED")
	if err == nil {
		t.Fatal("Resolve() expected error, got nil")
	}

	var malformedErr *MalformedTemplateError
	if !errors.As(err, &malformedErr) {
		t.Errorf("error should be *MalformedTemplateError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "unclosed") {
		t.Errorf("error message should mention 'unclosed', got: %v", err)
	}
}

func TestEnvResolverEmptyTemplate(t *testing.T) {
	r := NewEnvResolver()
	_, _, err := r.Resolve("${}")
	if err == nil {
		t.Fatal("Resolve() expected error, got nil")
	}

	var malformedErr *MalformedTemplateError
	if !errors.As(err, &malformedErr) {
		t.Errorf("error should be *MalformedTemplateError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error message should mention 'empty', got: %v", err)
	}
}

func TestEnvResolverInvalidVarName(t *testing.T) {
	r := NewEnvResolver()
	_, _, err := r.Resolve("${VAR-WITH-DASH}")
	if err == nil {
		t.Fatal("Resolve() expected error, got nil")
	}

	var malformedErr *MalformedTemplateError
	if !errors.As(err, &malformedErr) {
		t.Errorf("error should be *MalformedTemplateError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "invalid variable name") {
		t.Errorf("error message should mention 'invalid variable name', got: %v", err)
	}
}

func TestEnvResolverNoTokenLeakInErrors(t *testing.T) {
	r := NewEnvResolver()

	// Test: malformed template error must not leak input content or token-like values
	_, _, err := r.Resolve("prefix ${UNCLOSED")
	if err == nil {
		t.Fatal("expected error for unclosed template")
	}
	// Must not contain token-like values
	if strings.Contains(err.Error(), "ghp_") {
		t.Errorf("error must not contain token-like values, got: %v", err)
	}
	// Must not contain the literal input prefix from the value
	if strings.Contains(err.Error(), "prefix ") {
		t.Errorf("error must not contain input prefix, got: %v", err)
	}
	// Must not contain the uppercase input text
	if strings.Contains(err.Error(), "UNCLOSED") {
		t.Errorf("error must not contain template content, got: %v", err)
	}

	// Test: missing env var error must not contain env var VALUES
	cleanup := setEnv(t, "SAFE_VAR", "ghp_super_secret_value")
	defer cleanup()
	os.Unsetenv("ANOTHER_MISSING")
	_, _, err = r.Resolve("${ANOTHER_MISSING}")
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
	// Env var name is OK in the message, but NOT the value of ANY env var
	if strings.Contains(err.Error(), "ghp_super_secret_value") {
		t.Errorf("error must not contain env var values, got: %v", err)
	}
	// Must contain the missing var name
	if !strings.Contains(err.Error(), "ANOTHER_MISSING") {
		t.Errorf("error should contain var name, got: %v", err)
	}
}

func TestEnvResolverMultipleTemplates(t *testing.T) {
	cleanup1 := setEnv(t, "VAR_A", "alpha")
	cleanup2 := setEnv(t, "VAR_B", "beta")
	defer cleanup1()
	defer cleanup2()

	r := NewEnvResolver()
	val, source, err := r.Resolve("prefix_${VAR_A}_middle_${VAR_B}_suffix")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "prefix_alpha_middle_beta_suffix" {
		t.Errorf("Resolve() value = %q, want %q", val, "prefix_alpha_middle_beta_suffix")
	}
	if source != SourceTemplateEnv {
		t.Errorf("Resolve() source = %q, want %q", source, SourceTemplateEnv)
	}
}

func TestEnvResolverMixedEnvAndDefault(t *testing.T) {
	cleanup := setEnv(t, "SET_VAR", "from_env")
	defer cleanup()
	os.Unsetenv("UNSET_VAR_MIXED")

	r := NewEnvResolver()
	val, source, err := r.Resolve("${SET_VAR}_${UNSET_VAR_MIXED:fallback}")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "from_env_fallback" {
		t.Errorf("Resolve() value = %q, want %q", val, "from_env_fallback")
	}
	// source should be template_env because at least one template resolved from env
	if source != SourceTemplateEnv {
		t.Errorf("Resolve() source = %q, want %q", source, SourceTemplateEnv)
	}
}

func TestEnvResolverAllDefaults(t *testing.T) {
	os.Unsetenv("X")
	os.Unsetenv("Y")

	r := NewEnvResolver()
	val, source, err := r.Resolve("${X:a}_${Y:b}")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "a_b" {
		t.Errorf("Resolve() value = %q, want %q", val, "a_b")
	}
	if source != SourceTemplateDefault {
		t.Errorf("Resolve() source = %q, want %q", source, SourceTemplateDefault)
	}
}

func TestEnvResolverEmptyString(t *testing.T) {
	r := NewEnvResolver()
	val, source, err := r.Resolve("")
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if val != "" {
		t.Errorf("Resolve() value = %q, want %q", val, "")
	}
	if source != SourceFileLiteral {
		t.Errorf("Resolve() source = %q, want %q", source, SourceFileLiteral)
	}
}

func TestMalformedTemplateError(t *testing.T) {
	e := &MalformedTemplateError{Description: "unclosed template reference"}
	msg := e.Error()
	if !strings.Contains(msg, "malformed template reference") {
		t.Errorf("MalformedTemplateError.Error() should contain 'malformed template reference', got: %s", msg)
	}
	if !strings.Contains(msg, "unclosed template reference") {
		t.Errorf("MalformedTemplateError.Error() should contain description, got: %s", msg)
	}
}

func TestMissingEnvVarError(t *testing.T) {
	e := &MissingEnvVarError{VarName: "MY_VAR"}
	msg := e.Error()
	if !strings.Contains(msg, "MY_VAR") {
		t.Errorf("MissingEnvVarError.Error() should contain var name, got: %s", msg)
	}
	if !strings.Contains(msg, "is not set") {
		t.Errorf("MissingEnvVarError.Error() should mention 'is not set', got: %s", msg)
	}
}

// TestResolverInterface verifies that EnvResolver satisfies the Resolver interface.
func TestResolverInterface(t *testing.T) {
	var r Resolver = NewEnvResolver()
	if r == nil {
		t.Fatal("NewEnvResolver() returned nil")
	}
}