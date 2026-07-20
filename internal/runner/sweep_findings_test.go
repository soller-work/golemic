package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golemic/internal/credentials"
	"golemic/internal/eventlog"
)

// setupRunnerCreds loads credentials from a temp home dir and attaches them to r.
func setupRunnerCreds(t *testing.T, r *Runner) {
	t.Helper()
	homeDir, _, project := setupRunnerTest(t)
	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	r.creds = creds
}

// ---------------------------------------------------------------------------
// latestReviewID unit tests
// ---------------------------------------------------------------------------

// writeReviewSubmittedEventWithID appends a review_submitted event with the given reviewId.
func writeReviewSubmittedEventWithID(t *testing.T, logPath, verdict, reviewID string) {
	t.Helper()
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer w.Close() //nolint:errcheck

	zero := 0
	payload, _ := json.Marshal(map[string]interface{}{
		"verdict":            verdict,
		"mergeConfidence":    "high",
		"reviewId":           reviewID,
		"inlineCommentCount": &zero,
	})
	if err := w.Write(eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "test-run",
		Payload: payload,
	}); err != nil {
		t.Fatalf("write event: %v", err)
	}
}

// Unit: latestReviewID returns id from latest review_submitted event (AC-001 trace).
func TestLatestReviewID_ReturnsID(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	writeReviewSubmittedEventWithID(t, logPath, "approved", "PRR_abc123")

	id, err := r.latestReviewID(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "PRR_abc123" {
		t.Errorf("reviewId: got %q, want %q", id, "PRR_abc123")
	}
}

// Unit: latestReviewID returns latest event's id when multiple exist.
func TestLatestReviewID_ReturnsLatest(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	writeReviewSubmittedEventWithID(t, logPath, "changes_requested", "PRR_first")
	writeReviewSubmittedEventWithID(t, logPath, "approved", "PRR_latest")

	id, err := r.latestReviewID(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "PRR_latest" {
		t.Errorf("reviewId: got %q, want %q", id, "PRR_latest")
	}
}

// Unit: latestReviewID returns error when reviewId field is empty
// (written directly as raw JSONL to bypass eventlog validation, simulating a malformed event).
func TestLatestReviewID_EmptyIDReturnsError(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)

	// Write raw JSONL directly to bypass eventlog writer validation
	line := fmt.Sprintf(`{"type":"review_submitted","ts":%q,"runId":"r1","payload":{"verdict":"approved","mergeConfidence":"high","reviewId":"","inlineCommentCount":0}}`,
		time.Now().Format(time.RFC3339))
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	_, err := r.latestReviewID(logPath)
	if err == nil {
		t.Fatal("expected error for empty reviewId, got nil")
	}
	if !strings.Contains(err.Error(), "NO_VALID_REVIEW") {
		t.Errorf("error should contain NO_VALID_REVIEW; got: %v", err)
	}
}

// Unit: latestReviewID returns error when no review_submitted event exists.
func TestLatestReviewID_NoEventReturnsError(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)

	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"issue": 1, "runId": "r1"})
	_ = w.Write(eventlog.Event{
		Type:    eventlog.EventRunStarted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "r1",
		Payload: payload,
	})
	w.Close() //nolint:errcheck

	_, err = r.latestReviewID(logPath)
	if err == nil {
		t.Fatal("expected error for missing review_submitted, got nil")
	}
	if !strings.Contains(err.Error(), "NO_VALID_REVIEW") {
		t.Errorf("error should contain NO_VALID_REVIEW; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FindingsJSON marshaller tests (AC-002 trace)
// ---------------------------------------------------------------------------

// Unit: loadInlineComments transforms REST response into FindingsJSON shape (golden).
func TestLoadInlineComments_GoldenFixture(t *testing.T) { //nolint:funlen,cyclop
	const restResponse = `[
		{"path":"pkg/server.go","line":42,"side":"RIGHT","body":"Nil pointer risk here","pull_request_review_id":1},
		{"path":"internal/handler.go","original_line":15,"side":"LEFT","body":"Dead code","pull_request_review_id":1}
	]`

	calls := 0
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			calls++
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"testowner"},"name":"testrepo"}`, nil
			}
			if name == "gh" && args[0] == "api" {
				return restResponse, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
	r := &Runner{executor: exec, repoRoot: t.TempDir()}
	setupRunnerCreds(t, r)

	entries, err := r.loadInlineComments(99, "PRR_abc123")
	if err != nil {
		t.Fatalf("loadInlineComments: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// First entry: explicit line field
	if entries[0].Path != "pkg/server.go" {
		t.Errorf("entries[0].Path: got %q, want %q", entries[0].Path, "pkg/server.go")
	}
	if entries[0].Line != 42 {
		t.Errorf("entries[0].Line: got %d, want 42", entries[0].Line)
	}
	if entries[0].Side != "RIGHT" {
		t.Errorf("entries[0].Side: got %q, want RIGHT", entries[0].Side)
	}
	if entries[0].Body != "Nil pointer risk here" {
		t.Errorf("entries[0].Body: got %q", entries[0].Body)
	}

	// Second entry: fallback to original_line
	if entries[1].Path != "internal/handler.go" {
		t.Errorf("entries[1].Path: got %q", entries[1].Path)
	}
	if entries[1].Line != 15 {
		t.Errorf("entries[1].Line: got %d, want 15 (from original_line)", entries[1].Line)
	}

	// Verify JSON marshalling produces expected fields
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"path"`, `"line"`, `"side"`, `"body"`, `"pkg/server.go"`, `"RIGHT"`} {
		if !strings.Contains(got, want) {
			t.Errorf("FindingsJSON missing %q; got: %s", want, got)
		}
	}
}

