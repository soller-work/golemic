// Package prompt renders complete, self-contained prompts for dev and reviewer roles.
//
// It provides two rendering functions:
//   - RenderDev: renders a user prompt for the dev role with issue context, branch,
//     verify command, and guidelines injected.
//   - RenderReviewer: renders a user prompt for the reviewer role with PR number,
//     issue context, verify command, and guidelines injected.
//
// The role system prompt (persona body) is supplied by the caller from
// .golemic/agents/{role}.md; these workflow prompts are injected into the user prompt.
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
	Issue          Issue
	Branch         string
	VerifyCommand  string
	Guidelines     string
	CodebaseMemory bool
	Directives     string
}

// reviewerTemplateData holds the template variables for the reviewer user prompt.
type reviewerTemplateData struct {
	PRNumber       int
	Issue          Issue
	VerifyCommand  string
	Guidelines     string
	CodebaseMemory bool
	Directives     string
	PrecheckBlock  string
}

// workingDirDirective is injected into every runner prompt before ## Instructions.
// Agents must not prefix bash commands with cd <worktree> because cmd.Dir is already set.
const workingDirDirective = "## Working Directory\n\nYour `bash` tool starts each invocation with `cwd` already set to the worktree root. Do **not** prefix commands with `cd <path>` — it wastes tokens and clutters the run's progress display. If you must operate in a subdirectory for a single call, use a subshell, e.g. `(cd internal/foo && go test ./...)`."

// editOverWriteDirective is injected into every dev prompt before ## Instructions.
// Prefers targeted edits over full-file rewrites to keep run-context token usage low.
const editOverWriteDirective = "## File Edits\n\nPrefer the `edit` tool over `write` when modifying a file that already exists. Reserve `write` for new files or when replacing substantially all of a file's content. A full-file `write` re-emits the entire file into the run context and grows tokens over a long run."

// noReReadDirective is injected into every dev prompt before ## Instructions.
// Nudges the dev agent to avoid re-reading unchanged files at full length to keep token usage low.
const noReReadDirective = "## File Re-reads\n\nKeep track of files you have already read during this run. Do **not** re-read an unchanged file in full — re-reading re-emits the whole file into the run context and grows tokens over a long run. If you only need part of a file, use a targeted `read` range (offset/limit) or a code-intelligence tool lookup instead. A fresh full read is correct when the file has changed since you last read it (e.g. after an `edit`, `write`, or a command that rewrote it)."

// scaffoldFrame is the shared middle section of every renderer: Guidelines block,
// optional Code Intelligence block, injected Directives, and the ## Instructions header.
// Each renderer concatenates its unique header + scaffoldFrame + unique instruction steps.
const scaffoldFrame = `---

## Guidelines

{{.Guidelines}}
{{if .CodebaseMemory}}
---

## Code Intelligence

The worktree is indexed into a code-intelligence graph. Use the following tools for structural exploration — they answer in one call instead of many:

- ` + "`gm_code_search_graph`" + ` — find functions, classes, routes, variables (BM25 / regex / vector)
- ` + "`gm_code_search`" + ` — grep + graph enrichment for text patterns
- ` + "`gm_code_get_snippet`" + ` — read source for a qualified name
- ` + "`gm_code_trace_call_path`" + ` — trace callers/callees or data-flow paths
- ` + "`gm_code_query_graph`" + ` — Cypher query for multi-hop patterns
- ` + "`gm_code_get_architecture`" + ` — high-level overview: packages, clusters, entry points
- ` + "`gm_code_get_graph_schema`" + ` — node labels and edge types
- ` + "`gm_code_detect_changes`" + ` — detect files/symbols changed since a ref
{{end}}
---

{{.Directives}}

---

## Instructions`

const devUserTemplate = `# Task: Implement Issue #{{.Issue.Number}}

**Title:** {{.Issue.Title}}

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

` + scaffoldFrame + `

1. **First, fetch the authoritative task specification:** run ` + "`" + `gm_slice_get` + "`" + ` for issue ` + "`" + `{{.Issue.Number}}` + "`" + `. The output is the source of truth — do not rely on any summary rendered in the issue's web UI.
2. Understand the spec and the guidelines above.
3. Implement the necessary changes on branch ` + "`" + `{{.Branch}}` + "`" + `.
4. Run ` + "`" + `gm_project_check` + "`" + ` iteratively until it returns ` + "`" + `ok: true` + "`" + `. Fix any failures before proceeding.
5. Once ` + "`" + `gm_project_check` + "`" + ` returns ` + "`" + `ok: true` + "`" + `, call ` + "`" + `gm_dev_done` + "`" + ` with:
   - ` + "`" + `summary` + "`" + `: a brief description of the changes made
   - ` + "`" + `commitMsg` + "`" + `: a Conventional Commit message, e.g. ` + "`" + `feat(scope): description ({{.Issue.Number}})` + "`" + `
   - ` + "`" + `prTitle` + "`" + `: a concise PR title
   - ` + "`" + `prBody` + "`" + `: the PR description — **must** include ` + "`" + `Closes #{{.Issue.Number}}` + "`" + `

> **Important:** Do **not** run ` + "`" + `git add` + "`" + `, ` + "`" + `git commit` + "`" + `, ` + "`" + `git push` + "`" + `, or ` + "`" + `golemic open-pr` + "`" + `. The runner performs all of those steps automatically after ` + "`" + `gm_dev_done` + "`" + ` is accepted.
`

