package progress

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeActivity(t *testing.T, path string, lines ...string) {
	t.Helper()
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestFollowActivityJSONL_ToolCallsEmitted checks that tool_execution_start
// entries are emitted as tool-call lines.
func TestFollowActivityJSONL_ToolCallsEmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.activity.jsonl")
	writeActivity(t, path,
		`{"type":"session","version":3,"id":"x"}`,
		`{"type":"tool_execution_start","toolCallId":"id1","toolName":"bash","args":{"command":"go test ./..."}}`,
		`{"type":"tool_execution_start","toolCallId":"id2","toolName":"read","args":{"path":"internal/foo.go"}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`,
	)

	var buf bytes.Buffer
	r := New(&buf)
	stop := FollowActivityJSONL("dev", path, r)
	stop()

	out := buf.String()
	if !strings.Contains(out, "dev · bash: go test ./...") {
		t.Errorf("expected bash tool call line, got:\n%s", out)
	}
	if !strings.Contains(out, "dev · read: internal/foo.go") {
		t.Errorf("expected read tool call line, got:\n%s", out)
	}
}

// TestFollowActivityJSONL_FileNotExist checks non-fatal behaviour per BR-P3:
// when the file doesn't exist, stop() returns without blocking.
func TestFollowActivityJSONL_FileNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.activity.jsonl")
	var buf bytes.Buffer
	r := New(&buf)

	done := make(chan struct{})
	go func() {
		stop := FollowActivityJSONL("dev", path, r)
		stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("FollowActivityJSONL did not return within timeout when file missing")
	}
}

// TestFollowActivityJSONL_MalformedLinesSkipped checks that malformed JSON
// lines are skipped without aborting the follow reader per BR-P3.
func TestFollowActivityJSONL_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.activity.jsonl")
	writeActivity(t, path,
		`{not valid json`,
		`{"type":"tool_execution_start","toolName":"bash","args":{"command":"echo ok"}}`,
	)

	var buf bytes.Buffer
	r := New(&buf)
	stop := FollowActivityJSONL("dev", path, r)
	stop()

	out := buf.String()
	if !strings.Contains(out, "dev · bash: echo ok") {
		t.Errorf("expected bash line after malformed line, got:\n%s", out)
	}
}

// TestFollowActivityJSONL_OnlyToolCallsEmitted checks that non-tool_execution_start
// entries are silently ignored.
func TestFollowActivityJSONL_OnlyToolCallsEmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev.activity.jsonl")
	writeActivity(t, path,
		`{"type":"session"}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`,
	)

	var buf bytes.Buffer
	r := New(&buf)
	stop := FollowActivityJSONL("dev", path, r)
	stop()

	if buf.Len() != 0 {
		t.Errorf("expected no output for non-tool entries, got: %q", buf.String())
	}
}
