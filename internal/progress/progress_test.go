package progress

import (
	"encoding/json"
	"strings"
	"testing"

	"golemic/internal/eventlog"
	"golemic/internal/loop"
)

// TestFormatLifecycleLine_Coverage checks that every type in AllEventTypes()
// produces a non-empty line per BR-P4.
func TestFormatLifecycleLine_Coverage(t *testing.T) {
	for _, evType := range eventlog.AllEventTypes() {
		line := FormatLifecycleLine(eventlog.Event{Type: evType})
		if line == "" {
			t.Errorf("event type %q produced empty line", evType)
		}
	}
}

// TestFormatLifecycleLine_UnknownFallback verifies that unknown types produce
// "▶ <event_type>" without panicking per BR-P4.
func TestFormatLifecycleLine_UnknownFallback(t *testing.T) {
	line := FormatLifecycleLine(eventlog.Event{Type: "future_unknown_event"})
	if line != "▶ future_unknown_event" {
		t.Errorf("got %q, want %q", line, "▶ future_unknown_event")
	}
}

func TestFormatLifecycleLine_RunStarted(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{"issue": 42, "runId": "issue-42-abc"})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventRunStarted, Payload: payload})
	if line != "▶ run started (issue #42, runId issue-42-abc)" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatLifecycleLine_WorktreeCreated(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"role": "dev"})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventWorktreeCreated, Payload: payload})
	if line != "▶ worktree ready (dev)" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatLifecycleLine_AgentCompleted(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{"role": "dev", "exitCode": 0})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventAgentCompleted, Payload: payload})
	if line != "▶ dev completed (exit 0)" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatLifecycleLine_PROpened(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"prNumber": "99"})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventPROpened, Payload: payload})
	if line != "▶ PR #99 opened" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatLifecycleLine_ReviewSubmitted(t *testing.T) {
	zero := 0
	payload, _ := json.Marshal(map[string]interface{}{
		"verdict": "approved", "confidence": "high",
		"reviewId": "rev1", "inlineCommentCount": &zero,
	})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventReviewSubmitted, Payload: payload})
	if line != "▶ review: approved" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatLifecycleLine_CIWaitFinished(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{"result": "green", "round": 1})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventCIWaitFinished, Payload: payload})
	if line != "▶ CI green" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatLifecycleLine_PRMerged(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{"prNumber": 99, "mergedSHA": "abc"})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventPRMerged, Payload: payload})
	if line != "▶ PR #99 merged" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatLifecycleLine_RunFinished(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"outcome": "success"})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventRunFinished, Payload: payload})
	if line != "▶ run finished (success)" {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestFormatStepTransitionLine_Normal(t *testing.T) {
	line := FormatStepTransitionLine(string(loop.StepPrepare), string(loop.EventReady), string(loop.StepRunDev), false)
	if line != "▶ running dev agent" {
		t.Errorf("got %q, want %q", line, "▶ running dev agent")
	}
}

func TestFormatStepTransitionLine_GuardedSelfLoop(t *testing.T) {
	line := FormatStepTransitionLine(string(loop.StepRunDev), string(loop.EventDevGateRejected), string(loop.StepRunDev), true)
	if line != "↻ running dev agent (gate rejected)" {
		t.Errorf("got %q, want %q", line, "↻ running dev agent (gate rejected)")
	}
}

func TestFormatStepTransitionLine_TerminalSuccess(t *testing.T) {
	line := FormatStepTransitionLine(string(loop.StepMergePR), string(loop.EventMerged), string(loop.StepTerminalSuccess), false)
	if line != "✔ success" {
		t.Errorf("got %q, want %q", line, "✔ success")
	}
}

func TestFormatStepTransitionLine_TerminalFailure(t *testing.T) {
	line := FormatStepTransitionLine(string(loop.StepRunDev), string(loop.EventDevFailed), string(loop.StepTerminalDevFailed), false)
	if line != "✖ dev failed" {
		t.Errorf("got %q, want %q", line, "✖ dev failed")
	}
}

func TestFormatStepTransitionLine_UnknownKeyFallback(t *testing.T) {
	line := FormatStepTransitionLine("FROM", "EVENT", "FUTURE_STEP", false)
	if line != "▶ FUTURE_STEP" {
		t.Errorf("got %q, want %q", line, "▶ FUTURE_STEP")
	}
}

func TestFormatLifecycleLine_StepTransitionWithPayload(t *testing.T) {
	payload, _ := json.Marshal(map[string]interface{}{
		"from": string(loop.StepPrepare), "event": string(loop.EventReady),
		"to": string(loop.StepRunDev), "guarded": false, "seq": 1,
	})
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventStepTransition, Payload: payload})
	if line != "▶ running dev agent" {
		t.Errorf("got %q, want %q", line, "▶ running dev agent")
	}
}

func TestFormatLifecycleLine_StepTransitionEmptyPayloadFallback(t *testing.T) {
	line := FormatLifecycleLine(eventlog.Event{Type: eventlog.EventStepTransition})
	if line != "▶ step_transition" {
		t.Errorf("got %q, want %q", line, "▶ step_transition")
	}
}

