package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func testEvent(t *testing.T, typ, runID, ts string, payload interface{}) Event {
	t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	if ts == "" {
		ts = time.Now().Format(time.RFC3339)
	}
	return Event{Type: typ, Ts: ts, RunID: runID, Payload: raw}
}

func testPayload(verdict string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"verdict": verdict})
	return b
}

// ---------------------------------------------------------------------------
// AC-001: Round-trip write/read
// ---------------------------------------------------------------------------

func TestAC001_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	payload := json.RawMessage(`{"key":"value"}`)
	ev := testEvent(t, EventRunStarted, "run-1", "2024-01-01T00:00:00Z", nil)
	ev.Payload = payload

	if err := w.Write(ev); err != nil {
		t.Fatal(err)
	}
	w.Close()

	var r Reader
	events, err := r.Read(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.Type != ev.Type {
		t.Errorf("Type: got %q, want %q", got.Type, ev.Type)
	}
	if got.Ts != ev.Ts {
		t.Errorf("Ts: got %q, want %q", got.Ts, ev.Ts)
	}
	if got.RunID != ev.RunID {
		t.Errorf("RunID: got %q, want %q", got.RunID, ev.RunID)
	}
	if string(got.Payload) != string(ev.Payload) {
		t.Errorf("Payload: got %s, want %s", string(got.Payload), string(ev.Payload))
	}
}

// ---------------------------------------------------------------------------
// AC-002: Malformed line → error, no events returned
// ---------------------------------------------------------------------------

