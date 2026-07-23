package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScaffoldFrame_CodeIntelligenceLiteralOnce asserts that the string literal
// "## Code Intelligence" appears exactly once across all .go sources in this package.
func TestScaffoldFrame_CodeIntelligenceLiteralOnce(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read package dir: %v", err)
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		content, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("failed to read %s: %v", e.Name(), err)
		}
		total += strings.Count(string(content), "## Code Intelligence")
	}
	if total != 1 {
		t.Errorf("expected exactly 1 occurrence of \"## Code Intelligence\" in package sources, got %d", total)
	}
}

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

func renderDirectiveAssertions(t *testing.T, name string, render func() (string, error)) {
	t.Helper()

	out, err := render()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", name, err)
	}
	if count := strings.Count(out, workingDirDirective); count != 1 {
		t.Fatalf("%s: expected workingDirDirective exactly once, got %d", name, count)
	}
	assertBefore(t, out, workingDirDirective, "## Instructions")
}

func renderEditDirectiveAssertions(t *testing.T, name string, render func() (string, error)) {
	t.Helper()

	out, err := render()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", name, err)
	}
	if count := strings.Count(out, editOverWriteDirective); count != 1 {
		t.Fatalf("%s: expected editOverWriteDirective exactly once, got %d", name, count)
	}
	assertBefore(t, out, editOverWriteDirective, "## Instructions")
}

func renderNoReReadDirectiveAssertions(t *testing.T, name string, render func() (string, error)) {
	t.Helper()

	out, err := render()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", name, err)
	}
	if count := strings.Count(out, noReReadDirective); count != 1 {
		t.Fatalf("%s: expected noReReadDirective exactly once, got %d", name, count)
	}
	assertBefore(t, out, noReReadDirective, "## Instructions")
}

// AC-001: Dev prompt uses only --title/--body for open-pr; no b64 remnants
func TestRenderDev_OpenPRFlags(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	// Dev prompt cutover: must reference gm_project_check + gm_dev_done, not git/open-pr.
	mustContain(t, userPrompt, []string{"gm_project_check", "gm_dev_done", "gm_slice_get"})
	if strings.Contains(userPrompt, "golemic slice --issue") {
		t.Error("dev prompt must not mention golemic slice CLI")
	}
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
		"gm_project_check", "gm_dev_done",
		"gm_slice_get",
	})
}

// AC-002: Dev step list has gm_project_check before gm_dev_done (cutover from git+open-pr)
func TestRenderDev_CommitAndPushBeforeOpenPR(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")

	userPrompt, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev() unexpected error: %v", err)
	}

	// gm_project_check must appear before gm_dev_done in the step list.
	mustContain(t, userPrompt, []string{"gm_project_check", "gm_dev_done"})
	assertBefore(t, userPrompt, "gm_project_check", "gm_dev_done")
	// Banned terms must not appear as affirmative step instructions (numbered or backtick-command lines).
	// They may appear in prohibition notes ("Do not run `git add`...").
	for _, banned := range []string{"git add -A", "git push -u", "golemic open-pr --title"} {
		if strings.Contains(userPrompt, banned) {
			t.Errorf("dev prompt must not contain affirmative instruction %q after cutover", banned)
		}
	}
}

