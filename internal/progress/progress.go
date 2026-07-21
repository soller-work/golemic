// Package progress renders live-progress lines from golemic run onto stderr.
//
// Two line families are rendered:
//  1. Lifecycle lines — one per eventlog.Event written to events.jsonl.
//  2. Tool-call lines — one per tool_execution_start entry in role.activity.jsonl.
//
// All rendering is non-fatal per BR-P3: errors do not propagate to the caller.
package progress

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"golemic/internal/eventlog"
)

const maxArgsPreview = 120

// Renderer writes lifecycle and tool-call progress lines to an io.Writer.
// All methods are safe for concurrent use.
type Renderer struct {
	mu  sync.Mutex
	out io.Writer
}

// New creates a Renderer that writes to out.
func New(out io.Writer) *Renderer {
	return &Renderer{out: out}
}

// EmitLifecycle writes a formatted lifecycle line for the given event.
func (r *Renderer) EmitLifecycle(event eventlog.Event) {
	line := FormatLifecycleLine(event)
	r.mu.Lock()
	fmt.Fprintln(r.out, line) //nolint:errcheck
	r.mu.Unlock()
}

// EmitToolCall writes a formatted tool-call line for an agent tool execution.
func (r *Renderer) EmitToolCall(role, toolName string, args json.RawMessage) {
	line := FormatToolCallLine(role, toolName, args)
	r.mu.Lock()
	fmt.Fprintln(r.out, line) //nolint:errcheck
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Lifecycle formatters
// ---------------------------------------------------------------------------

// lifecycleFormatters maps event types to their line-formatter functions.
var lifecycleFormatters = map[string]func(json.RawMessage) string{
	eventlog.EventRunStarted:             fmtRunStarted,
	eventlog.EventWorktreeCreated:        fmtWorktreeCreated,
	eventlog.EventDevStarted:             func(json.RawMessage) string { return "▶ dev started" },
	eventlog.EventAgentCompleted:         fmtAgentCompleted,
	eventlog.EventPROpened:               fmtPROpened,
	eventlog.EventReviewSubmitted:        fmtReviewSubmitted,
	eventlog.EventCIWaitFinished:         fmtCIWaitFinished,
	eventlog.EventPRMerged:               fmtPRMerged,
	eventlog.EventAutomergeSkipped:       fmtAutomergeSkipped,
	eventlog.EventAutomergeFailed:        fmtAutomergeFailed,
	eventlog.EventAutomergeConflictRetry: fmtConflictRetry,
	eventlog.EventIssueClaimed:           fmtIssueClaimed,
	eventlog.EventIssueReleased:          fmtIssueReleased,
	eventlog.EventRunFinished:            fmtRunFinished,
}

// FormatLifecycleLine returns a deterministic one-line string for event.
// Unknown event types fall back to "▶ <event_type>" per BR-P4.
func FormatLifecycleLine(event eventlog.Event) string {
	if fn, ok := lifecycleFormatters[event.Type]; ok {
		return fn(event.Payload)
	}
	return "▶ " + event.Type
}

func fmtRunStarted(p json.RawMessage) string {
	var v struct {
		Issue int    `json:"issue"`
		RunID string `json:"runId"`
	}
	if json.Unmarshal(p, &v) == nil && v.Issue > 0 {
		return fmt.Sprintf("▶ run started (issue #%d, runId %s)", v.Issue, v.RunID)
	}
	return "▶ run started"
}

func fmtWorktreeCreated(p json.RawMessage) string {
	var v struct{ Role string `json:"role"` }
	if json.Unmarshal(p, &v) == nil && v.Role != "" {
		return fmt.Sprintf("▶ worktree ready (%s)", v.Role)
	}
	return "▶ worktree ready"
}

func fmtAgentCompleted(p json.RawMessage) string {
	var v struct {
		Role     string `json:"role"`
		ExitCode int    `json:"exitCode"`
	}
	if json.Unmarshal(p, &v) == nil && v.Role != "" {
		return fmt.Sprintf("▶ %s completed (exit %d)", v.Role, v.ExitCode)
	}
	return "▶ agent completed"
}

func fmtPROpened(p json.RawMessage) string {
	var v struct{ PRNumber string `json:"prNumber"` }
	if json.Unmarshal(p, &v) == nil && v.PRNumber != "" {
		return fmt.Sprintf("▶ PR #%s opened", v.PRNumber)
	}
	return "▶ PR opened"
}

func fmtReviewSubmitted(p json.RawMessage) string {
	var v struct{ Verdict string `json:"verdict"` }
	if json.Unmarshal(p, &v) == nil && v.Verdict != "" {
		return fmt.Sprintf("▶ review: %s", v.Verdict)
	}
	return "▶ review submitted"
}

func fmtCIWaitFinished(p json.RawMessage) string {
	var v struct{ Result string `json:"result"` }
	if json.Unmarshal(p, &v) == nil && v.Result != "" {
		return fmt.Sprintf("▶ CI %s", v.Result)
	}
	return "▶ CI finished"
}

func fmtPRMerged(p json.RawMessage) string {
	var v struct{ PRNumber interface{} `json:"prNumber"` }
	if json.Unmarshal(p, &v) == nil && v.PRNumber != nil {
		return fmt.Sprintf("▶ PR #%v merged", v.PRNumber)
	}
	return "▶ PR merged"
}

func fmtAutomergeSkipped(p json.RawMessage) string {
	var v struct{ Reason string `json:"reason"` }
	if json.Unmarshal(p, &v) == nil && v.Reason != "" {
		return fmt.Sprintf("▶ automerge skipped (%s)", v.Reason)
	}
	return "▶ automerge skipped"
}

func fmtAutomergeFailed(p json.RawMessage) string {
	var v struct{ Reason string `json:"reason"` }
	if json.Unmarshal(p, &v) == nil && v.Reason != "" {
		return fmt.Sprintf("▶ automerge failed (%s)", v.Reason)
	}
	return "▶ automerge failed"
}

func fmtConflictRetry(p json.RawMessage) string {
	var v struct{ Result string `json:"result"` }
	if json.Unmarshal(p, &v) == nil && v.Result != "" {
		return fmt.Sprintf("▶ conflict retry (%s)", v.Result)
	}
	return "▶ conflict retry"
}

func fmtIssueClaimed(p json.RawMessage) string {
	var v struct{ IssueNumber int `json:"issue_number"` }
	if json.Unmarshal(p, &v) == nil && v.IssueNumber > 0 {
		return fmt.Sprintf("▶ issue #%d claimed", v.IssueNumber)
	}
	return "▶ issue claimed"
}

func fmtIssueReleased(p json.RawMessage) string {
	var v struct {
		IssueNumber int    `json:"issue_number"`
		Reason      string `json:"reason"`
	}
	if json.Unmarshal(p, &v) == nil && v.IssueNumber > 0 {
		return fmt.Sprintf("▶ issue #%d released (%s)", v.IssueNumber, v.Reason)
	}
	return "▶ issue released"
}

func fmtRunFinished(p json.RawMessage) string {
	var v struct{ Outcome string `json:"outcome"` }
	if json.Unmarshal(p, &v) == nil && v.Outcome != "" {
		return fmt.Sprintf("▶ run finished (%s)", v.Outcome)
	}
	return "▶ run finished"
}

// ---------------------------------------------------------------------------
// Tool-call formatter
// ---------------------------------------------------------------------------

// FormatToolCallLine returns a one-line string for a tool call per BR-P5.
// The args preview is capped at maxArgsPreview characters, with newlines normalized.
func FormatToolCallLine(role, toolName string, argsRaw json.RawMessage) string {
	preview := argsPreview(toolName, argsRaw)
	return fmt.Sprintf("%s · %s: %s", role, toolName, preview)
}

func argsPreview(toolName string, argsRaw json.RawMessage) string {
	var args map[string]json.RawMessage
	if json.Unmarshal(argsRaw, &args) != nil {
		return trimPreview(string(argsRaw))
	}
	switch toolName {
	case "bash":
		return trimPreview(extractString(args["command"]))
	case "read", "write", "edit":
		return trimPreview(extractString(args["path"]))
	default:
		b, _ := json.Marshal(args)
		return trimPreview(string(b))
	}
}

func extractString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return string(raw)
	}
	return s
}

// trimPreview normalizes newlines and trims to maxArgsPreview per BR-P5.
func trimPreview(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > maxArgsPreview {
		return s[:maxArgsPreview] + "…"
	}
	return s
}
