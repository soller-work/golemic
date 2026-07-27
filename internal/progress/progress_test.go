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
