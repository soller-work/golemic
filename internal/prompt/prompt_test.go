package prompt

import (
	"os"
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

// mustContain fails if s does not contain every string in wants.
func mustContain(t *testing.T, s string, wants []string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("prompt missing %q", w)
		}
	}
}

// assertBefore fails if the first occurrence of a comes at or after the first occurrence of b.
func assertBefore(t *testing.T, s, a, b string) {
	t.Helper()
	ai, bi := strings.Index(s, a), strings.Index(s, b)
	if ai < 0 || bi < 0 || ai >= bi {
		t.Errorf("%q must appear before %q (positions %d, %d)", a, b, ai, bi)
	}
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

// AC-001: Dev prompt uses only --title/--body for open-pr; no b64 remnants
func TestRenderDev_OpenPRFlags(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "golemic open-pr --title") {
		t.Error("dev prompt missing 'golemic open-pr --title'")
	}
	if !strings.Contains(userPrompt, "--body") {
		t.Error("dev prompt missing '--body' flag for open-pr")
	}
	for _, banned := range []string{"title-b64", "body-b64", "--issue", "TitleB64", "BodyB64", "TITLE_B64", "BODY_B64"} {
		if strings.Contains(userPrompt, banned) {
			t.Errorf("dev prompt must not contain %q", banned)
		}
	}
}

// AC-001 (facts): Dev prompt contains all run-specific data
func TestRenderDev_ContainsAllFacts(t *testing.T) {
	guidelinesContent := "# Dev Guidelines (Test)\n\n## Stack\nGo 1.21, standard library"
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", guidelinesContent)

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	mustContain(t, userPrompt, []string{
		"42", "Fix bug", "Details here",
		"golemic/issue-42", "go test ./...",
		"# Dev Guidelines (Test)", "Go 1.21, standard library",
		"golemic open-pr",
	})
	if !strings.Contains(userPrompt, "Only after") && !strings.Contains(userPrompt, "only after") {
		t.Error("userPrompt missing condition that open-pr is only allowed after verify exits 0")
	}
}

// AC-002: Dev step list has commit and push before open-pr
func TestRenderDev_CommitAndPushBeforeOpenPR(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	mustContain(t, userPrompt, []string{"git commit", "git push", "golemic open-pr", "git push -u origin golemic/issue-42"})
	assertBefore(t, userPrompt, "git commit", "golemic open-pr")
	assertBefore(t, userPrompt, "git push", "golemic open-pr")
}

// AC-003 (facts): Reviewer prompt contains all run-specific data
func TestRenderReviewer_ContainsAllFacts(t *testing.T) {
	guidelinesContent := "# Reviewer Guidelines (Test)\n\n## Stack\nGo 1.21, standard library"
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", guidelinesContent)

	prNumber := 123
	userPrompt, err := RenderReviewer(prNumber, testIssue, "go test ./...", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "123") {
		t.Error("userPrompt missing PR number 123")
	}
	if !strings.Contains(userPrompt, "42") {
		t.Error("userPrompt missing issue number 42")
	}
	if !strings.Contains(userPrompt, "Fix bug") {
		t.Error("userPrompt missing issue title 'Fix bug'")
	}
	if !strings.Contains(userPrompt, "Details here") {
		t.Error("userPrompt missing issue body 'Details here'")
	}
	if !strings.Contains(userPrompt, "go test ./...") {
		t.Error("userPrompt missing verify command 'go test ./...'")
	}
	if !strings.Contains(userPrompt, "# Reviewer Guidelines (Test)") {
		t.Error("userPrompt missing guidelines content")
	}
	if !strings.Contains(userPrompt, "Go 1.21, standard library") {
		t.Error("userPrompt missing guidelines body content")
	}
	if !strings.Contains(userPrompt, "golemic submit-review") {
		t.Error("userPrompt missing 'golemic submit-review'")
	}
}

// AC-003 (steps): Reviewer prompt has diff-fetch, verify, and submit-review steps
func TestRenderReviewer_StepList(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")

	userPrompt, err := RenderReviewer(123, testIssue, "go test ./...", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "git diff origin/main...HEAD") {
		t.Error("reviewer prompt missing 'git diff origin/main...HEAD' step")
	}
	if !strings.Contains(userPrompt, "gh pr view 123") {
		t.Error("reviewer prompt missing 'gh pr view <PR>' step")
	}
	if !strings.Contains(userPrompt, "go test ./...") {
		t.Error("reviewer prompt missing verify command step")
	}
	if !strings.Contains(userPrompt, "golemic submit-review --verdict") {
		t.Error("reviewer prompt missing 'golemic submit-review --verdict'")
	}
	if !strings.Contains(userPrompt, "--body") {
		t.Error("reviewer prompt missing '--body' for submit-review")
	}
	if !strings.Contains(userPrompt, "--pr 123") {
		t.Error("reviewer prompt missing '--pr 123'")
	}
}

// AC-003: Missing guidelines file returns a named error
func TestRenderDev_MissingGuidelinesError(t *testing.T) {
	nonexistentPath := filepath.Join(t.TempDir(), "nonexistent", "dev.md")

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", nonexistentPath)

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

	userPrompt, err := RenderReviewer(123, testIssue, "go test ./...", nonexistentPath)

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

// AC-005 standalone: Prompt is rendered as a non-empty string in memory
func TestRenderDev_UserPromptNonEmpty(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Test Guidelines")

	userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	if userPrompt == "" {
		t.Error("userPrompt should be a non-empty string")
	}
}

func TestRenderReviewer_UserPromptNonEmpty(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Test Guidelines")

	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath)
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

	userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath)
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

	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath)
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
	userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath)
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
	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath)
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

	userPrompt, err := RenderDev(emptyIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error for empty title/body: %v", err)
	}

	if !strings.Contains(userPrompt, "99") {
		t.Error("userPrompt should still contain issue number")
	}
}

// AC-001: Adversarial input renders without b64 and includes the title/body in the display section
func TestRenderDev_AdversarialInput(t *testing.T) {
	adversarialIssue := Issue{
		Number: 42,
		Title:  `it's a fix`,
		Body:   "`echo it's broken`",
	}
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(adversarialIssue, "branch", "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	for _, banned := range []string{"title-b64", "body-b64", "TITLE_B64", "BODY_B64"} {
		if strings.Contains(userPrompt, banned) {
			t.Errorf("rendered prompt must not contain %q", banned)
		}
	}
	if !strings.Contains(userPrompt, "golemic open-pr") {
		t.Error("golemic open-pr missing from rendered prompt")
	}
	if !strings.Contains(userPrompt, `it's a fix`) {
		t.Error("adversarial title not present in rendered prompt")
	}
}

func TestRenderReviewer_NonZeroPR(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")

	userPrompt, err := RenderReviewer(1, testIssue, "verify", guidelinesPath)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error for PR#1: %v", err)
	}

	if !strings.Contains(userPrompt, "1") {
		t.Error("userPrompt should contain PR number 1")
	}
}
