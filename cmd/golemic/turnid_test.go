package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/eventlog"
)

// writeEventToLog writes an event directly to the log file for test setup.
func writeEventToLog(t *testing.T, logPath string, ev eventlog.Event) {
	t.Helper()
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("writeEventToLog: open writer: %v", err)
	}
	defer w.Close() //nolint:errcheck
	if err := w.Write(ev); err != nil {
		t.Fatalf("writeEventToLog: write: %v", err)
	}
}

// --- AC-001: Double submit-review in one reviewer turn produces exactly one review ---

func TestSubmitReview_DuplicateTurnIsNoOp_AC001(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-dedup-1",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "5",
	}

	// Pre-populate log: first submit-review already written for turnId 5.
	payload, _ := json.Marshal(map[string]interface{}{
		"verdict": "changes_requested", "body": "fix it", "prNumber": 10, "mergeConfidence": "low",
	})
	writeEventToLog(t, logPath, eventlog.Event{
		Type: eventlog.EventReviewSubmitted, Ts: time.Now().Format(time.RFC3339),
		RunID: "run-dedup-1", TurnID: 5, Payload: payload,
	})

	ghCalled := 0
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "review" {
				ghCalled++
			}
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "changes_requested", "--body", "fix it", "--pr", "10", "--merge-confidence", "low"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0 (no-op); stderr: %s", got, stderr.String())
	}
	if ghCalled != 0 {
		t.Errorf("gh should not be called on duplicate; called %d time(s)", ghCalled)
	}
	if !strings.Contains(stdout.String(), "already submitted for this turn") {
		t.Errorf("stdout should mention already submitted, got: %q", stdout.String())
	}

	// Verify still exactly one review_submitted event.
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == eventlog.EventReviewSubmitted && ev.TurnID == 5 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 review_submitted for turnId 5, got %d", count)
	}
}

// --- AC-002: Legitimate multi-round ping-pong still submits one review per round ---

func TestSubmitReview_DistinctTurnIDsAllowBothReviews_AC002(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	// Round 1: turnId 2 already emitted review_submitted.
	payload1, _ := json.Marshal(map[string]interface{}{
		"verdict": "changes_requested", "body": "round1", "prNumber": 20, "mergeConfidence": "low",
	})
	writeEventToLog(t, logPath, eventlog.Event{
		Type: eventlog.EventReviewSubmitted, Ts: time.Now().Format(time.RFC3339),
		RunID: "run-multi-1", TurnID: 2, Payload: payload1,
	})

	// Round 2: turnId 4 — should be allowed.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-multi-1",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "4",
	}

	ghCalled := 0
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "review" {
				ghCalled++
				return "ok", nil
			}
			if name == "gh" && args[0] == "label" {
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "edit" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "changes_requested", "--body", "round2", "--pr", "20", "--merge-confidence", "low"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if ghCalled != 1 {
		t.Errorf("gh should be called once for distinct turnId; called %d time(s)", ghCalled)
	}

	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count4 := 0
	for _, ev := range events {
		if ev.Type == eventlog.EventReviewSubmitted && ev.TurnID == 4 {
			count4++
		}
	}
	if count4 != 1 {
		t.Errorf("expected 1 review_submitted for turnId 4, got %d", count4)
	}
}

// --- AC-003: Agent CLI command without GOLEMIC_TURN_ID fails closed ---

func TestSubmitReview_MissingTurnID_FailsClosed_AC003(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-no-turn",
		"GOLEMIC_EVENT_LOG": logPath,
		// GOLEMIC_TURN_ID intentionally absent
	}

	var called bool
	exec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "1", "--merge-confidence", "high"}
	got := runSubmitReview(args, &stdout, &stderr, func(k string) string { return env[k] }, exec)

	if got == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "Missing required environment variable: GOLEMIC_TURN_ID") {
		t.Errorf("stderr should mention GOLEMIC_TURN_ID, got: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called when GOLEMIC_TURN_ID is missing")
	}
}

func TestEmit_MissingTurnID_FailsClosed_AC003(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-no-turn",
		"GOLEMIC_EVENT_LOG": logPath,
		// GOLEMIC_TURN_ID intentionally absent
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "Missing required environment variable: GOLEMIC_TURN_ID") {
		t.Errorf("stderr should mention GOLEMIC_TURN_ID, got: %q", stderr.String())
	}
}

func TestOpenPR_MissingTurnID_FailsClosed_AC003(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-no-turn",
		"GOLEMIC_EVENT_LOG": logPath,
		// GOLEMIC_TURN_ID intentionally absent
	}

	var called bool
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			return "feature/branch", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			called = true
			return "", fmt.Errorf("should not be called")
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "T", "--body", "B"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec, dir)

	if got == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "Missing required environment variable: GOLEMIC_TURN_ID") {
		t.Errorf("stderr should mention GOLEMIC_TURN_ID, got: %q", stderr.String())
	}
	if called {
		t.Error("gh should not be called when GOLEMIC_TURN_ID is missing")
	}
}

