// Package telemetry provides fail-open OTel-shaped span telemetry for golemic runs.
// Records are written to a per-run telemetry.jsonl file as newline-delimited JSON.
package telemetry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Span kind constants.
const (
	KindSpanStart = "span.start"
	KindSpanEnd   = "span.end"
)

// Status constants for span.end.
const (
	StatusOK     = "ok"
	StatusError  = "error"
	StatusKilled = "killed"
)

// Span name constants (low-cardinality set per BR-006).
const (
	SpanRun               = "run"
	SpanWorktreeCreate    = "worktree.create"
	SpanWorktreeCleanup   = "worktree.cleanup"
	SpanAgentTurn         = "agent.turn"
	SpanEscalationComment = "escalation.comment"
)

// Record is a single span.start or span.end telemetry record.
// Fields absent for a given kind are omitted from the JSON output.
type Record struct {
	Kind         string         `json:"kind"`
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Name         string         `json:"name,omitempty"`
	StartTS      string         `json:"start_ts,omitempty"`
	EndTS        string         `json:"end_ts,omitempty"`
	DurationMS   *int64         `json:"duration_ms,omitempty"`
	Status       string         `json:"status,omitempty"`
	Attrs        map[string]any `json:"attributes,omitempty"`
}

// Sink receives telemetry records. Implementations must be safe for concurrent use.
// Emit errors are returned to the caller but must be swallowed at the call site (BR-002).
type Sink interface {
	Emit(r Record) error
}

// NoopSink discards all records. Used when telemetry.enabled is false (BR-003).
type NoopSink struct{}

// Emit implements Sink and does nothing.
func (NoopSink) Emit(Record) error { return nil }

// FileSink appends JSONL records to a file, creating the file and its parent
// directories on first write (lazy open). Safe for concurrent use via mutex.
type FileSink struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// NewFileSink returns a FileSink that writes to path. The file is not opened
// until the first Emit call.
func NewFileSink(path string) *FileSink {
	return &FileSink{path: path}
}

// Emit marshals r to JSON and appends a newline-terminated line to the file.
func (s *FileSink) Emit(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("TELEMETRY_WRITE_FAILED: marshal: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file == nil {
		if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
			return fmt.Errorf("TELEMETRY_WRITE_FAILED: mkdir: %w", err)
		}
		f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("TELEMETRY_WRITE_FAILED: open: %w", err)
		}
		s.file = f
	}

	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("TELEMETRY_WRITE_FAILED: write: %w", err)
	}
	return nil
}

// Close closes the underlying file if it has been opened.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

// TraceID derives a deterministic 32-hex-character (128-bit) OTel-compatible
// trace ID from runID using SHA-256 truncated to 16 bytes (BR-004).
func TraceID(runID string) string {
	h := sha256.Sum256([]byte(runID))
	return hex.EncodeToString(h[:16])
}

// NewSpanID generates a random 16-hex-character (64-bit) OTel-compatible span ID.
func NewSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// StartSpan emits a span.start record to sink and returns the new span ID and
// an end function. Calling the end function emits the matching span.end record
// with the given status and optional end attributes.
//
// Errors from Emit are swallowed (BR-002): StartSpan always returns a valid
// spanID and endFunc regardless of sink health.
func StartSpan(sink Sink, traceID, parentSpanID, name string, attrs map[string]any) (spanID string, end func(status string, endAttrs map[string]any)) {
	spanID = NewSpanID()
	startTS := time.Now().UTC()

	_ = sink.Emit(Record{
		Kind:         KindSpanStart,
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Name:         name,
		StartTS:      startTS.Format(time.RFC3339),
		Attrs:        attrs,
	})

	end = func(status string, endAttrs map[string]any) {
		endTS := time.Now().UTC()
		ms := endTS.Sub(startTS).Milliseconds()
		_ = sink.Emit(Record{
			Kind:       KindSpanEnd,
			TraceID:    traceID,
			SpanID:     spanID,
			EndTS:      endTS.Format(time.RFC3339),
			DurationMS: &ms,
			Status:     status,
			Attrs:      endAttrs,
		})
	}

	return spanID, end
}
