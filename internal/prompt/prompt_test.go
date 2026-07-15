package prompt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testIssue is a reusable test issue used across multiple tests.
var testIssue = Issue{
	Number: 42,
	Title:  "Fix bug",
	Body:   "Details here",
}

func writeTestGuidelines(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create guidelines dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write guidelines file: %v", err)
	}
	return path
}

// AC-001: Dev prompt golden file has all facts
func TestRenderDev_ContainsAllFacts(t *testing.T) {
	guidelinesContent := "# Dev Guidelines (Test)\n\n## Stack\nGo 1.21, standard library"
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", guidelinesContent)

	systemPromptPath, userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	// System prompt path
	if systemPromptPath != "prompts/dev.md" {
		t.Errorf("systemPromptPath = %q, want %q", systemPromptPath, "prompts/dev.md")
	}

	// Issue number
	if !strings.Contains(userPrompt, "42") {
		t.Error("userPrompt missing issue number 42")
	}

	// Issue title
	if !strings.Contains(userPrompt, "Fix bug") {
		t.Error("userPrompt missing issue title 'Fix bug'")
	}

	// Issue body
	if !strings.Contains(userPrompt, "Details here") {
		t.Error("userPrompt missing issue body 'Details here'")
	}

	// Branch name
	if !strings.Contains(userPrompt, "golemic/issue-42") {
		t.Error("userPrompt missing branch name 'golemic/issue-42'")
	}

	// Verify command
	if !strings.Contains(userPrompt, "go test ./...") {
		t.Error("userPrompt missing verify command 'go test ./...'")
	}

	// Guidelines content injected
	if !strings.Contains(userPrompt, "# Dev Guidelines (Test)") {
		t.Error("userPrompt missing guidelines content")
	}
	if !strings.Contains(userPrompt, "Go 1.21, standard library") {
		t.Error("userPrompt missing guidelines body content")
	}

	// Final step: golemic open-pr
	if !strings.Contains(userPrompt, "golemic open-pr") {
		t.Error("userPrompt missing 'golemic open-pr' as final step")
	}

	// Only after verify_command exits 0
	if !strings.Contains(userPrompt, "Only after") && !strings.Contains(userPrompt, "only after") {
		t.Error("userPrompt missing condition that open-pr is only allowed after verify_command exits 0")
	}

	// AC-004: System prompt path returned (sub-check)
	if systemPromptPath == "" {
		t.Error("systemPromptPath is empty")
	}

	// AC-005: Prompt in memory as string (sub-check)
	if userPrompt == "" {
		t.Error("userPrompt is empty string")
	}
}

