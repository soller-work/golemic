// Package eventlog provides an append-only JSONL event log for golemic runs.
//
// Format: one JSON object per line in ~/.golemic/<project>/runs/<runId>/events.jsonl.
// Every write is a single O_APPEND write(2) call so OS-level concurrent-process
// safety holds. The Writer is also goroutine-safe via a mutex.
package eventlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Event type constants
// ---------------------------------------------------------------------------

const (
	EventRunStarted      = "run_started"
	EventWorktreeCreated = "worktree_created"
	EventDevStarted      = "dev_started"
	EventPROpened        = "pr_opened"
	EventReviewSubmitted = "review_submitted"
	EventRunFinished     = "run_finished"
	EventAgentCompleted  = "agent_completed"
	EventCIWaitFinished  = "ci_wait_finished"
)

// AllEventTypes returns every defined event type constant for documentation / validation.
func AllEventTypes() []string {
	return []string{
		EventRunStarted,
		EventWorktreeCreated,
		EventDevStarted,
		EventPROpened,
		EventReviewSubmitted,
		EventRunFinished,
		EventAgentCompleted,
		EventCIWaitFinished,
	}
}

// ciWaitFinishedData is the payload shape for ci_wait_finished events.
type ciWaitFinishedData struct {
	Result string `json:"result"`
	Round  int    `json:"round"`
}

// ValidateCIWaitFinishedPayload checks that the payload has a valid result field.
func ValidateCIWaitFinishedPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("ci_wait_finished payload is empty")
	}
	var d ciWaitFinishedData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("ci_wait_finished payload: invalid JSON: %w", err)
	}
	switch d.Result {
	case "green", "red", "timeout", "no_checks":
		return nil
	default:
		return fmt.Errorf("ci_wait_finished payload: result must be one of green, red, timeout, no_checks, got %q", d.Result)
	}
}

// MarshalCIWaitFinishedPayload encodes a ci_wait_finished payload.
func MarshalCIWaitFinishedPayload(result string, round int) (json.RawMessage, error) {
	return json.Marshal(ciWaitFinishedData{Result: result, Round: round})
}

// agentCompletedData is the payload shape for agent_completed events.
type agentCompletedData struct {
	Role     string `json:"role"`
	ExitCode int    `json:"exitCode"`
}

// MarshalAgentCompletedPayload encodes an agent_completed payload.
func MarshalAgentCompletedPayload(role string, exitCode int) (json.RawMessage, error) {
	return json.Marshal(agentCompletedData{Role: role, ExitCode: exitCode})
}

// ---------------------------------------------------------------------------
// Event struct
// ---------------------------------------------------------------------------