const reviewerUserTemplate = `# Task: Review PR #{{.PRNumber}} for Issue #{{.Issue.Number}}

**Issue Title:** {{.Issue.Title}}

` + scaffoldFrame + `

{{.PrecheckBlock}}

1. **First, fetch the authoritative task specification:** call ` + "`" + `gm_slice_get` + "`" + ` — its output is the source of truth for what the PR is supposed to do; do not rely on any summary in the web UI.
2. **Fetch the PR:** call ` + "`" + `gm_pr_view` + "`" + ` — it returns PR metadata, the unified diff, and the changed-files list.
3. **Navigate the repo:** use ` + "`" + `gm_repo_tree` + "`" + ` (directory listing) and ` + "`" + `read` + "`" + ` (file contents) to explore context around changed files.
4. Review the changes against the spec and the guidelines above. Do **not** run the verify command — the runner has already run it; see the Precheck Result above.
5. For each finding that can be anchored to a specific file and line, call ` + "`" + `gm_review_submit_comment` + "`" + ` with ` + "`" + `{ path, line, body, severity }` + "`" + `.
   - If it returns ` + "`" + `{ ok: false, code: "ANCHOR_INVALID" }` + "`" + `, retry **once** with corrected coordinates.
   - If the second attempt also returns ANCHOR_INVALID, include the finding in the ` + "`" + `body` + "`" + ` of ` + "`" + `gm_review_submit` + "`" + ` instead.
6. After posting all inline comments, call **exactly one** ` + "`" + `gm_review_submit` + "`" + ` with ` + "`" + `{ verdict, mergeConfidence, body }` + "`" + `.
   - ` + "`" + `verdict` + "`" + `: ` + "`" + `"approved"` + "`" + ` or ` + "`" + `"changes_requested"` + "`" + `
   - ` + "`" + `mergeConfidence` + "`" + `: ` + "`" + `"high"` + "`" + `, ` + "`" + `"medium"` + "`" + `, or ` + "`" + `"low"` + "`" + `
   - ` + "`" + `body` + "`" + ` must summarise all findings, including any that could not be anchored as inline comments.
   - **If the Precheck Result above was not ok, you MUST use ` + "`" + `changes_requested` + "`" + ` and explain why.**
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
func RenderDev(issue Issue, branch string, verifyCommand string, guidelinesPath string, cbmEnabled bool) (userPrompt string, err error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := devTemplateData{
		Issue:          issue,
		Branch:         branch,
		VerifyCommand:  verifyCommand,
		Guidelines:     guidelines,
		CodebaseMemory: cbmEnabled,
		Directives:     workingDirDirective + "\n\n" + editOverWriteDirective + "\n\n" + noReReadDirective,
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
func RenderReviewer(prNumber int, issue Issue, verifyCommand string, guidelinesPath string, cbmEnabled bool, precheckBlock string) (userPrompt string, err error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := reviewerTemplateData{
		PRNumber:       prNumber,
		Issue:          issue,
		VerifyCommand:  verifyCommand,
		Guidelines:     guidelines,
		CodebaseMemory: cbmEnabled,
		Directives:     workingDirDirective,
		PrecheckBlock:  precheckBlock,
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
	Issue          Issue
	Branch         string
	Findings       string
	FindingsJSON   string
	VerifyCommand  string
	Guidelines     string
	CodebaseMemory bool
	Directives     string
}

const devRetryUserTemplate = `# Dev Retry: Address Review Findings for Issue #{{.Issue.Number}}

**Title:** {{.Issue.Title}}

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Reviewer Findings

The reviewer has requested the following changes:

{{.Findings}}
{{if .FindingsJSON}}
## Inline Findings (FindingsJSON)