// AC-002: Reviewer prompt golden file has all facts
func TestRenderReviewer_ContainsAllFacts(t *testing.T) {
	guidelinesContent := "# Reviewer Guidelines (Test)\n\n## Stack\nGo 1.21, standard library"
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", guidelinesContent)

	prNumber := 123
	systemPromptPath, userPrompt, err := RenderReviewer(prNumber, testIssue, "go test ./...", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	// System prompt path
	if systemPromptPath != "prompts/reviewer.md" {
		t.Errorf("systemPromptPath = %q, want %q", systemPromptPath, "prompts/reviewer.md")
	}

	// PR number
	if !strings.Contains(userPrompt, "123") {
		t.Error("userPrompt missing PR number 123")
	}

	// Issue number
	if !strings.Contains(userPrompt, "42") {
		t.Error("userPrompt missing issue number 42")
	}

	// Issue title
	if !strings.Contains(userPrompt, "Fix bug") {
		t.Error("userPrompt missing issue title 'Fix bug'")
	}

	// Issue body
	if !strings.Contains(userPrompt, "Details here") {
		t.Error("userPrompt missing issue body 'Details here'")
	}

	// Verify command
	if !strings.Contains(userPrompt, "go test ./...") {
		t.Error("userPrompt missing verify command 'go test ./...'")
	}

	// Guidelines content injected
	if !strings.Contains(userPrompt, "# Reviewer Guidelines (Test)") {
		t.Error("userPrompt missing guidelines content")
	}
	if !strings.Contains(userPrompt, "Go 1.21, standard library") {
		t.Error("userPrompt missing guidelines body content")
	}

	// Final step: golemic submit-review
	if !strings.Contains(userPrompt, "golemic submit-review") {
		t.Error("userPrompt missing 'golemic submit-review' as final step")
	}

	// AC-004: System prompt path returned (sub-check)
	if systemPromptPath == "" {
		t.Error("systemPromptPath is empty")
	}

	// AC-005: Prompt in memory as string (sub-check)
	if userPrompt == "" {
		t.Error("userPrompt is empty string")
	}
}

// AC-003: Missing guidelines file returns a named error
func TestRenderDev_MissingGuidelinesError(t *testing.T) {
	nonexistentPath := filepath.Join(t.TempDir(), "nonexistent", "dev.md")

	_, userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", nonexistentPath)

	if err == nil {
		t.Fatal("RenderDev() expected error for missing guidelines file, got nil")
	}

	// Error message mentions the missing file path
	if !strings.Contains(err.Error(), nonexistentPath) {
		t.Errorf("error message = %q, expected to contain path %q", err.Error(), nonexistentPath)
	}

	// No prompt is rendered
	if userPrompt != "" {
		t.Error("userPrompt should be empty when guidelines file is missing")
	}
}

// AC-003 (reviewer variant): Missing guidelines/reviewer.md returns error
func TestRenderReviewer_MissingGuidelinesError(t *testing.T) {
	nonexistentPath := filepath.Join(t.TempDir(), "nonexistent", "reviewer.md")

	_, userPrompt, err := RenderReviewer(123, testIssue, "go test ./...", nonexistentPath)

	if err == nil {
		t.Fatal("RenderReviewer() expected error for missing guidelines file, got nil")
	}

	if !strings.Contains(err.Error(), nonexistentPath) {
		t.Errorf("error message = %q, expected to contain path %q", err.Error(), nonexistentPath)
	}

	if userPrompt != "" {
		t.Error("userPrompt should be empty when guidelines file is missing")
	}
}

// AC-004 standalone: System prompt paths returned correctly for both roles
func TestRenderDev_SystemPromptPath(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Test")

	systemPromptPath, _, err := RenderDev(testIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	want := "prompts/dev.md"
	if systemPromptPath != want {
		t.Errorf("systemPromptPath = %q, want %q", systemPromptPath, want)
	}
}

func TestRenderReviewer_SystemPromptPath(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Test")

	systemPromptPath, _, err := RenderReviewer(123, testIssue, "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	want := "prompts/reviewer.md"
	if systemPromptPath != want {
		t.Errorf("systemPromptPath = %q, want %q", systemPromptPath, want)
	}
}

// AC-005 standalone: Prompt is rendered as a non-empty string in memory
func TestRenderDev_UserPromptNonEmpty(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Test Guidelines")

	_, userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	if userPrompt == "" {
		t.Error("userPrompt should be a non-empty string")
	}
}

func TestRenderReviewer_UserPromptNonEmpty(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Test Guidelines")

	_, userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if userPrompt == "" {
		t.Error("userPrompt should be a non-empty string")
	}
}

// Guidelines content is injected verbatim (not truncated or modified)
func TestRenderDev_GuidelinesVerbatim(t *testing.T) {
	guidelinesContent := "# Custom Guidelines\n\nSome **markdown** content with `code` and\n\nmulti-line text."
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", guidelinesContent)

	_, userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, guidelinesContent) {
		t.Error("guidelines content should be injected verbatim, but was not found in userPrompt")
	}
}

func TestRenderReviewer_GuidelinesVerbatim(t *testing.T) {
	guidelinesContent := "# Custom Reviewer Guidelines\n\nSome **markdown** content with `code`."
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", guidelinesContent)

	_, userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, guidelinesContent) {
		t.Error("guidelines content should be injected verbatim, but was not found in userPrompt")
	}
}

// Step list in dev prompt ends with open-pr
func TestRenderDev_StepListEndsWithOpenPR(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	_, userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "golemic open-pr") {
		t.Error("step list must contain 'golemic open-pr'")
	}

	// open-pr should appear near the end, after other steps
	if strings.LastIndex(userPrompt, "golemic open-pr") < strings.LastIndex(userPrompt, "Instructions") {
		t.Error("'golemic open-pr' should appear near the end of the user prompt")
	}
}

// Step list in reviewer prompt ends with submit-review
func TestRenderReviewer_StepListEndsWithSubmitReview(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")
	_, userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "golemic submit-review") {
		t.Error("step list must contain 'golemic submit-review'")
	}

	if strings.LastIndex(userPrompt, "golemic submit-review") < strings.LastIndex(userPrompt, "Instructions") {
		t.Error("'golemic submit-review' should appear near the end of the user prompt")
	}
}