// TestFormatToolCallLine verifies bash, read, edit, write, and unknown tools.
func TestFormatToolCallLine(t *testing.T) {
	tests := []struct {
		role    string
		tool    string
		args    string
		wantPfx string
	}{
		{"dev", "bash", `{"command":"go test ./..."}`, "dev · bash: go test ./..."},
		{"dev", "read", `{"path":"internal/foo.go"}`, "dev · read: internal/foo.go"},
		{"dev", "edit", `{"path":"internal/bar.go","edits":[]}`, "dev · edit: internal/bar.go"},
		{"dev", "write", `{"path":"out.txt","content":"x"}`, "dev · write: out.txt"},
		{"reviewer", "unknown_tool", `{"key":"val"}`, "reviewer · unknown_tool: "},
	}
	for _, tc := range tests {
		line := FormatToolCallLine(tc.role, tc.tool, json.RawMessage(tc.args))
		if !strings.HasPrefix(line, tc.wantPfx) {
			t.Errorf("tool=%q: got %q, want prefix %q", tc.tool, line, tc.wantPfx)
		}
	}
}

// TestArgsPreviewTruncation checks that long args are capped at maxArgsPreview per BR-P5.
func TestArgsPreviewTruncation(t *testing.T) {
	long := strings.Repeat("x", maxArgsPreview+50)
	args := json.RawMessage(`{"command":` + `"` + long + `"` + `}`)
	line := FormatToolCallLine("dev", "bash", args)
	// preview part after "dev · bash: " should end with ellipsis
	preview := strings.TrimPrefix(line, "dev · bash: ")
	if !strings.HasSuffix(preview, "…") {
		t.Errorf("expected truncated preview to end with ellipsis, got: %q", preview)
	}
	if len(preview) > maxArgsPreview+5 {
		t.Errorf("preview too long: %d chars", len(preview))
	}
}

// TestArgsPreviewNewlines checks that newlines in args are replaced with spaces per BR-P5.
func TestArgsPreviewNewlines(t *testing.T) {
	args := json.RawMessage(`{"command":"line1\nline2\nline3"}`)
	line := FormatToolCallLine("dev", "bash", args)
	if strings.Contains(line, "\n") {
		t.Errorf("tool-call line must not contain newlines, got: %q", line)
	}
}

// ---------------------------------------------------------------------------
// FormatAgentContext tests
// ---------------------------------------------------------------------------

func buildPrompt(n int) string {
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, strings.Repeat("line ", 1)+string(rune('A'+i%26)))
	}
	return strings.Join(lines, "\n")
}

func TestFormatAgentContext_HeaderContainsRoleModelRoundAttempt(t *testing.T) {
	p := AgentContextParams{
		Role: "dev", Model: "claude-opus-4-7", Round: 1, Attempt: 0,
		UserPrompt: "hello", PromptFile: "/tmp/dev-r1-a0.prompt.md",
	}
	out := FormatAgentContext(p)
	for _, want := range []string{"dev", "claude-opus-4-7", "r1/a0"} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatAgentContext_CompactShowsPreviewAndTruncation(t *testing.T) {
	prompt := buildPrompt(agentContextPreviewLines + 5)
	p := AgentContextParams{
		Role: "dev", Model: "m", Round: 1, Attempt: 0,
		UserPrompt: prompt, PromptFile: "/runs/dev-r1-a0.prompt.md",
		Verbose: false,
	}
	out := FormatAgentContext(p)
	lines := strings.Split(out, "\n")
	// Count body lines (those starting with │) that are not the path or truncation marker.
	promptLines := 0
	truncLine := false
	hasPath := false
	for _, l := range lines {
		if strings.HasPrefix(l, "│ ") && !strings.Contains(l, "more lines omitted") && !strings.Contains(l, "→") {
			promptLines++
		}
		if strings.Contains(l, "more lines omitted") {
			truncLine = true
		}
		if strings.Contains(l, "/runs/dev-r1-a0.prompt.md") {
			hasPath = true
		}
	}
	if promptLines != agentContextPreviewLines {
		t.Errorf("compact: expected %d prompt lines, got %d", agentContextPreviewLines, promptLines)
	}
	if !truncLine {
		t.Errorf("compact: missing truncation marker")
	}
	if !hasPath {
		t.Errorf("compact: missing persisted file path")
	}
}

func TestFormatAgentContext_CompactShortPromptNoTruncation(t *testing.T) {
	prompt := buildPrompt(agentContextPreviewLines - 2)
	p := AgentContextParams{
		Role: "dev", Model: "m", Round: 1, Attempt: 0,
		UserPrompt: prompt, PromptFile: "/runs/dev-r1-a0.prompt.md",
		Verbose: false,
	}
	out := FormatAgentContext(p)
	if strings.Contains(out, "more lines omitted") {
		t.Errorf("compact with short prompt must not show truncation marker")
	}
	if !strings.Contains(out, "/runs/dev-r1-a0.prompt.md") {
		t.Errorf("compact: missing persisted file path")
	}
}

func TestFormatAgentContext_VerboseShowsFullPrompt(t *testing.T) {
	prompt := buildPrompt(agentContextPreviewLines + 10)
	p := AgentContextParams{
		Role: "reviewer", Model: "m", Round: 2, Attempt: 1,
		UserPrompt: prompt, PromptFile: "/runs/reviewer-r2-a1.prompt.md",
		Verbose: true,
	}
	out := FormatAgentContext(p)
	if strings.Contains(out, "more lines omitted") {
		t.Errorf("verbose: must not show truncation marker")
	}
	promptLines := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "│ ") && !strings.Contains(l, "→") {
			promptLines++
		}
	}
	want := agentContextPreviewLines + 10
	if promptLines != want {
		t.Errorf("verbose: expected %d prompt lines, got %d", want, promptLines)
	}
	if !strings.Contains(out, "/runs/reviewer-r2-a1.prompt.md") {
		t.Errorf("verbose: missing persisted file path")
	}
}