The following JSON array contains the reviewer's inline comments anchored to specific code locations. Each entry has ` + "`" + `path` + "`" + `, ` + "`" + `line` + "`" + `, ` + "`" + `side` + "`" + `, and ` + "`" + `body` + "`" + ` fields. Navigate directly to each location to address the finding.

` + "```" + `json
{{.FindingsJSON}}
` + "```" + `
{{end}}
` + scaffoldFrame + `

1. The reviewer findings above are the primary input for this retry. If you need the original task specification, run ` + "`" + `gm_slice_get` + "`" + ` for issue ` + "`" + `{{.Issue.Number}}` + "`" + ` — its output is the authoritative spec; do not rely on any summary rendered in the issue's web UI.
2. Address the reviewer\u2019s findings above on branch ` + "`" + `{{.Branch}}` + "`" + `.
3. Run ` + "`" + `gm_project_check` + "`" + ` iteratively until it returns ` + "`" + `ok: true` + "`" + `. Fix any failures before proceeding.
4. Once ` + "`" + `gm_project_check` + "`" + ` returns ` + "`" + `ok: true` + "`" + `, call ` + "`" + `gm_dev_done` + "`" + ` with summary, commitMsg, prTitle, and prBody.

> **Important:** Do **not** run ` + "`" + `git add` + "`" + `, ` + "`" + `git commit` + "`" + `, ` + "`" + `git push` + "`" + `, or ` + "`" + `golemic open-pr` + "`" + `. The runner handles those steps. Do **not** open a new PR — the existing PR on branch ` + "`" + `{{.Branch}}` + "`" + ` will be updated automatically.
`

// devGateRetryTemplateData holds the template variables for a gate-retry dev prompt.
type devGateRetryTemplateData struct {
	Issue          Issue
	Branch         string
	VerifyCommand  string
	GateReason     string
	Guidelines     string
	Directives     string
	CodebaseMemory bool
}

const devGateRetryUserTemplate = `# Gate Retry: Fix Verification Before Completing Issue #{{.Issue.Number}}

**Title:** {{.Issue.Title}}

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

**Previous gm_dev_done rejection:** {{.GateReason}}

` + scaffoldFrame + `

Your previous ` + "`" + `gm_dev_done` + "`" + ` call was rejected by the acceptance gate. To proceed:

1. Run ` + "`" + `gm_project_check` + "`" + `. It must return ` + "`" + `ok: true` + "`" + `. Fix any failures.
2. Do **not** modify files after ` + "`" + `gm_project_check` + "`" + ` returns ` + "`" + `ok: true` + "`" + ` — the runner recomputes the working-tree fingerprint at ` + "`" + `gm_dev_done` + "`" + ` time and requires it to match.
3. Call ` + "`" + `gm_dev_done` + "`" + ` with summary, commitMsg, prTitle, and prBody.
   - ` + "`" + `prBody` + "`" + ` must include ` + "`" + `Closes #{{.Issue.Number}}` + "`" + `.

> **Important:** Do **not** run ` + "`" + `git add` + "`" + `, ` + "`" + `git commit` + "`" + `, ` + "`" + `git push` + "`" + `, or ` + "`" + `golemic open-pr` + "`" + `. The runner handles all of that.
`

// RenderDevGateRetry renders a gate-retry dev user prompt explaining the gate
// rejection reason and instructing the agent to re-run gm_project_check then
// gm_dev_done. Returns an error if the guidelines file cannot be read or template
// execution fails.
func RenderDevGateRetry(gateReason string, issue Issue, branch, verifyCommand, guidelinesPath string) (string, error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := devGateRetryTemplateData{
		Issue:         issue,
		Branch:        branch,
		VerifyCommand: verifyCommand,
		GateReason:    gateReason,
		Guidelines:    guidelines,
		Directives:    workingDirDirective + "\n\n" + editOverWriteDirective + "\n\n" + noReReadDirective,
	}

	tmpl, err := template.New("devGateRetry").Parse(devGateRetryUserTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse dev gate retry prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("failed to render dev gate retry prompt template: %w", err)
	}

	return sb.String(), nil
}

// reviewerGateRetryTemplateData holds the template variables for the reviewer gate-retry prompt.
type reviewerGateRetryTemplateData struct {
	GateReason     string
	PRNumber       int
	Issue          Issue
	VerifyCommand  string
	Guidelines     string
	CodebaseMemory bool
	Directives     string
	PrecheckBlock  string
}

const reviewerGateRetryUserTemplate = `# Reviewer Gate Retry: Re-review PR #{{.PRNumber}} for Issue #{{.Issue.Number}}

**Issue Title:** {{.Issue.Title}}

