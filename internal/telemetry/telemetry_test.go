package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TraceID and NewSpanID
// ---------------------------------------------------------------------------

func TestTraceID_Deterministic(t *testing.T) {
	id1 := TraceID("issue-42-20240101T000000Z")
	id2 := TraceID("issue-42-20240101T000000Z")
	if id1 != id2 {
		t.Errorf("TraceID is not deterministic: %q != %q", id1, id2)
	}
	if len(id1) != 32 {
		t.Errorf("TraceID length: got %d, want 32", len(id1))
	}
}

func TestTraceID_DifferentRunIDs(t *testing.T) {
	id1 := TraceID("issue-1-20240101T000000Z")
	id2 := TraceID("issue-2-20240101T000000Z")
	if id1 == id2 {
		t.Error("different runIDs must produce different trace IDs")
	}
}

func TestNewSpanID_Unique(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := NewSpanID()
		if len(id) != 16 {
			t.Errorf("SpanID length: got %d, want 16", len(id))
		}
		if _, dup := ids[id]; dup {
			t.Errorf("duplicate span ID: %q", id)
		}
		ids[id] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// NoopSink
// ---------------------------------------------------------------------------

func TestNoopSink_WritesNothing(t *testing.T) {
	var s NoopSink
	for i := 0; i < 5; i++ {
		if err := s.Emit(Record{Kind: KindSpanStart, TraceID: "abc", SpanID: "def"}); err != nil {
			t.Errorf("NoopSink.Emit returned error: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// FileSink
// ---------------------------------------------------------------------------

func TestFileSink_AppendsValidJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")
	sink := NewFileSink(path)
	t.Cleanup(func() { sink.Close() }) //nolint:errcheck

	ms := int64(0)
	records := []Record{
		{Kind: KindSpanStart, TraceID: "trace1", SpanID: "span1", Name: SpanRun, StartTS: time.Now().Format(time.RFC3339)},
		{Kind: KindSpanEnd, TraceID: "trace1", SpanID: "span1", EndTS: time.Now().Format(time.RFC3339), DurationMS: &ms, Status: StatusOK},
	}

	for _, r := range records {
		if err := sink.Emit(r); err != nil {
			t.Fatalf("Emit returned error: %v", err)
		}
	}

	// Verify file contents
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for i, line := range lines {
		var got Record
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i+1, err)
		}
	}
}

func TestFileSink_CreatesParentDirs(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "sub", "dir", "telemetry.jsonl")
	sink := NewFileSink(path)
	t.Cleanup(func() { sink.Close() }) //nolint:errcheck

	if err := sink.Emit(Record{Kind: KindSpanStart, TraceID: "t", SpanID: "s"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestFileSink_LazyOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")
	sink := NewFileSink(path)

	// File must not exist before first Emit
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("file must not exist before first Emit")
	}

	if err := sink.Emit(Record{Kind: KindSpanStart, TraceID: "t", SpanID: "s"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	sink.Close() //nolint:errcheck

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file must exist after first Emit: %v", err)
	}
}

// ---------------------------------------------------------------------------
// StartSpan
// ---------------------------------------------------------------------------

func TestStartSpan_EmitsStartThenEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")
	sink := NewFileSink(path)
	t.Cleanup(func() { sink.Close() }) //nolint:errcheck

	traceID := TraceID("issue-42-20240101T000000Z")
	spanID, end := StartSpan(sink, traceID, "", SpanRun, map[string]any{"pid": 12345})
	if spanID == "" {
		t.Error("StartSpan returned empty spanID")
	}
	end(StatusOK, map[string]any{"outcome": "success"})

	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	assertSpanStart(t, records[0], traceID, spanID, "", SpanRun)
	assertSpanEnd(t, records[1], spanID, StatusOK)
}

func assertSpanStart(t *testing.T, r Record, traceID, spanID, parentID, name string) {
	t.Helper()
	if r.Kind != KindSpanStart {
		t.Errorf("kind: got %q, want %q", r.Kind, KindSpanStart)
	}
	if r.TraceID != traceID {
		t.Errorf("trace_id: got %q, want %q", r.TraceID, traceID)
	}
	if r.SpanID != spanID {
		t.Errorf("span_id: got %q, want %q", r.SpanID, spanID)
	}
	if r.ParentSpanID != parentID {
		t.Errorf("parent_span_id: got %q, want %q", r.ParentSpanID, parentID)
	}
	if r.Name != name {
		t.Errorf("name: got %q, want %q", r.Name, name)
	}
	if r.StartTS == "" {
		t.Error("start_ts must not be empty")
	}
}

func assertSpanEnd(t *testing.T, r Record, spanID, status string) {
	t.Helper()
	if r.Kind != KindSpanEnd {
		t.Errorf("kind: got %q, want %q", r.Kind, KindSpanEnd)
	}
	if r.SpanID != spanID {
		t.Errorf("span_id mismatch on span.end")
	}
	if r.DurationMS == nil {
		t.Error("duration_ms must not be nil on span.end")
	} else if *r.DurationMS < 0 {
		t.Errorf("duration_ms must be >= 0, got %d", *r.DurationMS)
	}
	if r.Status != status {
		t.Errorf("status: got %q, want %q", r.Status, status)
	}
}

func TestStartSpan_ParentSpanID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")
	sink := NewFileSink(path)
	t.Cleanup(func() { sink.Close() }) //nolint:errcheck

	traceID := "abcdef1234567890abcdef1234567890"
	parentID := "aabbccdd11223344"

	spanID, end := StartSpan(sink, traceID, parentID, SpanAgentTurn, map[string]any{"role": "dev"})
	end(StatusOK, nil)

	records := readRecords(t, path)
	if len(records) < 1 {
		t.Fatal("no records written")
	}
	assertSpanStart(t, records[0], traceID, spanID, parentID, SpanAgentTurn)
}

func TestStartSpan_FailingSink_StillReturnsValidSpanID(t *testing.T) {
	sink := &failingSink{}
	spanID, end := StartSpan(sink, "trace", "", SpanRun, nil)
	if spanID == "" {
		t.Error("StartSpan must return a non-empty spanID even when sink fails")
	}
	if end == nil {
		t.Error("StartSpan must return a non-nil end func even when sink fails")
	}
	// end must not panic
	end(StatusError, nil)
}

func TestStartSpan_ZeroDurationRecorded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")
	sink := NewFileSink(path)
	t.Cleanup(func() { sink.Close() }) //nolint:errcheck

	_, end := StartSpan(sink, "t", "", SpanWorktreeCreate, nil)
	end(StatusOK, nil)

	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	endRec := records[1]
	if endRec.DurationMS == nil {
		t.Fatal("duration_ms must be present on span.end")
	}
	if *endRec.DurationMS < 0 {
		t.Errorf("duration_ms must be >= 0, got %d", *endRec.DurationMS)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type failingSink struct{}

func (failingSink) Emit(Record) error { return errors.New("sink error") }

func readRecords(t *testing.T, path string) []Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck

	var records []Record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unmarshal record: %v", err)
		}
		records = append(records, r)
	}
	return records
}