func TestAC002_MalformedLineFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Write a valid line then an invalid one.
	content := `{"type":"run_started","ts":"2024-01-01T00:00:00Z","runId":"r1","payload":{}}
{broken json
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	var r Reader
	events, err := r.Read(path)
	if err == nil {
		t.Fatal("expected error for malformed line, got nil")
	}
	if events != nil {
		t.Fatalf("expected nil events on malformed line, got %d events", len(events))
	}
	// Error should mention the line number.
	if !contains(err.Error(), "line 2") {
		t.Errorf("error should mention line 2, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC-003 + AC-006: Concurrent appends lose no lines, maintain integrity
// ---------------------------------------------------------------------------

func TestAC003_AC006_ConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Two independent Writer instances sharing the same file — tests O_APPEND
	// atomicity across separate file descriptors, not just in-process mutex.
	w1, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w1.Close()

	w2, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()

	const eventsEach = 100

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for j := 0; j < eventsEach; j++ {
			ev := testEvent(t, EventDevStarted, "run-c", "", nil)
			if err := w1.Write(ev); err != nil {
				t.Errorf("w1 failed at event %d: %v", j, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for j := 0; j < eventsEach; j++ {
			ev := testEvent(t, EventDevStarted, "run-c", "", nil)
			if err := w2.Write(ev); err != nil {
				t.Errorf("w2 failed at event %d: %v", j, err)
				return
			}
		}
	}()
	wg.Wait()
	w1.Close()
	w2.Close()

	var r Reader
	events, err := r.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2*eventsEach {
		t.Fatalf("expected %d events, got %d", 2*eventsEach, len(events))
	}
	// All should parse.
	for i, ev := range events {
		if ev.Type != EventDevStarted {
			t.Errorf("event %d: unexpected type %q", i, ev.Type)
		}
		if ev.RunID != "run-c" {
			t.Errorf("event %d: unexpected runId %q", i, ev.RunID)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-004: Missing both env vars → named error
// ---------------------------------------------------------------------------

func TestAC004_MissingBothEnvVars(t *testing.T) {
	// Unset both vars.
	os.Unsetenv("GOLEMIC_RUN_ID")
	os.Unsetenv("GOLEMIC_EVENT_LOG")

	_, _, err := ResolveContext("/home/user", "myproject")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !contains(msg, "GOLEMIC_RUN_ID") || !contains(msg, "GOLEMIC_EVENT_LOG") {
		t.Errorf("error should list both missing vars, got: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// AC-005: LastEventOfType returns most recent match
// ---------------------------------------------------------------------------

func TestAC005_LastEventOfType(t *testing.T) {
	events := []Event{
		{Type: EventRunStarted, Ts: "t1"},
		{Type: EventDevStarted, Ts: "t2"},
		{Type: EventRunStarted, Ts: "t3"},
	}
	got, err := LastEventOfType(events, EventRunStarted)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ts != "t3" {
		t.Errorf("expected last event with ts=t3, got ts=%q", got.Ts)
	}

	// No match.
	_, err = LastEventOfType(events, EventReviewSubmitted)
	if err == nil {
		t.Fatal("expected error when type not found")
	}
}

// ---------------------------------------------------------------------------
// AC-007: GOLEMIC_EVENT_LOG set + GOLEMIC_RUN_ID set → path = env value
// ---------------------------------------------------------------------------

func TestAC007_EnvVarPathResolution(t *testing.T) {
	os.Setenv("GOLEMIC_RUN_ID", "run-007")
	os.Setenv("GOLEMIC_EVENT_LOG", "/custom/path/events.jsonl")
	defer os.Unsetenv("GOLEMIC_RUN_ID")
	defer os.Unsetenv("GOLEMIC_EVENT_LOG")

	runID, logPath, err := ResolveContext("/home/user", "myproject")
	if err != nil {
		t.Fatal(err)
	}
	if runID != "run-007" {
		t.Errorf("runID: got %q, want %q", runID, "run-007")
	}
	if logPath != "/custom/path/events.jsonl" {
		t.Errorf("logPath: got %q, want %q", logPath, "/custom/path/events.jsonl")
	}
}

// ---------------------------------------------------------------------------
// AC-008: Directory created on first write
// ---------------------------------------------------------------------------

func TestAC008_DirectoryCreatedOnFirstWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "events.jsonl")

	// Directory does not exist yet.
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("expected directory to not exist before write")
	}

	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ev := testEvent(t, EventRunStarted, "run-dir", "", nil)
	if err := w.Write(ev); err != nil {
		t.Fatal(err)
	}
	w.Close()

	// Directory and file should now exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should exist after write: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional tests: payload validation, LastEventOfType on empty, env var
// edge cases
// ---------------------------------------------------------------------------

func TestValidateReviewSubmitted_Valid(t *testing.T) {
	for _, v := range []string{"approved", "changes_requested"} {
		raw := testPayload(v)
		if err := ValidateReviewSubmittedPayload(raw); err != nil {
			t.Errorf("verdict %q should be valid: %v", v, err)
		}
	}
}

func TestValidateReviewSubmitted_Invalid(t *testing.T) {
	raw := testPayload("reject")
	if err := ValidateReviewSubmittedPayload(raw); err == nil {
		t.Error("expected error for invalid verdict, got nil")
	}
}

func TestValidateReviewSubmitted_EmptyPayload(t *testing.T) {
	if err := ValidateReviewSubmittedPayload(nil); err == nil {
		t.Error("expected error for nil payload, got nil")
	}
}

func TestWriteReviewSubmitted_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ev := testEvent(t, EventReviewSubmitted, "r1", "", map[string]string{"verdict": "approved"})
	if err := w.Write(ev); err != nil {
		t.Fatal(err)
	}
}

func TestWriteReviewSubmitted_Invalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	ev := testEvent(t, EventReviewSubmitted, "r1", "", map[string]string{"verdict": "reject"})
	if err := w.Write(ev); err == nil {
		t.Error("expected error for invalid verdict, got nil")
	}
}

func TestLastEventOfType_EmptySlice(t *testing.T) {
	_, err := LastEventOfType([]Event{}, EventRunStarted)
	if err == nil {
		t.Error("expected error on empty slice, got nil")
	}
}

func TestLastEventOfType_NilSlice(t *testing.T) {
	_, err := LastEventOfType(nil, EventRunStarted)
	if err == nil {
		t.Error("expected error on nil slice, got nil")
	}
}

func TestResolveContext_OnlyRunID(t *testing.T) {
	os.Setenv("GOLEMIC_RUN_ID", "run-only")
	os.Unsetenv("GOLEMIC_EVENT_LOG")
	defer os.Unsetenv("GOLEMIC_RUN_ID")

	runID, logPath, err := ResolveContext("/home/testuser", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if runID != "run-only" {
		t.Errorf("runID: got %q, want %q", runID, "run-only")
	}
	want := filepath.Join("/home/testuser", ".golemic", "proj", "runs", "run-only", "events.jsonl")
	if logPath != want {
		t.Errorf("logPath: got %q, want %q", logPath, want)
	}
}

func TestResolveContext_OnlyEventLog(t *testing.T) {
	os.Unsetenv("GOLEMIC_RUN_ID")
	os.Setenv("GOLEMIC_EVENT_LOG", "/some/path/events.jsonl")
	defer os.Unsetenv("GOLEMIC_EVENT_LOG")

	_, _, err := ResolveContext("/home/user", "proj")
	if err == nil {
		t.Fatal("expected error when only GOLEMIC_EVENT_LOG is set (RUN_ID missing)")
	}
	if !contains(err.Error(), "insufficient context") {
		t.Errorf("expected 'insufficient context' error, got: %v", err)
	}
}

func TestResolveContext_EmptyProject(t *testing.T) {
	os.Setenv("GOLEMIC_RUN_ID", "run-empty")
	os.Unsetenv("GOLEMIC_EVENT_LOG")
	defer os.Unsetenv("GOLEMIC_RUN_ID")

	_, _, err := ResolveContext("/home/user", "")
	if err == nil {
		t.Fatal("expected error when project is empty and no GOLEMIC_EVENT_LOG")
	}
}

func TestAllEventTypes(t *testing.T) {
	types := AllEventTypes()
	if len(types) != 8 {
		t.Errorf("expected 8 event types, got %d", len(types))
	}
}

func TestReaderTrailingEmptyLineAtEOF(t *testing.T) {
	// Trailing newline at EOF is normal JSONL — should not error.
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"type":"run_started","ts":"2024-01-01T00:00:00Z","runId":"r1"}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	var r Reader
	events, err := r.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event with trailing newline, got %d", len(events))
	}
}

func TestReaderMidFileBlankLine(t *testing.T) {
	// Blank line between events is malformed (fail-closed).
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := `{"type":"run_started","ts":"2024-01-01T00:00:00Z","runId":"r1"}