// --- AC-004: Duplicate emit of the same type in one turn is a no-op ---

func TestEmit_DuplicateTurnTypeIsNoOp_AC004(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	// Pre-populate: event of type "dev_started" with turnId 3 already exists.
	writeEventToLog(t, logPath, eventlog.Event{
		Type: "dev_started", Ts: time.Now().Format(time.RFC3339),
		RunID: "run-emit-dedup", TurnID: 3, Payload: json.RawMessage("{}"),
	})

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-dedup",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "3",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0 (no-op); stderr: %s", got, stderr.String())
	}

	// Still exactly one dev_started event with turnId 3.
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == "dev_started" && ev.TurnID == 3 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 dev_started event for turnId 3, got %d", count)
	}
}

// --- AC-005: Duplicate open-pr in one turn opens exactly one PR ---

func TestOpenPR_DuplicateTurnIsNoOp_AC005(t *testing.T) { //nolint:cyclop
	dir := t.TempDir()
	makeTestConfig(t, dir)
	logPath := filepath.Join(dir, "events.jsonl")

	// Pre-populate: pr_opened with turnId 1 already written.
	prPayload, _ := json.Marshal(map[string]string{"prNumber": "77", "url": "https://github.com/o/r/pull/77", "branch": "golemic/issue-42"})
	writeEventToLog(t, logPath, eventlog.Event{
		Type: eventlog.EventPROpened, Ts: time.Now().Format(time.RFC3339),
		RunID: "run-pr-dedup", TurnID: 1, Payload: prPayload,
	})

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-pr-dedup",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	ghCalled := 0
	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "sh" {
				return "", nil
			}
			return "golemic/issue-42\n", nil
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
				ghCalled++
				return "https://github.com/o/r/pull/99\n", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "open-pr", "--title", "T", "--body", "B"}
	got := runOpenPR(args, &stdout, &stderr, func(k string) string { return env[k] }, exec, dir)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0 (no-op); stderr: %s", got, stderr.String())
	}
	if ghCalled != 0 {
		t.Errorf("gh pr create should not be called on duplicate; called %d time(s)", ghCalled)
	}

	// Still exactly one pr_opened event with turnId 1.
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == eventlog.EventPROpened && ev.TurnID == 1 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 pr_opened for turnId 1, got %d", count)
	}
}

// --- AC-007: gh failure on first submit leaves the turn retryable ---

func TestSubmitReview_GhFailureThenSuccessInSameTurn_AC007(t *testing.T) { //nolint:cyclop,gocognit
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-retry-7",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "3",
	}

	// First invocation: gh fails.
	failExec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "review" {
				return "", fmt.Errorf("network error")
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout1, stderr1 bytes.Buffer
	args := []string{"golemic", "submit-review", "--verdict", "approved", "--body", "LGTM", "--pr", "5", "--merge-confidence", "high"}
	got1 := runSubmitReview(args, &stdout1, &stderr1, func(k string) string { return env[k] }, failExec)
	if got1 == 0 {
		t.Fatalf("first invocation should fail; exit code: %d", got1)
	}

	// No event should have been written.
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == eventlog.EventReviewSubmitted {
			t.Errorf("no review_submitted should exist after gh failure, got: %+v", ev)
		}
	}

	// Second invocation: gh succeeds — should NOT be treated as duplicate.
	ghCalled := 0
	successExec := fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "review" {
				ghCalled++
				return "ok", nil
			}
			if name == "gh" && args[0] == "label" {
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "edit" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout2, stderr2 bytes.Buffer
	got2 := runSubmitReview(args, &stdout2, &stderr2, func(k string) string { return env[k] }, successExec)
	if got2 != 0 {
		t.Fatalf("second invocation should succeed; exit code: %d, stderr: %s", got2, stderr2.String())
	}
	if ghCalled != 1 {
		t.Errorf("gh should be called once on the successful retry; called %d time(s)", ghCalled)
	}

	// Exactly one review_submitted event for turnId 3.
	events2, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events2 {
		if ev.Type == eventlog.EventReviewSubmitted && ev.TurnID == 3 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 review_submitted for turnId 3, got %d", count)
	}
}

// --- eventlog.Event round-trips the turnId field ---

func TestEventTurnIDRoundTrips(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() //nolint:errcheck

	if err := w.Write(eventlog.Event{
		Type:    "test_event",
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "run-roundtrip",
		TurnID:  42,
		Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].TurnID != 42 {
		t.Errorf("TurnID round-trip: got %d, want 42", events[0].TurnID)
	}
}