**Previous approval rejected:** {{.GateReason}}

` + scaffoldFrame + `

{{.PrecheckBlock}}

Your previous ` + "`" + `gm_review_submit` + "`" + ` with ` + "`" + `verdict="approved"` + "`" + ` was rejected by the runner.
` + "`" + `approved` + "`" + ` is only valid when the precheck was ok, the tree was not mutated, and the tree has not changed since the precheck.

Please re-review and submit a valid verdict:

1. Re-read the precheck result above carefully.
2. If the precheck was not ok: you **must** submit ` + "`" + `changes_requested` + "`" + ` and explain the failures in the body.
3. If the precheck was ok and the tree is unchanged: you may submit ` + "`" + `approved` + "`" + `.
4. Call ` + "`" + `gm_review_submit` + "`" + ` with ` + "`" + `{ verdict, mergeConfidence, body }` + "`" + `. The body must justify your verdict.
   Do **not** call any other tools first unless you need more context from ` + "`" + `gm_pr_view` + "`" + ` or ` + "`" + `read` + "`" + `.
`

// RenderReviewerGateRetry renders a reviewer gate-retry prompt explaining why the approval
// was rejected and instructing the agent to re-review and submit a valid verdict.
func RenderReviewerGateRetry(gateReason string, prNumber int, issue Issue, verifyCommand, guidelinesPath, precheckBlock string, cbmEnabled bool) (string, error) {
	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := reviewerGateRetryTemplateData{
		GateReason:     gateReason,
		PRNumber:       prNumber,
		Issue:          issue,
		VerifyCommand:  verifyCommand,
		Guidelines:     guidelines,
		CodebaseMemory: cbmEnabled,
		Directives:     workingDirDirective,
		PrecheckBlock:  precheckBlock,
	}

	tmpl, err := template.New("reviewerGateRetry").Parse(reviewerGateRetryUserTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse reviewer gate retry prompt template: %w", err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("failed to render reviewer gate retry prompt template: %w", err)
	}

	return sb.String(), nil
}

// RenderDevRetry renders a dev retry user prompt injecting the verbatim reviewer findings
// and optional structured FindingsJSON from inline review comments.
//
// Returns EMPTY_FINDINGS error if findings is empty (BR-002, IF-001).
func RenderDevRetry(findings, findingsJSON string, issue Issue, branch string, verifyCommand string, guidelinesPath string, cbmEnabled bool) (userPrompt string, err error) {
	if findings == "" {
		return "", fmt.Errorf("EMPTY_FINDINGS: changes_requested review has an empty body")
	}

	guidelines, err := readGuidelines(guidelinesPath)
	if err != nil {
		return "", err
	}

	data := devRetryTemplateData{
		Issue:          issue,
		Branch:         branch,
		Findings:       findings,
		FindingsJSON:   findingsJSON,
		VerifyCommand:  verifyCommand,
		Guidelines:     guidelines,
		CodebaseMemory: cbmEnabled,
		Directives:     workingDirDirective + "\n\n" + editOverWriteDirective + "\n\n" + noReReadDirective,
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
	CodebaseMemory  bool
	Directives      string
}

const devCIRetryUserTemplate = `# CI Retry: Fix Failing Checks for Issue #{{.Issue.Number}}

**Title:** {{.Issue.Title}}

**Branch:** {{.Branch}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

---

## Failing CI Checks

The following CI checks failed on the PR. Fix the failures and push to the same branch:

{{.FailedCheckInfo}}

` + scaffoldFrame + `

1. The failing checks above are the primary input for this retry. If you need the original task specification, run ` + "`" + `gm_slice_get` + "`" + ` for issue ` + "`" + `{{.Issue.Number}}` + "`" + ` — its output is the authoritative spec; do not rely on any summary rendered in the issue's web UI.
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
		CodebaseMemory:  false,
		Directives:      workingDirDirective + "\n\n" + editOverWriteDirective + "\n\n" + noReReadDirective,
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
	CodebaseMemory  bool
	Directives      string
}

const devRebaseConflictResolveUserTemplate = `# Rebase Conflict Resolution: PR #{{.PRNumber}}

**Branch:** {{.Branch}}

**Base:** {{.Base}}

**Verification Command:** ` + "`" + `{{.VerifyCommand}}` + "`" + `

**Conflicted Files:**
{{range .ConflictedFiles}}- {{.}}
{{end}}
` + scaffoldFrame + `

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
		CodebaseMemory:  false,
		Directives:      workingDirDirective + "\n\n" + editOverWriteDirective + "\n\n" + noReReadDirective,
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
