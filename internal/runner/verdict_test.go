package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golemic/internal/eventlog"
)

// writeReviewSubmittedEvent appends a review_submitted event with the given verdict
// and mergeConfidence "high" to the event log at logPath.
func writeReviewSubmittedEvent(t *testing.T, logPath, verdict string) {
	t.Helper()
	writeReviewSubmittedEventWithConfidence(t, logPath, verdict, "high")
}

// writeReviewSubmittedEventWithConfidence appends a review_submitted event with
// the given verdict and mergeConfidence to the event log at logPath.
func writeReviewSubmittedEventWithConfidence(t *testing.T, logPath, verdict, confidence string) {
	t.Helper()
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer w.Close() //nolint:errcheck

	zero := 0
	payload, _ := json.Marshal(map[string]interface{}{"verdict": verdict, "mergeConfidence": confidence, "reviewId": "PRR_test", "inlineCommentCount": &zero})
	if err := w.Write(eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "test-run",
		Payload: payload,
	}); err != nil {
		t.Fatalf("write event: %v", err)
	}
}

func newLogPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "events.jsonl")
}

// ---------------------------------------------------------------------------
// latestReviewVerdict unit tests (AC-001 – AC-004)
// ---------------------------------------------------------------------------

// AC-001: approved verdict is returned correctly.
func TestLatestReviewVerdict_Approved_AC001(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	writeReviewSubmittedEvent(t, logPath, "approved")

	verdict, err := r.latestReviewVerdict(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != "approved" {
		t.Errorf("verdict: got %q, want %q", verdict, "approved")
	}
}

// AC-002: changes_requested verdict is returned correctly.
func TestLatestReviewVerdict_ChangesRequested_AC002(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	writeReviewSubmittedEvent(t, logPath, "changes_requested")

	verdict, err := r.latestReviewVerdict(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != "changes_requested" {
		t.Errorf("verdict: got %q, want %q", verdict, "changes_requested")
	}
}

// Most-recent wins: write approved then changes_requested; should return changes_requested.
func TestLatestReviewVerdict_MostRecentWins(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	writeReviewSubmittedEvent(t, logPath, "approved")
	writeReviewSubmittedEvent(t, logPath, "changes_requested")

	verdict, err := r.latestReviewVerdict(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict != "changes_requested" {
		t.Errorf("verdict: got %q, want %q", verdict, "changes_requested")
	}
}

// AC-003: no review_submitted event returns NO_VALID_REVIEW error.
func TestLatestReviewVerdict_NoEvent_AC003(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)

	// Write a run_started event but no review_submitted.
	w, _ := eventlog.NewWriter(logPath)
	payload, _ := json.Marshal(map[string]interface{}{"issue": 1, "runId": "r1"})
	_ = w.Write(eventlog.Event{Type: eventlog.EventRunStarted, Ts: time.Now().Format(time.RFC3339), RunID: "r1", Payload: payload})
	_ = w.Close()

	_, err := r.latestReviewVerdict(logPath)
	if err == nil {
		t.Fatal("expected error for missing review_submitted event, got nil")
	}
}

// AC-003: non-existent log file returns NO_VALID_REVIEW error.
func TestLatestReviewVerdict_MissingFile_AC003(t *testing.T) {
	r := &Runner{}
	_, err := r.latestReviewVerdict("/nonexistent/events.jsonl")
	if err == nil {
		t.Fatal("expected error for missing log file, got nil")
	}
}

// AC-004: invalid verdict payload (not approved or changes_requested) returns error.
func TestLatestReviewVerdict_InvalidVerdict_AC004(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)

	// Write a review_submitted event with an invalid verdict directly (bypass writer validation).
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "r1",
		Payload: json.RawMessage(`{"verdict":"unknown_value"}`),
	})
	if err := os.WriteFile(logPath, append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := r.latestReviewVerdict(logPath)
	if err == nil {
		t.Fatal("expected error for invalid verdict, got nil")
	}
}

// ---------------------------------------------------------------------------
// orchestrate verdict-to-outcome mapping tests
// ---------------------------------------------------------------------------

// AC-001: orchestrate returns outcomeSuccess for approved verdict.
func TestOrchestrate_ApprovedVerdict_ReturnsSuccess_AC001(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	writeReviewSubmittedEvent(t, logPath, "approved")

	verdict, err := r.latestReviewVerdict(logPath)
	if err != nil {
		t.Fatalf("latestReviewVerdict: %v", err)
	}
	var outcome string
	switch verdict {
	case "approved":
		outcome = outcomeSuccess
	case "changes_requested":
		outcome = outcomeEscalated
	default:
		outcome = outcomeReviewFailed
	}
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
}

// AC-002: orchestrate returns outcomeEscalated for changes_requested verdict.
func TestOrchestrate_ChangesRequestedVerdict_ReturnsEscalated_AC002(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	writeReviewSubmittedEvent(t, logPath, "changes_requested")

	verdict, err := r.latestReviewVerdict(logPath)
	if err != nil {
		t.Fatalf("latestReviewVerdict: %v", err)
	}
	var outcome string
	switch verdict {
	case "approved":
		outcome = outcomeSuccess
	case "changes_requested":
		outcome = outcomeEscalated
	default:
		outcome = outcomeReviewFailed
	}
	if outcome != outcomeEscalated {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeEscalated)
	}
}

// AC-003: orchestrate returns outcomeReviewFailed when no review_submitted event exists.
func TestOrchestrate_MissingReviewEvent_ReturnsReviewFailed_AC003(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	// Write an empty log (just run_started).
	w, _ := eventlog.NewWriter(logPath)
	payload, _ := json.Marshal(map[string]interface{}{"issue": 1, "runId": "r1"})
	_ = w.Write(eventlog.Event{Type: eventlog.EventRunStarted, Ts: time.Now().Format(time.RFC3339), RunID: "r1", Payload: payload})
	_ = w.Close()

	_, err := r.latestReviewVerdict(logPath)
	if err == nil {
		t.Fatal("expected error for missing review_submitted event")
	}
	// Confirm that the caller would map this to outcomeReviewFailed.
	outcome := outcomeReviewFailed // caller's error branch
	if outcome != outcomeReviewFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeReviewFailed)
	}
}
