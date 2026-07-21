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

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "golemic open-pr --title") {
		t.Error("dev prompt missing 'golemic open-pr --title'")
	}
	if !strings.Contains(userPrompt, "--body") {
		t.Error("dev prompt missing '--body' flag for open-pr")
	}
	// --issue is now a legitimate flag on `golemic slice --issue N` (spec fetch),
	// so it is not part of the banned set.
	for _, banned := range []string{"title-b64", "body-b64", "TitleB64", "BodyB64", "TITLE_B64", "BODY_B64"} {
		if strings.Contains(userPrompt, banned) {
			t.Errorf("dev prompt must not contain %q", banned)
		}
	}
}

// AC-001 (facts): Dev prompt contains all run-specific data
func TestRenderDev_ContainsAllFacts(t *testing.T) {
	guidelinesContent := "# Dev Guidelines (Test)\n\n## Stack\nGo 1.21, standard library"
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", guidelinesContent)

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	mustContain(t, userPrompt, []string{
		"42", "Fix bug",
		"golemic/issue-42", "go test ./...",
		"# Dev Guidelines (Test)", "Go 1.21, standard library",
		"golemic open-pr",
		"golemic slice --issue 42",
	})
	if !strings.Contains(userPrompt, "Only after") && !strings.Contains(userPrompt, "only after") {
		t.Error("userPrompt missing condition that open-pr is only allowed after verify exits 0")
	}
}

// AC-002: Dev step list has commit and push before open-pr
func TestRenderDev_CommitAndPushBeforeOpenPR(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
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
	userPrompt, err := RenderReviewer(prNumber, testIssue, "go test ./...", guidelinesPath, false)
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
	if !strings.Contains(userPrompt, "golemic slice --issue 42") {
		t.Error("reviewer prompt missing 'golemic slice --issue 42' spec fetch instruction")
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

	userPrompt, err := RenderReviewer(123, testIssue, "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "git diff origin/main...HEAD") {
		t.Error("reviewer prompt missing 'git diff origin/main...HEAD' step")
	}
	if !strings.Contains(userPrompt, "golemic pr-view --pr 123") {
		t.Error("reviewer prompt missing 'golemic pr-view --pr <PR>' step")
	}
	if strings.Contains(userPrompt, "gh pr view") {
		t.Error("reviewer prompt must not contain raw 'gh pr view' instruction")
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

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", nonexistentPath, false)

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

	userPrompt, err := RenderReviewer(123, testIssue, "go test ./...", nonexistentPath, false)

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

	userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	if userPrompt == "" {
		t.Error("userPrompt should be a non-empty string")
	}
}

func TestRenderReviewer_UserPromptNonEmpty(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Test Guidelines")

	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false)
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

	userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath, false)
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

	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false)
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
	userPrompt, err := RenderDev(testIssue, "branch", "verify", guidelinesPath, false)
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
	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false)
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

// AC-124: Reviewer prompt exposes all three merge-confidence tiers
func TestRenderReviewer_MergeConfidenceAllTiers(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")
	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if !strings.Contains(userPrompt, "--merge-confidence high|medium|low") {
		t.Error("reviewer prompt must offer --merge-confidence high|medium|low (all three tiers)")
	}
	if strings.Contains(userPrompt, "--merge-confidence high|low") {
		t.Error("reviewer prompt must not offer only two tiers (high|low); medium must be present")
	}
}

// Issue with empty title or body still renders (no panic)
func TestRenderDev_EmptyTitleBody(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	emptyIssue := Issue{Number: 99, Title: ""}

	userPrompt, err := RenderDev(emptyIssue, "branch", "verify", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error for empty title/body: %v", err)
	}

	if !strings.Contains(userPrompt, "99") {
		t.Error("userPrompt should still contain issue number")
	}
}