// Issue with empty title or body still renders (no panic)
func TestRenderDev_EmptyTitleBody(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	emptyIssue := Issue{Number: 99, Title: "", Body: ""}

	_, userPrompt, err := RenderDev(emptyIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error for empty title/body: %v", err)
	}

	if !strings.Contains(userPrompt, "99") {
		t.Error("userPrompt should still contain issue number")
	}
}

// P2-1: Adversarial input — title with apostrophe, body with backticks + apostrophe
// Asserts the pre-encoded base64 approach prevents ALL shell quoting issues.
func TestRenderDev_AdversarialShellSafety(t *testing.T) {
	adversarialIssue := Issue{
		Number: 42,
		Title:  `it's a fix`,
		Body:   "`echo it's broken`",
	}
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	_, userPrompt, err := RenderDev(adversarialIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	// Must use base64 flags, not inline --title/--body or tempfile flags
	if strings.Contains(userPrompt, `--title "`) {
		t.Error("rendered prompt uses unsafe inline --title \"...\" shell quoting; must use --title-b64")
	}
	if strings.Contains(userPrompt, `--body "`) {
		t.Error("rendered prompt uses unsafe inline --body \"...\" shell quoting; must use --body-b64")
	}
	if strings.Contains(userPrompt, "--title-file") {
		t.Error("rendered prompt uses old --title-file flag; must use --title-b64")
	}
	if strings.Contains(userPrompt, "--body-file") {
		t.Error("rendered prompt uses old --body-file flag; must use --body-b64")
	}

	// Must reference the base64 flags
	if !strings.Contains(userPrompt, "--title-b64") {
		t.Error("rendered prompt missing --title-b64 flag")
	}
	if !strings.Contains(userPrompt, "--body-b64") {
		t.Error("rendered prompt missing --body-b64 flag")
	}

	// Pre-encoded base64 values are used (no printf or base64 commands)
	if !strings.Contains(userPrompt, "TITLE_B64=") {
		t.Error("rendered prompt missing TITLE_B64 variable assignment")
	}
	if !strings.Contains(userPrompt, "BODY_B64=") {
		t.Error("rendered prompt missing BODY_B64 variable assignment")
	}
	// The base64-encoded values are alphanumeric (no shell metacharacters)
	if !strings.Contains(userPrompt, "aXQncyBhIGZpeA==") {
		t.Error("rendered prompt missing expected base64-encoded title value")
	}
	if !strings.Contains(userPrompt, "YGVjaG8gaXQncyBicm9rZW5g") {
		t.Error("rendered prompt missing expected base64-encoded body value")
	}

	// Adversarial content is present in the prompt (as instruction text)
	if !strings.Contains(userPrompt, `it's a fix`) {
		t.Error("adversarial title not present in rendered prompt")
	}
	if !strings.Contains(userPrompt, "`echo it's broken`") {
		t.Error("adversarial body not present in rendered prompt")
	}

	// golemic open-pr is still present as the final action
	if !strings.Contains(userPrompt, "golemic open-pr") {
		t.Error("golemic open-pr missing from rendered prompt")
	}

	// Extract the full shell script (TITLE_B64, BODY_B64, golemic invocation) and
	// validate the entire script with bash -n to verify zero syntax errors.
	var shellScriptLines []string
	for _, line := range strings.Split(userPrompt, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "TITLE_B64=") ||
			strings.HasPrefix(trimmed, "BODY_B64=") ||
			strings.HasPrefix(trimmed, "golemic open-pr") {
			shellScriptLines = append(shellScriptLines, trimmed)
		}
	}

	if len(shellScriptLines) == 0 {
		t.Fatal("no shell script lines found in rendered prompt")
	}

	fullScript := strings.Join(shellScriptLines, "\n")
	if err := bashValidate(fullScript); err != nil {
		t.Errorf("full shell script fails bash -n syntax check:\n  %v\n  script:\n%s", err, fullScript)
	}
}

// bashValidate runs bash -n on a shell command string to check syntax.
func bashValidate(cmd string) error {
	// Use "bash -n -c" to check syntax without executing
	return bashCheckSyntax(cmd)
}

func bashCheckSyntax(cmd string) error {
	// We can't easily capture stderr separately, so use combined output
	c := exec.Command("bash", "-n", "-c", cmd)
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bash syntax error: %v\n  output: %s", err, string(out))
	}
	return nil
}

func TestRenderReviewer_NonZeroPR(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")

	_, userPrompt, err := RenderReviewer(1, testIssue, "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error for PR#1: %v", err)
	}

	if !strings.Contains(userPrompt, "1") {
		t.Error("userPrompt should contain PR number 1")
	}
}