// Unit: loadInlineComments returns empty slice for empty REST response.
func TestLoadInlineComments_EmptyResponse(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			return "[]", nil
		},
	}
	r := &Runner{executor: exec, repoRoot: t.TempDir()}
	setupRunnerCreds(t, r)

	entries, err := r.loadInlineComments(1, "PRR_x")
	if err != nil {
		t.Fatalf("loadInlineComments: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty entries, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// Integration: pre-round sweep (AC-001 traces)
// ---------------------------------------------------------------------------

// Integration: sweep calls delete when a pending review is discovered.
func TestSweepPendingReviews_DeletesOrphan_AC001(t *testing.T) { //nolint:cyclop
	const orphanID = "PRR_orphan_node"
	discoverResp := fmt.Sprintf(
		`{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[{"id":%q}]}}}}}`,
		orphanID,
	)
	var graphqlCalls [][]string
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && args[1] == "graphql" {
				graphqlCalls = append(graphqlCalls, append([]string(nil), args...))
				// First call: discover → return orphan; subsequent: delete → empty
				if len(graphqlCalls) == 1 {
					return discoverResp, nil
				}
				return `{"data":{"deletePullRequestReview":{"pullRequestReview":{"id":"PRR_orphan_node"}}}}`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
	r := &Runner{executor: exec, repoRoot: t.TempDir()}
	setupRunnerCreds(t, r)

	if err := r.sweepPendingReviews(99); err != nil {
		t.Fatalf("sweepPendingReviews: %v", err)
	}
	if len(graphqlCalls) != 2 {
		t.Fatalf("expected 2 graphql calls (discover+delete), got %d", len(graphqlCalls))
	}
	// Verify delete call contains the orphan node id
	deleteArgs := strings.Join(graphqlCalls[1], " ")
	if !strings.Contains(deleteArgs, orphanID) {
		t.Errorf("delete call must reference orphan id %q; args: %v", orphanID, graphqlCalls[1])
	}
	if !strings.Contains(deleteArgs, "deletePullRequestReview") {
		t.Errorf("delete call must use deletePullRequestReview mutation; args: %v", graphqlCalls[1])
	}
}

// Integration: sweep is a no-op when no pending review exists.
func TestSweepPendingReviews_NoPendingNoop_AC001(t *testing.T) {
	var graphqlCalls [][]string
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" && args[1] == "graphql" {
				graphqlCalls = append(graphqlCalls, append([]string(nil), args...))
				return `{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[]}}}}}`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
	r := &Runner{executor: exec, repoRoot: t.TempDir()}
	setupRunnerCreds(t, r)

	if err := r.sweepPendingReviews(99); err != nil {
		t.Fatalf("sweepPendingReviews: %v", err)
	}
	// Only the discover call, no delete
	if len(graphqlCalls) != 1 {
		t.Errorf("expected 1 graphql call (discover only), got %d", len(graphqlCalls))
	}
}

// ---------------------------------------------------------------------------
// Integration: FindingsJSON injection (AC-002 trace)
// ---------------------------------------------------------------------------

// Integration: buildFindingsJSON returns JSON array of correct length and fields.
func TestBuildFindingsJSON_InjectsComments_AC002(t *testing.T) { //nolint:cyclop
	const restComments = `[
		{"path":"main.go","line":10,"side":"RIGHT","body":"Unnecessary import"},
		{"path":"util.go","line":5,"side":"RIGHT","body":"Error not handled"}
	]`

	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			if name == "gh" && args[0] == "api" {
				if args[1] == "graphql" {
					return `{}`, nil // not called in buildFindingsJSON
				}
				return restComments, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}
	r := &Runner{executor: exec, repoRoot: t.TempDir()}
	setupRunnerCreds(t, r)

	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 99)
	writeReviewSubmittedEventWithID(t, logPath, "changes_requested", "PRR_round1")

	got, err := r.buildFindingsJSON(logPath)
	if err != nil {
		t.Fatalf("buildFindingsJSON: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty FindingsJSON")
	}

	var entries []findingEntry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("unmarshal FindingsJSON: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Path != "main.go" {
		t.Errorf("entries[0].Path: got %q, want %q", entries[0].Path, "main.go")
	}
	if entries[1].Body != "Error not handled" {
		t.Errorf("entries[1].Body: got %q", entries[1].Body)
	}
}

// ---------------------------------------------------------------------------
// Schema-level query shape validation
// ---------------------------------------------------------------------------

// TestGraphqlDiscoverPending_QueryShape validates that graphqlDiscoverPending
// does not use the object-literal author argument that GitHub rejects
// ("Expected type 'String'" error). PENDING state alone scopes to the viewer.
func TestGraphqlDiscoverPending_QueryShape(t *testing.T) {
	// Must not contain object-literal author filter (GitHub rejects it with argumentLiteralsIncompatible).
	if strings.Contains(graphqlDiscoverPending, `author:{`) {
		t.Error("graphqlDiscoverPending must not use 'author:{...}' object literal; GitHub expects a String login")
	}
	// Must filter by PENDING state (the scope mechanism for viewer-only visibility).
	if !strings.Contains(graphqlDiscoverPending, "states:[PENDING]") {
		t.Error("graphqlDiscoverPending must include states:[PENDING] filter")
	}
	// Must be parameterized (not hardcoded repo values).
	for _, param := range []string{"$owner", "$name", "$prNumber"} {
		if !strings.Contains(graphqlDiscoverPending, param) {
			t.Errorf("graphqlDiscoverPending missing parameter %q", param)
		}
	}
}

// Integration: buildFindingsJSON returns empty string when there are no inline comments.
func TestBuildFindingsJSON_EmptyWhenNoComments_AC002(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "repo" {
				return `{"owner":{"login":"o"},"name":"r"}`, nil
			}
			return "[]", nil
		},
	}
	r := &Runner{executor: exec, repoRoot: t.TempDir()}
	setupRunnerCreds(t, r)

	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 99)
	writeReviewSubmittedEventWithID(t, logPath, "changes_requested", "PRR_round1")

	got, err := r.buildFindingsJSON(logPath)
	if err != nil {
		t.Fatalf("buildFindingsJSON: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for no inline comments, got %q", got)
	}
}
