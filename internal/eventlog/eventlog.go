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
	EventRunStarted             = "run_started"
	EventWorktreeCreated        = "worktree_created"
	EventDevStarted             = "dev_started"
	EventPROpened               = "pr_opened"
	EventReviewSubmitted        = "review_submitted"
	EventRunFinished            = "run_finished"
	EventAgentCompleted         = "agent_completed"
	EventCIWaitFinished         = "ci_wait_finished"
	EventPRMerged               = "pr_merged"
	EventAutomergeSkipped       = "automerge_skipped"
	EventAutomergeFailed        = "automerge_failed"
	EventAutomergeConflictRetry  = "automerge_conflict_retry"
	EventAutomergeOutOfDateRetry = "automerge_out_of_date_retry"
	EventIssueClaimed            = "issue_claimed"
	EventIssueReleased          = "issue_released"
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
		EventPRMerged,
		EventAutomergeSkipped,
		EventAutomergeFailed,
		EventAutomergeConflictRetry,
		EventAutomergeOutOfDateRetry,
		EventIssueClaimed,
		EventIssueReleased,
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

// issueClaimedData is the payload shape for issue_claimed events.
type issueClaimedData struct {
	IssueNumber  int    `json:"issue_number"`
	VerifyResult string `json:"verify_result"`
}

// ValidateIssueClaimedPayload checks that the payload has a non-zero issue_number.
func ValidateIssueClaimedPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("issue_claimed payload is empty")
	}
	var d issueClaimedData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("issue_claimed payload: invalid JSON: %w", err)
	}
	if d.IssueNumber <= 0 {
		return fmt.Errorf("issue_claimed payload: issue_number is required")
	}
	return nil
}

// MarshalIssueClaimedPayload encodes an issue_claimed payload.
func MarshalIssueClaimedPayload(issueNumber int, verifyResult string) (json.RawMessage, error) {
	return json.Marshal(issueClaimedData{IssueNumber: issueNumber, VerifyResult: verifyResult})
}

// issueReleasedData is the payload shape for issue_released events.
type issueReleasedData struct {
	IssueNumber int    `json:"issue_number"`
	Reason      string `json:"reason"`
}

// ValidateIssueReleasedPayload checks that the payload has a non-zero issue_number
// and a reason in {done, failed, abandoned}.
func ValidateIssueReleasedPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("issue_released payload is empty")
	}
	var d issueReleasedData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("issue_released payload: invalid JSON: %w", err)
	}
	if d.IssueNumber <= 0 {
		return fmt.Errorf("issue_released payload: issue_number is required")
	}
	switch d.Reason {
	case "done", "failed", "abandoned":
		return nil
	default:
		return fmt.Errorf("issue_released payload: reason must be one of done, failed, abandoned, got %q", d.Reason)
	}
}

// MarshalIssueReleasedPayload encodes an issue_released payload.
func MarshalIssueReleasedPayload(issueNumber int, reason string) (json.RawMessage, error) {
	return json.Marshal(issueReleasedData{IssueNumber: issueNumber, Reason: reason})
}

// automergeConflictRetryData is the payload shape for automerge_conflict_retry events.
type automergeConflictRetryData struct {
	ConflictedFiles []string `json:"conflictedFiles"`
	Result          string   `json:"result"`
	TurnID          int      `json:"turnId"`
}

// ValidateAutomergeConflictRetryPayload checks that the payload has non-empty conflictedFiles
// and a result in {resolved, unresolved}.
func ValidateAutomergeConflictRetryPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("automerge_conflict_retry payload is empty")
	}
	var d automergeConflictRetryData
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("automerge_conflict_retry payload: invalid JSON: %w", err)
	}
	if len(d.ConflictedFiles) == 0 {
		return fmt.Errorf("automerge_conflict_retry payload: conflictedFiles must be non-empty")
	}
	switch d.Result {
	case "resolved", "unresolved":
		return nil
	default:
		return fmt.Errorf("automerge_conflict_retry payload: result must be one of resolved, unresolved, got %q", d.Result)
	}
}

// MarshalAutomergeConflictRetryPayload encodes an automerge_conflict_retry payload.
func MarshalAutomergeConflictRetryPayload(conflictedFiles []string, result string, turnID int) (json.RawMessage, error) {
	return json.Marshal(automergeConflictRetryData{ConflictedFiles: conflictedFiles, Result: result, TurnID: turnID})
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
	TurnID  int             `json:"turnId"`
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
	Verdict            string `json:"verdict"`
	MergeConfidence    string `json:"mergeConfidence"`
	ReviewID           string `json:"reviewId"`
	InlineCommentCount *int   `json:"inlineCommentCount"`
}

// ValidateReviewSubmittedPayload checks that payload decodes to an object with
// verdict ∈ {approved, changes_requested}, mergeConfidence ∈ {high, low},
// non-empty reviewId, and non-negative inlineCommentCount (BR-006).
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
	default:
		return fmt.Errorf("review_submitted payload: verdict must be %q or %q, got %q",
			"approved", "changes_requested", d.Verdict)
	}
	switch d.MergeConfidence {
	case "high", "medium", "low":
	default:
		return fmt.Errorf("review_submitted payload: mergeConfidence must be %q, %q, or %q, got %q",
			"low", "medium", "high", d.MergeConfidence)
	}
	if d.ReviewID == "" {
		return fmt.Errorf("review_submitted payload: reviewId is required")
	}
	if d.InlineCommentCount == nil {
		return fmt.Errorf("review_submitted payload: inlineCommentCount is required")
	}
	if *d.InlineCommentCount < 0 {
		return fmt.Errorf("review_submitted payload: inlineCommentCount must be >= 0, got %d", *d.InlineCommentCount)
	}
	return nil
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

// validateEventPayload dispatches to the type-specific validator for events that
// carry payload constraints. Returns nil for event types with no constraints.
func validateEventPayload(event Event) error {
	switch event.Type {
	case EventPROpened:
		return ValidatePROpenedPayload(event.Payload)
	case EventReviewSubmitted:
		return ValidateReviewSubmittedPayload(event.Payload)
	case EventCIWaitFinished:
		return ValidateCIWaitFinishedPayload(event.Payload)
	case EventIssueClaimed:
		return ValidateIssueClaimedPayload(event.Payload)
	case EventIssueReleased:
		return ValidateIssueReleasedPayload(event.Payload)
	case EventAutomergeConflictRetry:
		return ValidateAutomergeConflictRetryPayload(event.Payload)
	}
	return nil
}

// Write appends one event to the log. It validates event payload constraints
// (e.g. review_submitted verdict) before writing.
func (w *Writer) Write(event Event) error {
	if err := validateEventPayload(event); err != nil {
		return err
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

// HasTurnTypeEvent returns true if events contains an event matching turnID and eventType.
func HasTurnTypeEvent(events []Event, turnID int, eventType string) bool {
	for _, ev := range events {
		if ev.TurnID == turnID && ev.Type == eventType {
			return true
		}
	}
	return false
}

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