// AC-003 (facts): Reviewer prompt contains all run-specific data
func TestRenderReviewer_ContainsAllFacts(t *testing.T) {
	guidelinesContent := "# Reviewer Guidelines (Test)\n\n## Stack\nGo 1.21, standard library"
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", guidelinesContent)

	prNumber := 123
	userPrompt, err := RenderReviewer(prNumber, testIssue, "go test ./...", guidelinesPath, false, "")
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
	if !strings.Contains(userPrompt, "gm_slice_get") {
		t.Error("reviewer prompt missing 'gm_slice_get' spec fetch instruction")
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

	// Use a unique verify command that won't appear in any shared directive text.
	userPrompt, err := RenderReviewer(123, testIssue, "my-unique-verify-cmd-12345", guidelinesPath, false, "")
	if err != nil {
		t.Fatalf("RenderReviewer() unexpected error: %v", err)
	}

	if strings.Contains(userPrompt, "git diff origin/main...HEAD") {
		t.Error("reviewer prompt must not contain 'git diff origin/main...HEAD'")
	}
	if strings.Contains(userPrompt, "golemic pr-view") {
		t.Error("reviewer prompt must not contain 'golemic pr-view'")
	}
	if !strings.Contains(userPrompt, "gm_pr_view") {
		t.Error("reviewer prompt missing 'gm_pr_view' step")
	}
	if !strings.Contains(userPrompt, "gm_repo_tree") {
		t.Error("reviewer prompt missing 'gm_repo_tree' step")
	}
	if strings.Contains(userPrompt, "my-unique-verify-cmd-12345") {
		t.Error("reviewer prompt must not instruct agent to run verify command")
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

	userPrompt, err := RenderReviewer(123, testIssue, "go test ./...", nonexistentPath, false, "")

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

func TestRenderWorkingDirDirective(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "guidelines.md", "# Guidelines")

	tests := []struct {
		name   string
		render func() (string, error)
	}{
		{
			name: "RenderDev",
			render: func() (string, error) {
				return RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
			},
		},
		{
			name: "RenderDevRetry",
			render: func() (string, error) {
				return RenderDevRetry("Fix the null pointer", "", testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
			},
		},
		{
			name: "RenderDevCIRetry",
			render: func() (string, error) {
				return RenderDevCIRetry("### verify\n```\ngo test failed\n```\n", testIssue, "golemic/issue-42", "go test ./...", guidelinesPath)
			},
		},
		{
			name: "RenderReviewer",
			render: func() (string, error) {
				return RenderReviewer(123, testIssue, "go test ./...", guidelinesPath, false, "")
			},
		},
		{
			name: "RenderDevRebaseConflictResolve",
			render: func() (string, error) {
				return RenderDevRebaseConflictResolve(42, "golemic/issue-42", "origin/main", []string{"foo.go"}, "go test ./...", guidelinesPath)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			renderDirectiveAssertions(t, tc.name, tc.render)
		})
	}
}

func TestRenderEditOverWriteDirective(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "guidelines.md", "# Guidelines")

	devTests := []struct {
		name   string
		render func() (string, error)
	}{
		{
			name: "RenderDev",
			render: func() (string, error) {
				return RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
			},
		},
		{
			name: "RenderDevRetry",
			render: func() (string, error) {
				return RenderDevRetry("Fix the null pointer", "", testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
			},
		},
		{
			name: "RenderDevCIRetry",
			render: func() (string, error) {
				return RenderDevCIRetry("### verify\n```\ngo test failed\n```\n", testIssue, "golemic/issue-42", "go test ./...", guidelinesPath)
			},
		},
		{
			name: "RenderDevRebaseConflictResolve",
			render: func() (string, error) {
				return RenderDevRebaseConflictResolve(42, "golemic/issue-42", "origin/main", []string{"foo.go"}, "go test ./...", guidelinesPath)
			},
		},
	}

	for _, tc := range devTests {
		t.Run(tc.name, func(t *testing.T) {
			renderEditDirectiveAssertions(t, tc.name, tc.render)
		})
	}

	// Reviewer must NOT contain the edit-over-write directive.
	t.Run("RenderReviewer_NoEditDirective", func(t *testing.T) {
		out, err := RenderReviewer(123, testIssue, "go test ./...", guidelinesPath, false, "")
		if err != nil {
			t.Fatalf("RenderReviewer: unexpected error: %v", err)
		}
		if strings.Contains(out, editOverWriteDirective) {
			t.Error("reviewer prompt must not contain editOverWriteDirective")
		}
	})
}

func TestRenderNoReReadDirective(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "guidelines.md", "# Guidelines")

	devTests := []struct {
		name   string
		render func() (string, error)
	}{
		{
			name: "RenderDev",
			render: func() (string, error) {
				return RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
			},
		},
		{
			name: "RenderDevRetry",
			render: func() (string, error) {
				return RenderDevRetry("Fix the null pointer", "", testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
			},
		},
		{
			name: "RenderDevCIRetry",
			render: func() (string, error) {
				return RenderDevCIRetry("### verify\n```\ngo test failed\n```\n", testIssue, "golemic/issue-42", "go test ./...", guidelinesPath)
			},
		},
		{
			name: "RenderDevRebaseConflictResolve",
			render: func() (string, error) {
				return RenderDevRebaseConflictResolve(42, "golemic/issue-42", "origin/main", []string{"foo.go"}, "go test ./...", guidelinesPath)
			},
		},
	}

	for _, tc := range devTests {
		t.Run(tc.name, func(t *testing.T) {
			renderNoReReadDirectiveAssertions(t, tc.name, tc.render)
		})
	}

	// Reviewer must NOT contain the no-re-read directive.
	t.Run("RenderReviewer_NoReReadDirective", func(t *testing.T) {
		out, err := RenderReviewer(123, testIssue, "go test ./...", guidelinesPath, false, "")
		if err != nil {
			t.Fatalf("RenderReviewer: unexpected error: %v", err)
		}
		if strings.Contains(out, noReReadDirective) {
			t.Error("reviewer prompt must not contain noReReadDirective")
		}
	})
}

func TestRenderWorkingDirDirective_WithEmptyGuidelines(t *testing.T) {
	dir := t.TempDir()
	emptyGuidelinesPath := writeTestGuidelines(t, dir, "guidelines.md", "")

	tests := []struct {
		name   string
		render func() (string, error)
	}{
		{
			name: "RenderDev",
			render: func() (string, error) {
				return RenderDev(testIssue, "golemic/issue-42", "go test ./...", emptyGuidelinesPath, false)
			},
		},
		{
			name: "RenderDevRetry",
			render: func() (string, error) {
				return RenderDevRetry("Fix the null pointer", "", testIssue, "golemic/issue-42", "go test ./...", emptyGuidelinesPath, false)
			},
		},
		{
			name: "RenderDevCIRetry",
			render: func() (string, error) {
				return RenderDevCIRetry("### verify\n```\ngo test failed\n```\n", testIssue, "golemic/issue-42", "go test ./...", emptyGuidelinesPath)
			},
		},
		{
			name: "RenderReviewer",
			render: func() (string, error) {
				return RenderReviewer(123, testIssue, "go test ./...", emptyGuidelinesPath, false, "")
			},
		},
		{
			name: "RenderDevRebaseConflictResolve",
			render: func() (string, error) {
				return RenderDevRebaseConflictResolve(42, "golemic/issue-42", "origin/main", []string{"foo.go"}, "go test ./...", emptyGuidelinesPath)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			renderDirectiveAssertions(t, tc.name, tc.render)
		})
	}
}

func TestRenderReviewer_UserPromptNonEmpty(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Test Guidelines")

	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false, "")
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

	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false, "")
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

	// After cutover: step list ends with gm_dev_done, not golemic open-pr.
	if !strings.Contains(userPrompt, "gm_dev_done") {
		t.Error("step list must contain 'gm_dev_done'")
	}
	if strings.LastIndex(userPrompt, "gm_dev_done") < strings.LastIndex(userPrompt, "Instructions") {
		t.Error("'gm_dev_done' should appear near the end of the user prompt")
	}
}

// Step list in reviewer prompt ends with submit-review
func TestRenderReviewer_StepListEndsWithSubmitReview(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")
	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false, "")
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
	userPrompt, err := RenderReviewer(123, testIssue, "verify", guidelinesPath, false, "")
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

	// After cutover: neither gh pr create nor golemic open-pr appear as affirmative instructions.
	// The prompt must reference gm_dev_done as the terminal tool.
	if !strings.Contains(userPrompt, "gm_dev_done") {
		t.Fatal("dev prompt missing 'gm_dev_done'")
	}

	// gh pr create and golemic open-pr must only appear in a prohibition context (if at all).
	lower := strings.ToLower(userPrompt)
	for _, term := range []string{"gh pr create", "golemic open-pr"} {
		if idx := strings.Index(lower, term); idx >= 0 {
			window := lower[max(0, idx-100):min(len(lower), idx+100)]
			hasNeg := strings.Contains(window, "do not") || strings.Contains(window, "must not") ||
				strings.Contains(window, "never") || strings.Contains(window, "do **not**")
			if !hasNeg {
				t.Errorf("%q appears in dev prompt without a nearby negative directive; context: %q", term, window)
			}
		}
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
	if !strings.Contains(userPrompt, "gm_dev_done") {
		t.Error("gm_dev_done missing from rendered prompt after cutover")
	}
	if !strings.Contains(userPrompt, `it's a fix`) {
		t.Error("adversarial title not present in rendered prompt")
	}
}

func TestRenderReviewer_NonZeroPR(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")

	userPrompt, err := RenderReviewer(1, testIssue, "verify", guidelinesPath, false, "")
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
		"gm_slice_get",
		"authoritative spec",
	})
	if strings.Contains(p, "golemic slice --issue") {
		t.Error("dev CI retry prompt must not mention golemic slice CLI")
	}
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
	mustContain(t, p, []string{"99", "Test CI retry", "make test", "gm_slice_get"})
	if strings.Contains(p, "golemic slice --issue") {
		t.Error("dev CI retry prompt must not mention golemic slice CLI")
	}
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
		"gm_slice_get",
		"authoritative spec",
	})
	if strings.Contains(p, "golemic slice --issue") {
		t.Error("dev retry prompt must not mention golemic slice CLI")
	}
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

