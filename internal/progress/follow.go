package progress

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const pollInterval = 200 * time.Millisecond

// activityEntry is the minimal shape of a tool_execution_start line.
type activityEntry struct {
	Type     string          `json:"type"`
	ToolName string          `json:"toolName"`
	Args     json.RawMessage `json:"args"`
}

// FollowActivityJSONL starts following the activity.jsonl file at path and
// emitting tool-call progress lines via r. role is used as the line prefix.
//
// The returned stop function must be called to release resources. It blocks
// until the follower has drained all pending lines and exited (BR-P6 / BR-P3).
func FollowActivityJSONL(role, path string, r *Renderer) func() {
	done := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		var lastLine int
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			lastLine = emitNewToolCalls(role, path, lastLine, r)
			select {
			case <-done:
				emitNewToolCalls(role, path, lastLine, r) // drain
				return
			case <-ticker.C:
			}
		}
	}()

	return func() {
		close(done)
		<-finished
	}
}

// emitNewToolCalls reads activity.jsonl from startLine, emits tool_execution_start
// entries, and returns the new total line count. Non-fatal on I/O errors (BR-P3).
func emitNewToolCalls(role, path string, startLine int, r *Renderer) int {
	f, err := os.Open(path)
	if err != nil {
		return startLine
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= startLine {
			continue
		}
		var entry activityEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip malformed lines per BR-P3
		}
		if entry.Type == "tool_execution_start" && entry.ToolName != "" {
			r.EmitToolCall(role, entry.ToolName, entry.Args)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "progress: follow read error for %s: %v\n", path, err)
	}
	return lineNum
}
