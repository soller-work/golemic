// Package prompt renders complete, self-contained prompts for dev and reviewer roles.
//
// It provides two rendering functions:
//   - RenderDev: renders a user prompt for the dev role with issue context, branch,
//     verify command, and guidelines injected.
//   - RenderReviewer: renders a user prompt for the reviewer role with PR number,
//     issue context, verify command, and guidelines injected.
//
// Static system prompts are stored in prompts/dev.md and prompts/reviewer.md;
// the caller resolves their paths from the golemic binary directory.
package prompt

import (
	"fmt"
	"os"
	"strings"
	"text/template"
)

// Issue represents a GitHub issue reference passed into a prompt. The full
// task specification is not embedded — agents fetch it at run time via
// `golemic slice --issue N` to keep the initial prompt small.
type Issue struct {
	Number int
	Title  string
}

// devTemplateData holds the template variables for the dev user prompt.
type devTemplateData struct {
	Issue         Issue
	Branch        string
	VerifyCommand string
	Guidelines    string
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

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Guidelines

{{.Guidelines}}

---

## Instructions

1. **First, fetch the authoritative task specification:** run ` + "`" + `golemic slice --issue {{.Issue.Number}}` + "`" + `. The output is either a structured JSON slice or the raw issue body — treat that output as the source of truth. Do not rely on any summary rendered in the issue's web UI.
2. Understand the spec and the guidelines above.
3. Implement the necessary changes on branch ` + "`" + `{{.Branch}}` + "`" + `.
4. Run the verification command: ` + "`" + `{{.VerifyCommand}}` + "`" + `
5. Stage and commit your changes: ` + "`" + `git add -A && git commit -m "<meaningful message>"` + "`" + `
6. Push the branch: ` + "`" + `git push -u origin {{.Branch}}` + "`" + `
7. **Only after ` + "`" + `{{.VerifyCommand}}` + "`" + ` exits 0**, open the PR:
   ` + "`" + `golemic open-pr --title "..." --body "..."` + "`" + `
   The body **must** include a closing keyword so merging auto-closes the issue, e.g. ` + "`" + `Closes #{{.Issue.Number}}` + "`" + `.

> **Important:** Do not run ` + "`" + `gh pr create` + "`" + ` — the runner requires the ` + "`" + `pr_opened` + "`" + ` event that only ` + "`" + `golemic open-pr` + "`" + ` writes. ` + "`" + `golemic open-pr` + "`" + ` is the **sole** permitted method to open a PR.
`

const reviewerUserTemplate = `# Task: Review PR #{{.PRNumber}} for Issue #{{.Issue.Number}}

**Issue Title:** {{.Issue.Title}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Guidelines

{{.Guidelines}}

---

## Instructions

1. **First, fetch the authoritative task specification:** run ` + "`" + `golemic slice --issue {{.Issue.Number}}` + "`" + `. The output is the source of truth for what the PR is supposed to do — do not rely on any summary rendered in the issue's web UI.
2. Fetch the diff: run ` + "`" + `git diff origin/main...HEAD` + "`" + ` and ` + "`" + `gh pr view {{.PRNumber}}` + "`" + `
3. Run the verification command: ` + "`" + `{{.VerifyCommand}}` + "`" + `
4. Review the changes against the spec and the guidelines above.
5. For each finding that can be anchored to a specific file and line, call:
   ` + "`" + `golemic review-comment --pr {{.PRNumber}} --path <file> --line <line> --body "<finding>"` + "`" + `
   - If the command exits 2 (ANCHOR_FAILED), retry **once** with corrected coordinates.
   - If the second attempt also exits 2, include the finding in the ` + "`" + `--body` + "`" + ` of ` + "`" + `submit-review` + "`" + ` instead.
   - Any other error (exit 1) is fatal; do not retry.
6. After posting all inline comments, call **exactly one**:
   ` + "`" + `golemic submit-review --verdict approved|changes_requested --body "..." --pr {{.PRNumber}} --merge-confidence high|low` + "`" + `
   The ` + "`" + `--body` + "`" + ` must summarise all findings, including any that could not be anchored as inline comments.
`

// RenderDev renders a dev user prompt with all run-specific facts injected.
//
// It reads the guidelines file from guidelinesPath, renders the dev user template
// with the given issue, branch, and verifyCommand, and returns the rendered user
// prompt string. The system prompt path is resolved by the caller from the
// golemic binary directory.
//
// Returns an error if the guidelines file does not exist or cannot be read,
// or if template execution fails.
func RenderDev(issue Issue, branch string, verifyCommand string, guidelinesPath string) (userPrompt string, err error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := devTemplateData{
		Issue:         issue,
		Branch:        branch,
		VerifyCommand: verifyCommand,
		Guidelines:    guidelines,
	}

	tmpl, err := template.New("dev").Parse(devUserTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse dev prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("failed to render dev prompt template: %w", err)
	}

	return sb.String(), nil
}

// RenderReviewer renders a reviewer user prompt with all run-specific facts injected.
//
// It reads the guidelines file from guidelinesPath, renders the reviewer user template
// with the given PR number, issue, and verifyCommand, and returns the rendered user
// prompt string. The system prompt path is resolved by the caller from the
// golemic binary directory.
//
// Returns an error if the guidelines file does not exist or cannot be read,
// or if template execution fails.
func RenderReviewer(prNumber int, issue Issue, verifyCommand string, guidelinesPath string) (userPrompt string, err error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := reviewerTemplateData{
		PRNumber:      prNumber,
		Issue:         issue,
		VerifyCommand: verifyCommand,
		Guidelines:    guidelines,
	}

	tmpl, err := template.New("reviewer").Parse(reviewerUserTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse reviewer prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("failed to render reviewer prompt template: %w", err)
	}

	return sb.String(), nil
}

// devRetryTemplateData holds the template variables for the dev retry user prompt.
type devRetryTemplateData struct {
	Issue         Issue
	Branch        string
	Findings      string
	VerifyCommand string
	Guidelines    string
}

const devRetryUserTemplate = `# Dev Retry: Address Review Findings for Issue #{{.Issue.Number}}

**Title:** {{.Issue.Title}}

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Reviewer Findings

The reviewer has requested the following changes:

{{.Findings}}

---

## Guidelines

{{.Guidelines}}

---

## Instructions

1. The reviewer findings above are the primary input for this retry. If you need the original task specification, run ` + "`" + `golemic slice --issue {{.Issue.Number}}` + "`" + ` — its output is the authoritative spec; do not rely on any summary rendered in the issue’s web UI.
2. Address the reviewer\u2019s findings above on branch ` + "`" + `{{.Branch}}` + "`" + `.
3. Run the verification command: ` + "`" + `{{.VerifyCommand}}` + "`" + `
4. Stage and commit your changes: ` + "`" + `git add -A && git commit -m "<meaningful message>"` + "`" + `
5. Push the branch: ` + "`" + `git push origin {{.Branch}}` + "`" + `
`

// RenderDevRetry renders a dev retry user prompt injecting the verbatim reviewer findings.
//
// Returns EMPTY_FINDINGS error if findings is empty (BR-002, IF-001).
func RenderDevRetry(findings string, issue Issue, branch string, verifyCommand string, guidelinesPath string) (userPrompt string, err error) {
	if findings == "" {
		return "", fmt.Errorf("EMPTY_FINDINGS: changes_requested review has an empty body")
	}

	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := devRetryTemplateData{
		Issue:         issue,
		Branch:        branch,
		Findings:      findings,
		VerifyCommand: verifyCommand,
		Guidelines:    guidelines,
	}

	tmpl, err := template.New("devRetry").Parse(devRetryUserTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse dev retry prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("failed to render dev retry prompt template: %w", err)
	}

	return sb.String(), nil
}

// devCIRetryTemplateData holds the template variables for the dev CI retry user prompt.
type devCIRetryTemplateData struct {
	Issue           Issue
	Branch          string
	FailedCheckInfo string
	VerifyCommand   string
	Guidelines      string
}

const devCIRetryUserTemplate = `# CI Retry: Fix Failing Checks for Issue #{{.Issue.Number}}

**Title:** {{.Issue.Title}}

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Failing CI Checks

The following CI checks failed on the PR. Fix the failures and push to the same branch:

{{.FailedCheckInfo}}

---

## Guidelines

{{.Guidelines}}

---

## Instructions

1. The failing checks above are the primary input for this retry. If you need the original task specification, run ` + "`" + `golemic slice --issue {{.Issue.Number}}` + "`" + ` — its output is the authoritative spec; do not rely on any summary rendered in the issue’s web UI.
2. Diagnose and fix the failing CI checks described above on branch ` + "`" + `{{.Branch}}` + "`" + `.
3. Run the verification command locally: ` + "`" + `{{.VerifyCommand}}` + "`" + `
4. Stage and commit your changes: ` + "`" + `git add -A && git commit -m "<meaningful message>"` + "`" + `
5. Push the branch: ` + "`" + `git push origin {{.Branch}}` + "`" + `
6. **Do not open a new PR** — the existing PR on branch ` + "`" + `{{.Branch}}` + "`" + ` will update automatically.
`

// RenderDevCIRetry renders a dev CI retry user prompt injecting failed check info.
//
// Returns EMPTY_FAILED_CHECKS error if failedCheckInfo is empty.
func RenderDevCIRetry(failedCheckInfo string, issue Issue, branch string, verifyCommand string, guidelinesPath string) (userPrompt string, err error) {
	if failedCheckInfo == "" {
		return "", fmt.Errorf("EMPTY_FAILED_CHECKS: no failed check info provided")
	}

	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := devCIRetryTemplateData{
		Issue:           issue,
		Branch:          branch,
		FailedCheckInfo: failedCheckInfo,
		VerifyCommand:   verifyCommand,
		Guidelines:      guidelines,
	}

	tmpl, err := template.New("devCIRetry").Parse(devCIRetryUserTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse dev CI retry prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("failed to render dev CI retry prompt template: %w", err)
	}

	return sb.String(), nil
}

// devRebaseConflictResolveTemplateData holds the template variables for the rebase conflict
// resolution prompt.
type devRebaseConflictResolveTemplateData struct {
	PRNumber        int
	Branch          string
	Base            string
	ConflictedFiles []string
	VerifyCommand   string
	Guidelines      string
}

const devRebaseConflictResolveUserTemplate = `# Rebase Conflict Resolution: PR #{{.PRNumber}}

**Branch:** {{.Branch}}

**Base:** {{.Base}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

**Conflicted Files:**
{{range .ConflictedFiles}}- {{.}}
{{end}}
---

## Guidelines

{{.Guidelines}}

---

## Instructions

Resolve all rebase conflicts and complete the rebase. **Do NOT run ` + "`" + `golemic open-pr` + "`" + `, ` + "`" + `golemic submit-review` + "`" + `, or ` + "`" + `golemic emit` + "`" + ` during this turn.**

1. For each conflicted file listed above, open the file and resolve all conflict markers (` + "`" + `<<<<<<<` + "`" + `, ` + "`" + `=======` + "`" + `, ` + "`" + `>>>>>>>` + "`" + `).
2. Stage the resolved files: ` + "`" + `git add <file>` + "`" + ` for each resolved file.
3. Continue the rebase: ` + "`" + `git rebase --continue` + "`" + `. If further conflicts appear in subsequent commits, repeat steps 1–3.
4. Run the verification command: ` + "`" + `{{.VerifyCommand}}` + "`" + `
5. If verification passes, you are done. Do not open a PR, submit a review, or emit any events.
`

// RenderDevRebaseConflictResolve renders a user prompt for resolving rebase conflicts.
//
// The rendered prompt instructs the dev agent to resolve conflict markers, run
// git add and git rebase --continue, and verify the result — without opening a PR,
// submitting a review, or emitting events.
//
// Returns an error if conflictedFiles is empty, the guidelines file cannot be read,
// or template execution fails.
func RenderDevRebaseConflictResolve(prNumber int, branch, base string, conflictedFiles []string, verifyCommand string, guidelinesPath string) (string, error) {
	if len(conflictedFiles) == 0 {
		return "", fmt.Errorf("EMPTY_CONFLICTED_FILES: conflictedFiles must not be empty")
	}

	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := devRebaseConflictResolveTemplateData{
		PRNumber:        prNumber,
		Branch:          branch,
		Base:            base,
		ConflictedFiles: conflictedFiles,
		VerifyCommand:   verifyCommand,
		Guidelines:      guidelines,
	}

	tmpl, err := template.New("devRebaseConflictResolve").Parse(devRebaseConflictResolveUserTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse dev rebase conflict resolve prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("failed to render dev rebase conflict resolve prompt template: %w", err)
	}

	return sb.String(), nil
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