{"type":"dev_started","ts":"2024-01-01T00:00:01Z","runId":"r1"}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	var r Reader
	events, err := r.Read(path)
	if err == nil {
		t.Fatal("expected error for mid-file blank line")
	}
	if events != nil {
		t.Fatalf("expected nil events, got %d", len(events))
	}
	if !contains(err.Error(), "empty line") {
		t.Errorf("expected 'empty line' in error, got: %v", err)
	}
}

func TestReaderFileNotFound(t *testing.T) {
	var r Reader
	_, err := r.Read("/nonexistent/path/events.jsonl")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !contains(err.Error(), "LOG_FILE_NOT_FOUND") {
		t.Errorf("expected LOG_FILE_NOT_FOUND in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ci_wait_finished payload validation (AC-001 to AC-002 from issue-13)
// ---------------------------------------------------------------------------

func TestValidateCIWaitFinishedPayload_ValidResults(t *testing.T) {
	for _, result := range []string{"green", "red", "timeout", "no_checks"} {
		payload, _ := MarshalCIWaitFinishedPayload(result, 0)
		if err := ValidateCIWaitFinishedPayload(payload); err != nil {
			t.Errorf("result %q: unexpected error: %v", result, err)
		}
	}
}

func TestValidateCIWaitFinishedPayload_InvalidResult(t *testing.T) {
	payload := json.RawMessage(`{"result":"unknown","round":0}`)
	if err := ValidateCIWaitFinishedPayload(payload); err == nil {
		t.Error("expected error for invalid result, got nil")
	}
}

func TestValidateCIWaitFinishedPayload_Empty(t *testing.T) {
	if err := ValidateCIWaitFinishedPayload(nil); err == nil {
		t.Error("expected error for empty payload, got nil")
	}
}

func TestWriteCIWaitFinished_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalCIWaitFinishedPayload("green", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(Event{
		Type:    EventCIWaitFinished,
		Ts:      "2024-01-01T00:00:00Z",
		RunID:   "r1",
		Payload: payload,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Close() //nolint:errcheck

	var r Reader
	events, err := r.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != EventCIWaitFinished {
		t.Errorf("type: got %q, want %q", events[0].Type, EventCIWaitFinished)
	}
}

func TestWriteCIWaitFinished_RejectsInvalidResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() //nolint:errcheck

	payload := json.RawMessage(`{"result":"bad","round":0}`)
	if err := w.Write(Event{
		Type:    EventCIWaitFinished,
		Ts:      "2024-01-01T00:00:00Z",
		RunID:   "r1",
		Payload: payload,
	}); err == nil {
		t.Error("expected error for invalid ci_wait_finished result, got nil")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}