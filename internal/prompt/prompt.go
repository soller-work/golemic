// Package prompt renders complete, self-contained prompts for dev and reviewer roles.
//
// It provides two rendering functions:
//   - RenderDev: renders a user prompt for the dev role with issue context, branch,
//     verify command, and guidelines injected.
//   - RenderReviewer: renders a user prompt for the reviewer role with PR number,
//     issue context, verify command, and guidelines injected.
//
// Static system prompts are stored in prompts/dev.md and prompts/reviewer.md
// and returned as file paths alongside the rendered user prompt.
package prompt

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// Issue represents a GitHub issue with its number, title, and body.
type Issue struct {
	Number int
	Title  string
	Body   string
}

// devTemplateData holds the template variables for the dev user prompt.
type devTemplateData struct {
	Issue         Issue
	Branch        string
	VerifyCommand string
	Guidelines    string
	TitleB64      string
	BodyB64       string
}

// reviewerTemplateData holds the template variables for the reviewer user prompt.
type reviewerTemplateData struct {
	PRNumber      int
	Issue         Issue
	VerifyCommand string
	Guidelines    string
}

const devUserTemplate = `# Task: Implement Issue #{{.Issue.Number}}

**Title:** {{.Issue.Title}}

**Description:**
{{.Issue.Body}}

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Guidelines

{{.Guidelines}}

---

## Instructions

1. Understand the issue and the guidelines above.
2. Implement the necessary changes on branch ` + "`" + `{{.Branch}}` + "`" + `.
3. Run the verification command: ` + "`" + `{{.VerifyCommand}}` + "`" + `
4. Store the base64-encoded title and body in variables:
   TITLE_B64={{.TitleB64}}
   BODY_B64={{.BodyB64}}
5. **Only after ` + "`" + `{{.VerifyCommand}}` + "`" + ` exits 0**, call:
   ` + "`" + `golemic open-pr --title-b64 $TITLE_B64 --body-b64 $BODY_B64 --issue {{.Issue.Number}}` + "`" + `
`

const reviewerUserTemplate = `# Task: Review PR #{{.PRNumber}} for Issue #{{.Issue.Number}}

**Issue Title:** {{.Issue.Title}}

**Issue Description:**
{{.Issue.Body}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Guidelines

{{.Guidelines}}

---

## Instructions

1. Review the diff and changes in PR #{{.PRNumber}}.
2. Run the verification command: ` + "`" + `{{.VerifyCommand}}` + "`" + `
3. Write your review findings.
4. Call ` + "`" + `golemic submit-review --verdict approved|changes_requested --body "<your review>" --pr {{.PRNumber}}` + "`" + `
`

// RenderDev renders a dev user prompt with all run-specific facts injected.
//
// It reads the guidelines file from guidelinesPath, renders the dev user template
// with the given issue, branch, and verifyCommand, and returns the system prompt
// file path and the rendered user prompt string.
//
// Returns an error if the guidelines file does not exist or cannot be read,
// or if template execution fails.
func RenderDev(issue Issue, branch string, verifyCommand string, guidelinesPath string) (systemPromptPath string, userPrompt string, err error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", "", err
	}

	titleB64 := base64.StdEncoding.EncodeToString([]byte(issue.Title))
	bodyB64 := base64.StdEncoding.EncodeToString([]byte(issue.Body))

	data := devTemplateData{
		Issue:         issue,
		Branch:        branch,
		VerifyCommand: verifyCommand,
		Guidelines:    guidelines,
		TitleB64:      titleB64,
		BodyB64:       bodyB64,
	}

	tmpl, err := template.New("dev").Parse(devUserTemplate)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse dev prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", "", fmt.Errorf("failed to render dev prompt template: %w", err)
	}

	return filepath.Join("prompts", "dev.md"), sb.String(), nil
}

// RenderReviewer renders a reviewer user prompt with all run-specific facts injected.
//
// It reads the guidelines file from guidelinesPath, renders the reviewer user template
// with the given PR number, issue, and verifyCommand, and returns the system prompt
// file path and the rendered user prompt string.
//
// Returns an error if the guidelines file does not exist or cannot be read,
// or if template execution fails.
func RenderReviewer(prNumber int, issue Issue, verifyCommand string, guidelinesPath string) (systemPromptPath string, userPrompt string, err error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", "", err
	}

	data := reviewerTemplateData{
		PRNumber:      prNumber,
		Issue:         issue,
		VerifyCommand: verifyCommand,
		Guidelines:    guidelines,
	}

	tmpl, err := template.New("reviewer").Parse(reviewerUserTemplate)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse reviewer prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", "", fmt.Errorf("failed to render reviewer prompt template: %w", err)
	}

	return filepath.Join("prompts", "reviewer.md"), sb.String(), nil
}

// readGuidelines reads the guidelines file at the given path.
// Returns an error if the file does not exist or cannot be read.
func readGuidelines(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("guidelines file not found: %s", path)
		}
		return "", fmt.Errorf("failed to read guidelines file %s: %w", path, err)
	}
	return string(content), nil
}