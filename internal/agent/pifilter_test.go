package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fatMessageUpdate returns a synthetic message_update line with large cumulative
// content in both assistantMessageEvent.partial.content and the top-level message,
// plus a delta, usage, model, stopReason, and timestamp.
func fatMessageUpdate(delta, cumulative string) string {
	partial := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": cumulative},
		},
	}
	message := map[string]any{
		"role": "assistant",
		"content": []map[string]any{
			{"type": "text", "text": cumulative},
		},
	}
	event := map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "content_block_delta",
			"contentIndex": 0,
			"delta":        delta,
			"partial":      partial,
		},
		"message":    message,
		"usage":      map[string]any{"inputTokens": 100, "outputTokens": 200},
		"model":      "claude-opus-4-5",
		"stopReason": "",
		"timestamp":  "2026-07-22T10:00:00Z",
	}
	b, _ := json.Marshal(event)
	return string(b) + "\n"
}

// fatMessageUpdateNoDelta returns a message_update with no delta field (e.g. text_start).
func fatMessageUpdateNoDelta(cumulative string) string {
	partial := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": cumulative},
		},
	}
	event := map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "content_block_start",
			"contentIndex": 0,
			"partial":      partial,
		},
		"usage":     map[string]any{"inputTokens": 50, "outputTokens": 0},
		"model":     "claude-opus-4-5",
		"timestamp": "2026-07-22T10:00:00Z",
	}
	b, _ := json.Marshal(event)
	return string(b) + "\n"
}

// assertAMEProjection checks the projected assistantMessageEvent for the
// expected delta value and that cumulative fields are absent.
func assertAMEProjection(t *testing.T, ame map[string]json.RawMessage, wantDelta string) {
	t.Helper()
	if _, ok := ame["type"]; !ok {
		t.Error("assistantMessageEvent.type missing from projection")
	}
	if _, ok := ame["contentIndex"]; !ok {
		t.Error("assistantMessageEvent.contentIndex missing from projection")
	}
	if wantDelta != "" && string(ame["delta"]) != `"`+wantDelta+`"` {
		t.Errorf("assistantMessageEvent.delta = %s, want %q", ame["delta"], wantDelta)
	}
	if _, ok := ame["partial"]; ok {
		t.Error("assistantMessageEvent.partial must be dropped from projection")
	}
}

// assertMetadataProjection checks that top-level metadata fields survive and
// cumulative blobs are absent.
func assertMetadataProjection(t *testing.T, result map[string]json.RawMessage, wantModel string) {
	t.Helper()
	if _, ok := result["message"]; ok {
		t.Error("top-level message must be dropped from projection")
	}
	if _, ok := result["usage"]; !ok {
		t.Error("usage missing from projection")
	}
	if wantModel != "" && string(result["model"]) != `"`+wantModel+`"` {
		t.Errorf("model = %s, want %q", result["model"], wantModel)
	}
	if _, ok := result["timestamp"]; !ok {
		t.Error("timestamp missing from projection")
	}
}

func TestFilterPiLine_MessageUpdateProjection(t *testing.T) {
	cumulative := strings.Repeat("x", 10_000)
	line := fatMessageUpdate("hello", cumulative)

	out := filterPiLine([]byte(line))

	outData := bytes.TrimRight(out, "\n")
	var result map[string]json.RawMessage
	if err := json.Unmarshal(outData, &result); err != nil {
		t.Fatalf("projected output is not valid JSON: %v\noutput: %s", err, out)
	}
	if string(result["type"]) != `"message_update"` {
		t.Errorf("type = %s, want %q", result["type"], "message_update")
	}
	var ame map[string]json.RawMessage
	if err := json.Unmarshal(result["assistantMessageEvent"], &ame); err != nil {
		t.Fatalf("assistantMessageEvent is not valid JSON: %v", err)
	}
	assertAMEProjection(t, ame, "hello")
	assertMetadataProjection(t, result, "claude-opus-4-5")
	if len(out) >= len(line)/2 {
		t.Errorf("projected size %d not significantly smaller than input size %d", len(out), len(line))
	}
}

func TestFilterPiLine_MessageUpdateNoDelta(t *testing.T) {
	cumulative := strings.Repeat("y", 5_000)
	line := fatMessageUpdateNoDelta(cumulative)

	out := filterPiLine([]byte(line))

	outData := bytes.TrimRight(out, "\n")
	var result map[string]json.RawMessage
	if err := json.Unmarshal(outData, &result); err != nil {
		t.Fatalf("projected output is not valid JSON: %v", err)
	}
	if string(result["type"]) != `"message_update"` {
		t.Errorf("type = %s, want %q", result["type"], "message_update")
	}

	var ame map[string]json.RawMessage
	if err := json.Unmarshal(result["assistantMessageEvent"], &ame); err != nil {
		t.Fatalf("assistantMessageEvent is not valid JSON: %v", err)
	}
	// delta must be absent (not present in input).
	if _, ok := ame["delta"]; ok {
		t.Error("delta must be absent when not in input")
	}
	// partial must be dropped.
	if _, ok := ame["partial"]; ok {
		t.Error("partial must be dropped from projection")
	}
	// message must be dropped.
	if _, ok := result["message"]; ok {
		t.Error("top-level message must be dropped from projection")
	}
}