// Event represents one entry in the JSONL event log.
type Event struct {
	Type    string          `json:"type"`
	Ts      string          `json:"ts"` // RFC3339 string
	RunID   string          `json:"runId"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ---------------------------------------------------------------------------
// Payload helpers
// ---------------------------------------------------------------------------

// prOpenedData is the expected payload shape for pr_opened events.
type prOpenedData struct {
	PRNumber string `json:"prNumber"`
}

// ValidatePROpenedPayload checks that payload decodes to an object with prNumber field.
func ValidatePROpenedPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("pr_opened payload is empty")
	}
	var d prOpenedData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("pr_opened payload: invalid JSON: %w", err)
	}
	if d.PRNumber == "" {
		return fmt.Errorf("pr_opened payload: prNumber field is required")
	}
	return nil
}

// reviewSubmittedData is the expected payload shape for review_submitted events.
type reviewSubmittedData struct {
	Verdict string `json:"verdict"`
}

// ValidateReviewSubmittedPayload checks that payload decodes to an object with
// verdict ∈ {approved, changes_requested}.
func ValidateReviewSubmittedPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("review_submitted payload is empty")
	}
	var d reviewSubmittedData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("review_submitted payload: invalid JSON: %w", err)
	}
	switch d.Verdict {
	case "approved", "changes_requested":
		return nil
	default:
		return fmt.Errorf("review_submitted payload: verdict must be %q or %q, got %q",
			"approved", "changes_requested", d.Verdict)
	}
}

// ---------------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------------

// Writer appends events to a JSONL file. It is safe for concurrent use.
type Writer struct {
	mu       sync.Mutex
	filePath string
	file     *os.File
}

// NewWriter creates (or opens for append) a Writer at the given absolute path.
// Parent directories are created as needed.
func NewWriter(path string) (*Writer, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("LOG_DIR_CREATE_FAILED: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("LOG_FILE_OPEN_FAILED: %w", err)
	}

	return &Writer{filePath: path, file: f}, nil
}

// Write appends one event to the log. It validates event payload constraints
// (e.g. review_submitted verdict) before writing.
func (w *Writer) Write(event Event) error {
	// Validate payload for pr_opened.
	if event.Type == EventPROpened {
		if err := ValidatePROpenedPayload(event.Payload); err != nil {
			return err
		}
	}
	// Validate payload for review_submitted.
	if event.Type == EventReviewSubmitted {
		if err := ValidateReviewSubmittedPayload(event.Payload); err != nil {
			return err
		}
	}
	// Validate payload for ci_wait_finished.
	if event.Type == EventCIWaitFinished {
		if err := ValidateCIWaitFinishedPayload(event.Payload); err != nil {
			return err
		}
	}

	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("EVENT_MARSHAL_FAILED: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}
	return nil
}

// Path returns the absolute file path this writer is writing to.
func (w *Writer) Path() string {
	return w.filePath
}

// Close closes the underlying file.
func (w *Writer) Close() error {
	return w.file.Close()
}

// ---------------------------------------------------------------------------
// Reader
// ---------------------------------------------------------------------------

// Reader reads events from a JSONL file.
type Reader struct{}

// Read reads the entire JSONL file at path and returns all events.
// On the first malformed line it returns an error naming the line number and
// parse failure. No events are returned (fail-closed).
func (Reader) Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("LOG_FILE_NOT_FOUND: %s", path)
		}
		return nil, fmt.Errorf("failed to open event log file %s: %w", path, err)
	}
	defer f.Close()

	// Collect all raw lines with their 1-based line numbers.
	type rawLine struct {
		num int
		raw string
	}
	var lines []rawLine
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		lines = append(lines, rawLine{num: lineNum, raw: scanner.Text()})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read event log file %s: %w", path, err)
	}

	// Trailing empty line at EOF (terminal newline) is normal JSONL — drop it.
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].raw) == "" {
		lines = lines[:len(lines)-1]
	}

	// Any other empty/whitespace-only line is malformed (fail-closed).
	var events []Event
	for _, l := range lines {
		trimmed := strings.TrimSpace(l.raw)
		if trimmed == "" {
			return nil, fmt.Errorf("MALFORMED_LINE: malformed event log line %d: empty line", l.num)
		}
		var ev Event
		if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
			return nil, fmt.Errorf("MALFORMED_LINE: malformed event log line %d: %v", l.num, err)
		}
		events = append(events, ev)
	}
	return events, nil
}

// ---------------------------------------------------------------------------
// LastEventOfType
// ---------------------------------------------------------------------------

// LastEventOfType returns a pointer to the last (most recent / highest index)
// event in events whose Type matches eventType. Returns an error if none found.
func LastEventOfType(events []Event, eventType string) (*Event, error) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventType {
			return &events[i], nil
		}
	}
	return nil, fmt.Errorf("EVENT_TYPE_NOT_FOUND: no event of type %q found in log", eventType)
}

// ---------------------------------------------------------------------------
// Env-var context resolution
// ---------------------------------------------------------------------------

// ResolveContext reads GOLEMIC_RUN_ID and GOLEMIC_EVENT_LOG from the environment
// and resolves the event log path according to decision table DT-001.
//
//  1. GOLEMIC_EVENT_LOG set AND GOLEMIC_RUN_ID set → use GOLEMIC_EVENT_LOG as-is.
//  2. GOLEMIC_EVENT_LOG NOT set, GOLEMIC_RUN_ID set, project available
//     → construct ~/.golemic/<project>/runs/<runId>/events.jsonl.
//  3. Neither set → error listing both missing vars.
//  4. Any other combination (e.g. only one set) → error "insufficient context".
//
// The function does NOT call os.Exit; callers must do that.
//
// Callers must validate the project name via credentials.ValidateProjectName
// before calling this function. eventlog does not validate project names
// internally to avoid a cross-package dependency on internal/credentials.
func ResolveContext(homeDir, project string) (runID string, logPath string, err error) {
	eventLogEnv := os.Getenv("GOLEMIC_EVENT_LOG")
	runIDEnv := os.Getenv("GOLEMIC_RUN_ID")

	// Row 1: both set → use GOLEMIC_EVENT_LOG as absolute path.
	if eventLogEnv != "" && runIDEnv != "" {
		return runIDEnv, eventLogEnv, nil
	}

	// Row 2: only RUN_ID set, project available → construct path.
	if eventLogEnv == "" && runIDEnv != "" && project != "" {
		path := filepath.Join(homeDir, ".golemic", project, "runs", runIDEnv, "events.jsonl")
		return runIDEnv, path, nil
	}

	// Row 3: neither set.
	if eventLogEnv == "" && runIDEnv == "" {
		return "", "", fmt.Errorf("missing required environment variables: GOLEMIC_RUN_ID, GOLEMIC_EVENT_LOG")
	}

	// Default: any other combination (e.g. only one set).
	return "", "", fmt.Errorf("insufficient context to determine event log path: "+
		"both GOLEMIC_RUN_ID and GOLEMIC_EVENT_LOG are required; "+
		"GOLEMIC_RUN_ID=%q, GOLEMIC_EVENT_LOG=%q", runIDEnv, eventLogEnv)
}