// codeToolNames are the gm_code_* tool names that appear in prompts when CBM is on.
var codeToolNames = []string{
	"gm_code_search_graph", "gm_code_search", "gm_code_get_snippet",
	"gm_code_trace_call_path", "gm_code_query_graph", "gm_code_get_architecture",
	"gm_code_get_graph_schema", "gm_code_detect_changes",
}

func TestRenderDev_CodebaseMemoryOff_NoSection(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, word := range append(codeToolNames, "Code Intelligence") {
		if strings.Contains(p, word) {
			t.Errorf("flag-off dev prompt must not contain %q", word)
		}
	}
}

func TestRenderDev_CodebaseMemoryOn_HasCBMHint(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDev(testIssue, "golemic/issue-42", "go test ./...", guidelinesPath, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, append([]string{"Code Intelligence"}, codeToolNames...))
	if strings.Count(p, "gm_code_search_graph") != 1 {
		t.Error("dev prompt must contain gm_code_search_graph exactly once")
	}
	if strings.Contains(p, "golemic cbm") {
		t.Error("dev prompt must not contain golemic cbm instruction (BR-6)")
	}
}

func TestRenderReviewer_CodebaseMemoryOff_NoSection(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")
	p, err := RenderReviewer(42, testIssue, "go test ./...", guidelinesPath, false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, word := range append(codeToolNames, "Code Intelligence") {
		if strings.Contains(p, word) {
			t.Errorf("flag-off reviewer prompt must not contain %q", word)
		}
	}
}

func TestRenderReviewer_CodebaseMemoryOn_HasCBMHint(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "reviewer.md", "# Guidelines")
	p, err := RenderReviewer(42, testIssue, "go test ./...", guidelinesPath, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, append([]string{"Code Intelligence"}, codeToolNames...))
	if strings.Contains(p, "golemic cbm") {
		t.Error("reviewer prompt must not contain golemic cbm instruction (BR-6)")
	}
}

func TestRenderDevRetry_CodebaseMemoryOn_HasCBMHint(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDevRetry("fix null ptr", "", testIssue, "golemic/issue-42", "go test", guidelinesPath, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustContain(t, p, append([]string{"Code Intelligence"}, codeToolNames...))
	if strings.Contains(p, "golemic cbm") {
		t.Error("dev retry prompt must not contain golemic cbm instruction (BR-6)")
	}
}

func TestRenderDevRetry_CodebaseMemoryOff_NoSection(t *testing.T) {
	guidelinesPath := writeTestGuidelines(t, t.TempDir(), "dev.md", "# Guidelines")
	p, err := RenderDevRetry("fix null ptr", "", testIssue, "golemic/issue-42", "go test", guidelinesPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, word := range append(codeToolNames, "Code Intelligence") {
		if strings.Contains(p, word) {
			t.Errorf("flag-off dev retry prompt must not contain %q", word)
		}
	}
}
