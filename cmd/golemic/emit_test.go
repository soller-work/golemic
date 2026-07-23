package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/eventlog"
)

func TestRunEmit_Success(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-1",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}

	// Verify exactly one event was written.
	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "dev_started" {
		t.Errorf("Type: got %q, want %q", ev.Type, "dev_started")
	}
	if ev.RunID != "run-emit-1" {
		t.Errorf("RunID: got %q, want %q", ev.RunID, "run-emit-1")
	}
	// Verify ts is a valid RFC3339 timestamp.
	if _, err := time.Parse(time.RFC3339, ev.Ts); err != nil {
		t.Errorf("Ts is not valid RFC3339: %q (err: %v)", ev.Ts, err)
	}
	// Verify payload is the normalized empty object.
	if string(ev.Payload) != "{}" {
		t.Errorf("Payload: got %s, want %s", string(ev.Payload), "{}")
	}
}

func TestRunEmit_EmptyType(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-2",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "--type must not be empty") {
		t.Errorf("stderr missing error message, got: %q", stderr.String())
	}

	// No event should be written.
	var r eventlog.Reader
	_, err := r.Read(logPath)
	if err == nil {
		t.Error("expected error reading log (no events should exist)")
	}
}

func TestRunEmit_InvalidPayloadJSON(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-3",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "not-json"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Invalid --payload:") {
		t.Errorf("stderr missing 'Invalid --payload:', got: %q", stderr.String())
	}

	// No event should be written.
	var r eventlog.Reader
	_, err := r.Read(logPath)
	if err == nil {
		t.Error("expected error reading log (no events should exist)")
	}
}

func TestRunEmit_PayloadNotObject(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-4",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	tests := []struct {
		payload string
		desc    string
	}{
		{`[1,2,3]`, "array"},
		{`"string"`, "string"},
		{`42`, "number"},
		{`null`, "null"},
		{`true`, "boolean"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := []string{"golemic", "emit", "--type", "dev_started", "--payload", tt.payload}
			got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

			if got != 1 {
				t.Fatalf("exit code: got %d, want 1", got)
			}
			if !strings.Contains(stderr.String(), "Invalid --payload:") {
				t.Errorf("stderr missing 'Invalid --payload:', got: %q", stderr.String())
			}
		})
	}
}

func TestRunEmit_MissingRunID(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_RUN_ID") {
		t.Errorf("stderr missing GOLEMIC_RUN_ID, got: %q", stderr.String())
	}
	// AC-003: no event written when env var check fails before any I/O.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when env var check fails before any I/O")
	}
}

func TestRunEmit_MissingEventLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-5",
		"GOLEMIC_EVENT_LOG": "",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr missing GOLEMIC_EVENT_LOG, got: %q", stderr.String())
	}
	// AC-003: no event written when env var check fails before any I/O.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when env var check fails before any I/O")
	}
}

func TestRunEmit_MissingBothEnvVars(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": "",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "GOLEMIC_RUN_ID") || !strings.Contains(msg, "GOLEMIC_EVENT_LOG") {
		t.Errorf("stderr should list both missing vars, got: %q", msg)
	}
	// AC-003: no event written when env var check fails before any I/O.
	if _, err := os.Stat(logPath); err == nil {
		t.Error("log file should not exist when env var check fails before any I/O")
	}
}

func TestRunEmit_WriteFailure(t *testing.T) {
	// Use a directory path (not a regular file) so NewWriter's OpenFile fails
	// with "is a directory" on all platforms.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-6",
		"GOLEMIC_EVENT_LOG": t.TempDir(),
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if !strings.Contains(stderr.String(), "Failed to write event:") {
		t.Errorf("stderr missing 'Failed to write event:', got: %q", stderr.String())
	}
}

func TestRunEmit_NormalizesPayload(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-7",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	// Payload with extra whitespace and different key spacing.
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", `{  "key" :  "value"  }`}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	// Re-encoded payload should be compact JSON.
	if string(events[0].Payload) != `{"key":"value"}` {
		t.Errorf("Payload not normalized: got %s, want %s", string(events[0].Payload), `{"key":"value"}`)
	}
}

func TestRunEmit_UnknownEventTypeAllowed(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-8",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "my_custom_progress", "--payload", `{"step":3}`}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "my_custom_progress" {
		t.Errorf("Type: got %q, want %q", events[0].Type, "my_custom_progress")
	}
}

func TestRunEmit_WithPayload(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-9",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "run_finished", "--payload", `{"outcome":"success"}`}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != "run_finished" {
		t.Errorf("Type: got %q, want %q", ev.Type, "run_finished")
	}
	if ev.RunID != "run-emit-9" {
		t.Errorf("RunID: got %q, want %q", ev.RunID, "run-emit-9")
	}
	if string(ev.Payload) != `{"outcome":"success"}` {
		t.Errorf("Payload: got %s, want %s", string(ev.Payload), `{"outcome":"success"}`)
	}
}

func TestRunEmit_ArbitraryOrderFlags(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-10",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	// --payload before --type
	args := []string{"golemic", "emit", "--payload", `{"x":1}`, "--type", "test_type"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "test_type" {
		t.Errorf("Type: got %q, want %q", events[0].Type, "test_type")
	}
}

func TestRunEmit_EnvVarsCheckedBeforePayloadParse(t *testing.T) {
	// Missing env var should fail before payload validation.
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": "",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "not-json"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	// Error should mention env vars, not payload.
	msg := stderr.String()
	if !strings.Contains(msg, "Missing required environment variable") {
		t.Errorf("stderr should mention env vars first, got: %q", msg)
	}
}

func TestRunEmit_EmptyTypeCheckedBeforePayloadParse(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	env := map[string]string{
		"GOLEMIC_RUN_ID":    "run-emit-11",
		"GOLEMIC_EVENT_LOG": logPath,
		"GOLEMIC_TURN_ID":   "1",
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "", "--payload", "not-json"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	// Error should mention empty type, not payload.
	msg := stderr.String()
	if !strings.Contains(msg, "--type must not be empty") {
		t.Errorf("stderr should mention empty type first, got: %q", msg)
	}
}

func TestRunEmit_ErrorsToStderrOnly(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"GOLEMIC_RUN_ID":    "",
		"GOLEMIC_EVENT_LOG": filepath.Join(dir, "events.jsonl"),
	}

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := runEmit(args, &stdout, &stderr, func(k string) string { return env[k] })

	if got != 1 {
		t.Fatalf("exit code: got %d, want 1", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must be empty for error states, got: %q", stdout.String())
	}
}

func TestRunEmitDispatch(t *testing.T) {
	// Test that run() dispatches to runEmit with env vars set.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	t.Setenv("GOLEMIC_RUN_ID", "run-dispatch")
	t.Setenv("GOLEMIC_EVENT_LOG", logPath)
	t.Setenv("GOLEMIC_TURN_ID", "1")

	var stdout, stderr bytes.Buffer
	args := []string{"golemic", "emit", "--type", "dev_started", "--payload", "{}"}
	got := run(args, &stdout, &stderr)

	if got != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr: %s", got, stderr.String())
	}

	var r eventlog.Reader
	events, err := r.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "dev_started" {
		t.Errorf("Type: got %q, want %q", events[0].Type, "dev_started")
	}
}