func TestFilterPiLine_NonMessageUpdateVerbatim(t *testing.T) {
	cases := []string{
		`{"type":"tool_execution_start","toolName":"bash","args":{}}` + "\n",
		`{"type":"tool_execution_end","toolName":"bash","result":"ok"}` + "\n",
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stopReason":"end_turn"}}` + "\n",
		`{"type":"auto_retry_end","success":false}` + "\n",
		`{"type":"turn_start"}` + "\n",
		`{"type":"session","sessionId":"abc"}` + "\n",
	}
	for _, line := range cases {
		out := filterPiLine([]byte(line))
		if string(out) != line {
			t.Errorf("line %q was modified; want verbatim, got %q", line, out)
		}
	}
}

func TestFilterPiLine_MalformedJSONVerbatim(t *testing.T) {
	cases := []string{
		"not json at all\n",
		"{broken\n",
		"\n",
	}
	for _, line := range cases {
		out := filterPiLine([]byte(line))
		if string(out) != line {
			t.Errorf("malformed line %q was modified; want verbatim, got %q", line, out)
		}
	}
}

func TestLineFilterWriter_SplitWrites(t *testing.T) {
	cumulative := strings.Repeat("z", 1_000)
	line := fatMessageUpdate("chunk", cumulative)

	var buf bytes.Buffer
	w := &lineFilterWriter{dst: &buf}

	// Split the line into two writes at an arbitrary mid-point.
	mid := len(line) / 2
	if _, err := w.Write([]byte(line[:mid])); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatal("partial write should not flush any line yet")
	}
	if _, err := w.Write([]byte(line[mid:])); err != nil {
		t.Fatal(err)
	}

	out := buf.Bytes()
	if len(out) == 0 {
		t.Fatal("expected output after completing the line")
	}
	// Must be valid projected JSON.
	outData := bytes.TrimRight(out, "\n")
	var result map[string]json.RawMessage
	if err := json.Unmarshal(outData, &result); err != nil {
		t.Fatalf("output after split write is not valid JSON: %v", err)
	}
	if _, ok := result["message"]; ok {
		t.Error("top-level message must be dropped from projected output")
	}
}

func TestLineFilterWriter_MultipleLines(t *testing.T) {
	toolLine := `{"type":"tool_execution_start","toolName":"read","args":{}}` + "\n"
	muLine := fatMessageUpdate("delta-text", strings.Repeat("a", 500))
	endLine := `{"type":"message_end","message":{"role":"assistant","stopReason":"end_turn"}}` + "\n"

	input := toolLine + muLine + endLine

	var buf bytes.Buffer
	w := &lineFilterWriter{dst: &buf}
	if _, err := w.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}

	// Split output into lines.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 output lines, got %d: %v", len(lines), lines)
	}

	// First line: tool_execution_start verbatim (except we re-check it's identical).
	if lines[0]+"\n" != toolLine {
		t.Errorf("tool line not verbatim: got %q", lines[0])
	}

	// Second line: message_update projected (no message field).
	var muResult map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[1]), &muResult); err != nil {
		t.Fatalf("projected message_update is not valid JSON: %v", err)
	}
	if _, ok := muResult["message"]; ok {
		t.Error("top-level message must be absent from projected message_update")
	}
	if string(muResult["type"]) != `"message_update"` {
		t.Errorf("type = %s, want message_update", muResult["type"])
	}

	// Third line: message_end verbatim.
	if lines[2]+"\n" != endLine {
		t.Errorf("message_end line not verbatim: got %q", lines[2])
	}
}

// TestReadToolProgressUnaffectedByFilter verifies that readToolProgress correctly
// counts tool executions from a filtered stream (tool lines are verbatim).
func TestReadToolProgressUnaffectedByFilter(t *testing.T) {
	toolStart := `{"type":"tool_execution_start","toolName":"bash","args":{}}` + "\n"
	toolEnd := `{"type":"tool_execution_end","toolName":"bash","result":"ok"}` + "\n"
	muLine := fatMessageUpdate("hi", strings.Repeat("b", 200))

	input := toolStart + muLine + toolEnd

	// Write through the filter to a temp file.
	tmp := t.TempDir() + "/activity.jsonl"
	f, err := createFileForTest(tmp)
	if err != nil {
		t.Fatal(err)
	}
	w := &lineFilterWriter{dst: f}
	if _, err := w.Write([]byte(input)); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	var state toolProgressState
	completed, inFlight := readToolProgress(tmp, &state)
	if completed != 1 {
		t.Errorf("readToolProgress completed = %d, want 1", completed)
	}
	if inFlight {
		t.Error("readToolProgress inFlight = true, want false")
	}
}

// TestInspectTranscriptUnaffectedByFilter verifies that InspectTranscript
// correctly classifies a run from a filtered stream (message_end is verbatim).
func TestInspectTranscriptUnaffectedByFilter(t *testing.T) {
	muLine := fatMessageUpdate("prose", strings.Repeat("c", 300))
	endLine := `{"type":"message_end","message":{"role":"assistant","stopReason":"end_turn","errorMessage":""}}` + "\n"

	input := muLine + endLine

	tmp := t.TempDir() + "/activity.jsonl"
	f, err := createFileForTest(tmp)
	if err != nil {
		t.Fatal(err)
	}
	w := &lineFilterWriter{dst: f}
	if _, err := w.Write([]byte(input)); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	result := InspectTranscript(tmp)
	if result.SemanticFailed {
		t.Error("InspectTranscript: SemanticFailed = true, want false for end_turn")
	}
}

// createFileForTest creates (or truncates) a file at path for use in tests.
func createFileForTest(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
}