// AS-1/AS-2: Dev prompt prohibits gh pr create and mandates golemic open-pr as sole PR opener
func TestRenderDev_GhPrCreateProhibition(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	// AS-1: 'gh pr create' must appear in a sentence with a negative directive
	lower := strings.ToLower(userPrompt)
	ghIdx := strings.Index(lower, "gh pr create")
	if ghIdx < 0 {
		t.Fatal("dev prompt missing 'gh pr create'")
	}
	// Check that 'do not', 'must not', or 'never' appears in the same sentence (within 80 chars before or after)
	window := lower[max(0, ghIdx-80):min(len(lower), ghIdx+80)]
	foundNegative := strings.Contains(window, "do not") || strings.Contains(window, "must not") || strings.Contains(window, "never")
	if !foundNegative {
		t.Error("'gh pr create' must appear near a negative directive ('do not', 'must not', or 'never')")
	}

	// AS-2: 'golemic open-pr' must be bound to exclusivity ('only', 'sole', or 'exclusively')
	openPRIdx := strings.Index(lower, "golemic open-pr")
	if openPRIdx < 0 {
		t.Fatal("dev prompt missing 'golemic open-pr'")
	}
	// Search around the last occurrence of 'golemic open-pr' for the exclusivity clause
	lastOpenPRIdx := strings.LastIndex(lower, "golemic open-pr")
	exclusiveWindow := lower[max(0, lastOpenPRIdx-80):min(len(lower), lastOpenPRIdx+80)]
	foundExclusive := strings.Contains(exclusiveWindow, "only") || strings.Contains(exclusiveWindow, "sole") || strings.Contains(exclusiveWindow, "exclusively")
	if !foundExclusive {
		t.Error("dev prompt must bind 'golemic open-pr' to an exclusivity word ('only', 'sole', or 'exclusively')")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// AC-001: Adversarial input renders without b64 and includes the title/body in the display section
func TestRenderDev_AdversarialInput(t *testing.T) {
	adversarialIssue := Issue{
		Number: 42,
		Title:  `it's a fix`,
	}
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(adversarialIssue, "branch", "verify", guidelinesPath, false)
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

	userPrompt, err := RenderReviewer(1, testIssue, "verify", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error for PR#1: %v", err)
	}

	if !strings.Contains(userPrompt, "1") {
		t.Error("userPrompt should contain PR number 1")
	}
}

// ---------------------------------------------------------------------------
// RenderDevCIRetry tests (issue-13)
// ---------------------------------------------------------------------------

func TestRenderDevCIRetry_ContainsFailedCheckInfo(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	failedCheckInfo := "### verify\n```\ngo test failed\n```\n"

	p, err := RenderDevCIRetry(failedCheckInfo, testIssue, "golemic/issue-42", "go test", guidelinesPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, []string{
		failedCheckInfo,
		"golemic/issue-42",
		"go test",
		"Do not open a new PR",
		"golemic slice --issue 42",
		"authoritative spec",
	})
}

func TestRenderDevCIRetry_EmptyInfoError(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	_, err := RenderDevCIRetry("", testIssue, "golemic/issue-42", "go test", guidelinesPath)
	if err == nil {
		t.Fatal("expected EMPTY_FAILED_CHECKS error, got nil")
	}
	if !strings.Contains(err.Error(), "EMPTY_FAILED_CHECKS") {
		t.Errorf("expected EMPTY_FAILED_CHECKS in error, got: %v", err)
	}
}

func TestRenderDevCIRetry_InjectedIssueContext(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	issue := Issue{Number: 99, Title: "Test CI retry"}
	p, err := RenderDevCIRetry("check info", issue, "golemic/issue-99", "make test", guidelinesPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, []string{"99", "Test CI retry", "make test", "golemic slice --issue 99"})
}

// ---------------------------------------------------------------------------
// RenderDevRebaseConflictResolve — AC-006
// ---------------------------------------------------------------------------

func TestRenderDevRebaseConflictResolve_ContainsAllFacts(t *testing.T) {
	dir := t.TempDir()
	guidelinesPath := writeTestGuidelines(t, dir, "dev.md", "# Dev Guidelines")

	out, err := RenderDevRebaseConflictResolve(
		42,
		"golemic/issue-42",
		"origin/main",
		[]string{"foo.go", "bar.go"},
		"go build ./... && go test ./...",
		guidelinesPath,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustContain(t, out, []string{
		"42",
		"golemic/issue-42",
		"origin/main",
		"foo.go",
		"bar.go",
		"go build ./... && go test ./...",
		"# Dev Guidelines",
	})
}

func TestRenderDevRebaseConflictResolve_ProhibitsAgentSubcommands(t *testing.T) {
	dir := t.TempDir()
	guidelinesPath := writeTestGuidelines(t, dir, "dev.md", "# Guidelines")

	out, err := RenderDevRebaseConflictResolve(
		42,
		"golemic/issue-42",
		"origin/main",
		[]string{"foo.go"},
		"go test ./...",
		guidelinesPath,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustContain(t, out, []string{
		"golemic open-pr",
		"golemic submit-review",
		"golemic emit",
	})
}

func TestRenderDevRebaseConflictResolve_StepListOrder(t *testing.T) {
	dir := t.TempDir()
	guidelinesPath := writeTestGuidelines(t, dir, "dev.md", "# Guidelines")

	out, err := RenderDevRebaseConflictResolve(
		42,
		"golemic/issue-42",
		"origin/main",
		[]string{"foo.go"},
		"go test ./...",
		guidelinesPath,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// git add must appear before git rebase --continue in the step list
	assertBefore(t, out, "git add", "git rebase --continue")
	// The step instructing to run verify_command must appear after git rebase --continue.
	// We check the step number prefix to avoid matching the header occurrence.
	assertBefore(t, out, "git rebase --continue", "4. Run the verification command")
}

func TestRenderDevRebaseConflictResolve_EmptyFilesError(t *testing.T) {
	dir := t.TempDir()
	guidelinesPath := writeTestGuidelines(t, dir, "dev.md", "# Guidelines")

	_, err := RenderDevRebaseConflictResolve(42, "golemic/issue-42", "origin/main", []string{}, "go test", guidelinesPath)
	if err == nil {
		t.Error("expected error for empty conflictedFiles, got nil")
	}
	if !strings.Contains(err.Error(), "EMPTY_CONFLICTED_FILES") {
		t.Errorf("error should contain EMPTY_CONFLICTED_FILES, got: %v", err)
	}
}

func TestRenderDevRebaseConflictResolve_MissingGuidelinesError(t *testing.T) {
	_, err := RenderDevRebaseConflictResolve(42, "branch", "origin/main", []string{"foo.go"}, "go test", "/nonexistent/dev.md")
	if err == nil {
		t.Error("expected error for missing guidelines file, got nil")
	}
}

// ---------------------------------------------------------------------------
// RenderDevRetry tests (AC-002 trace: FindingsJSON injection)
// ---------------------------------------------------------------------------

func TestRenderDevRetry_ContainsFindingsJSON(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	findings := "Fix the null pointer"
	findingsJSON := `[{"path":"main.go","line":10,"side":"RIGHT","body":"Nil pointer risk"}]`

	p, err := RenderDevRetry(findings, findingsJSON, testIssue, "golemic/issue-42", "go test", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDevRetry: %v", err)
	}

	mustContain(t, p, []string{
		findings,
		"FindingsJSON",
		findingsJSON,
		"golemic slice --issue 42",
		"authoritative spec",
	})
}

func TestRenderDevRetry_NoFindingsJSONSectionWhenEmpty(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	p, err := RenderDevRetry("Some findings", "", testIssue, "golemic/issue-42", "go test", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDevRetry: %v", err)
	}

	if strings.Contains(p, "FindingsJSON") {
		t.Errorf("prompt must not contain FindingsJSON section when findingsJSON is empty; got: %s", p)
	}
}

func TestRenderDevRetry_EmptyFindingsError(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	_, err := RenderDevRetry("", "", testIssue, "golemic/issue-42", "go test", guidelinesPath, false)
	if err == nil {
		t.Fatal("expected EMPTY_FINDINGS error for empty findings, got nil")
	}
	if !strings.Contains(err.Error(), "EMPTY_FINDINGS") {
		t.Errorf("expected EMPTY_FINDINGS in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CodebaseMemory prompt tests (issue-92)
// ---------------------------------------------------------------------------

func TestRenderDev_CodebaseMemoryOff_NoSection(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, tool := range []string{"search_graph", "trace_call_path", "detect_changes", "Code Intelligence"} {
		if strings.Contains(p, tool) {
			t.Errorf("flag-off dev prompt must not contain %q", tool)
		}
	}
}

func TestRenderDev_CodebaseMemoryOn_HasDevTools(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, []string{
		"Code Intelligence",
		"golemic cbm search_graph",
		"golemic cbm search_code",
		"golemic cbm get_code_snippet",
		"golemic cbm trace_call_path",
		"golemic cbm query_graph",
		"golemic cbm get_architecture",
		"golemic cbm get_graph_schema",
		"Prefer",
	})
	if strings.Contains(p, "detect_changes") {
		t.Error("dev prompt must not contain detect_changes (reviewer-only per BR-4)")
	}
}

func TestRenderReviewer_CodebaseMemoryOff_NoSection(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")
	p, err := RenderReviewer(42, testIssue, "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, tool := range []string{"search_graph", "detect_changes", "Code Intelligence"} {
		if strings.Contains(p, tool) {
			t.Errorf("flag-off reviewer prompt must not contain %q", tool)
		}
	}
}

func TestRenderReviewer_CodebaseMemoryOn_HasReviewerTools(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")
	p, err := RenderReviewer(42, testIssue, "go test ./...", guidelinesPath, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, []string{
		"Code Intelligence",
		"golemic cbm search_graph",
		"golemic cbm search_code",
		"golemic cbm get_code_snippet",
		"golemic cbm trace_call_path",
		"golemic cbm query_graph",
		"golemic cbm get_architecture",
		"golemic cbm get_graph_schema",
		"golemic cbm detect_changes",
		"Prefer",
	})
}

func TestRenderDevRetry_CodebaseMemoryOn_HasDevTools(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDevRetry("fix null ptr", "", testIssue, "golemic/issue-42", "go test", guidelinesPath, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, []string{
		"Code Intelligence",
		"golemic cbm search_graph",
		"golemic cbm search_code",
		"golemic cbm get_code_snippet",
		"golemic cbm trace_call_path",
		"golemic cbm get_graph_schema",
		"Prefer",
	})
	if strings.Contains(p, "detect_changes") {
		t.Error("dev retry prompt must not contain detect_changes")
	}
}

func TestRenderDevRetry_CodebaseMemoryOff_NoSection(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDevRetry("fix null ptr", "", testIssue, "golemic/issue-42", "go test", guidelinesPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(p, "Code Intelligence") || strings.Contains(p, "search_graph") {
		t.Error("flag-off dev retry prompt must not contain Code Intelligence section")
	}
}
