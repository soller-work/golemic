package gmbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// shortTempDir creates a short-named directory under os.TempDir to keep unix
// socket paths within the 104-byte limit on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gmb*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	return dir
}

func startTestBroker(t *testing.T, fetcher IssueFetcher) (*Broker, string) {
	t.Helper()
	sockPath := filepath.Join(shortTempDir(t), "gm.sock")
	b, err := StartWithFetcher(sockPath, fetcher)
	if err != nil {
		t.Fatalf("StartWithFetcher: %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b, sockPath
}

// call sends one gm_ tool request over the unix socket and returns the decoded result.
func call(t *testing.T, sockPath, tool, callID string, params any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	req := map[string]any{"tool": tool, "callId": callID, "params": json.RawMessage(raw)}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	enc, _ := json.Marshal(req)
	enc = append(enc, '\n')
	if _, err := conn.Write(enc); err != nil {
		t.Fatalf("write request: %v", err)
	}

	var resp struct {
		CallID string          `json:"callId"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CallID != callID {
		t.Errorf("callId: got %q, want %q", resp.CallID, callID)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// gm_slice_get
// ---------------------------------------------------------------------------

// TestSliceGet_ReturnsIssueBody verifies that gm_slice_get returns the issue
// Markdown body fetched by the runner's fetcher.
func TestSliceGet_ReturnsIssueBody(t *testing.T) {
	const wantSpec = "## Task\n\nImplement the feature."
	fetcher := func(_ context.Context) (string, error) { return wantSpec, nil }

	_, sockPath := startTestBroker(t, fetcher)

	result := call(t, sockPath, "gm_slice_get", "c1", map[string]any{})

	if result["ok"] != true {
		t.Errorf("ok: got %v, want true", result["ok"])
	}
	if result["spec"] != wantSpec {
		t.Errorf("spec: got %q, want %q", result["spec"], wantSpec)
	}
}

// TestSliceGet_FetchesForTokenlessCaller verifies that the broker invokes
// its fetcher on behalf of a caller that has no GH token — the caller only
// knows the socket path and the fetcher (broker side) holds the credential.
func TestSliceGet_FetchesForTokenlessCaller(t *testing.T) {
	const wantSpec = "spec body"
	fetchCalled := false
	fetcher := func(_ context.Context) (string, error) {
		fetchCalled = true
		return wantSpec, nil
	}
	_, sockPath := startTestBroker(t, fetcher)

	// The agent-side caller has no token — it only knows the socket path.
	result := call(t, sockPath, "gm_slice_get", "c1", map[string]any{})
	if result["ok"] != true {
		t.Fatalf("unexpected error result: %v", result)
	}
	if !fetchCalled {
		t.Error("fetcher was not called; expected broker to fetch on behalf of agent")
	}
}

// TestSliceGet_CachesWithinInvocation verifies that repeated calls within one
// Broker instance trigger at most one fetch.
func TestSliceGet_CachesWithinInvocation(t *testing.T) {
	var fetchCount atomic.Int32
	fetcher := func(_ context.Context) (string, error) {
		fetchCount.Add(1)
		return "cached body", nil
	}
	_, sockPath := startTestBroker(t, fetcher)

	for i := 0; i < 3; i++ {
		result := call(t, sockPath, "gm_slice_get", fmt.Sprintf("c%d", i), map[string]any{})
		if result["ok"] != true {
			t.Fatalf("call %d: unexpected error: %v", i, result)
		}
	}

	if got := fetchCount.Load(); got != 1 {
		t.Errorf("fetch count: got %d, want 1 (cache must suppress repeated fetches)", got)
	}
}

// TestSliceGet_FreshFetchPerInvocation verifies that a new Broker (new invocation)
// always re-fetches, so edits between invocations are picked up.
func TestSliceGet_FreshFetchPerInvocation(t *testing.T) {
	var fetchCount atomic.Int32
	fetcher := func(_ context.Context) (string, error) {
		fetchCount.Add(1)
		return fmt.Sprintf("body-v%d", fetchCount.Load()), nil
	}

	// First invocation.
	_, sock1 := startTestBroker(t, fetcher)
	r1 := call(t, sock1, "gm_slice_get", "c1", map[string]any{})
	if r1["spec"] != "body-v1" {
		t.Errorf("first invocation: got %q, want %q", r1["spec"], "body-v1")
	}

	// Second invocation — new Broker, new cache.
	_, sock2 := startTestBroker(t, fetcher)
	r2 := call(t, sock2, "gm_slice_get", "c1", map[string]any{})
	if r2["spec"] != "body-v2" {
		t.Errorf("second invocation: got %q, want %q", r2["spec"], "body-v2")
	}

	if got := fetchCount.Load(); got != 2 {
		t.Errorf("fetch count: got %d, want 2 (each invocation re-fetches)", got)
	}
}

// TestSliceGet_FetchError returns a structured error when the fetcher fails.
func TestSliceGet_FetchError(t *testing.T) {
	fetcher := func(_ context.Context) (string, error) {
		return "", fmt.Errorf("gh: not found")
	}
	_, sockPath := startTestBroker(t, fetcher)

	result := call(t, sockPath, "gm_slice_get", "c1", map[string]any{})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "FETCH_FAILED" {
		t.Errorf("code: got %v, want FETCH_FAILED", result["code"])
	}
}

// ---------------------------------------------------------------------------
// gm_dev_done
// ---------------------------------------------------------------------------

// TestDevDone_ValidPayload verifies that a well-formed gm_dev_done call returns
// a structured echo and stores params in the broker.
func TestDevDone_ValidPayload(t *testing.T) {
	broker, sockPath := startTestBroker(t, nil)

	params := map[string]any{
		"summary":   "Implement the feature",
		"commitMsg": "feat(runner): implement gm_ transport (173)",
		"prTitle":   "feat: implement gm_ transport",
		"prBody":    "Closes #173",
	}
	result := call(t, sockPath, "gm_dev_done", "c1", params)

	if result["ok"] != true {
		t.Errorf("ok: got %v, want true", result["ok"])
	}
	echo, _ := result["echo"].(map[string]any)
	if echo == nil {
		t.Fatalf("echo: expected object, got %v", result["echo"])
	}
	if echo["summary"] != params["summary"] {
		t.Errorf("echo.summary: got %v, want %v", echo["summary"], params["summary"])
	}
	if echo["commitMsg"] != params["commitMsg"] {
		t.Errorf("echo.commitMsg: got %v, want %v", echo["commitMsg"], params["commitMsg"])
	}
	if echo["prTitle"] != params["prTitle"] {
		t.Errorf("echo.prTitle: got %v, want %v", echo["prTitle"], params["prTitle"])
	}
	if echo["prBody"] != params["prBody"] {
		t.Errorf("echo.prBody: got %v, want %v", echo["prBody"], params["prBody"])
	}

	// Verify params are stored in the broker.
	stored, ok := broker.DevDoneResult()
	if !ok {
		t.Fatal("DevDoneResult: expected stored params, got false")
	}
	if stored.Summary != "Implement the feature" {
		t.Errorf("stored.Summary: got %q, want %q", stored.Summary, "Implement the feature")
	}
	if stored.PrTitle != "feat: implement gm_ transport" {
		t.Errorf("stored.PrTitle: got %q", stored.PrTitle)
	}
}

// TestDevDone_MissingField rejects a payload that omits required fields.
func TestDevDone_MissingField(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing commitMsg", map[string]any{"summary": "ok", "prTitle": "T", "prBody": "B"}},
		{"missing summary", map[string]any{"commitMsg": "fix: (1)", "prTitle": "T", "prBody": "B"}},
		{"missing prTitle", map[string]any{"summary": "ok", "commitMsg": "fix: (1)", "prBody": "B"}},
		{"missing prBody", map[string]any{"summary": "ok", "commitMsg": "fix: (1)", "prTitle": "T"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, sockPath := startTestBroker(t, nil)
			result := call(t, sockPath, "gm_dev_done", "c1", tc.params)
			if result["ok"] != false {
				t.Errorf("ok: got %v, want false", result["ok"])
			}
			if result["code"] != "SCHEMA_INVALID" {
				t.Errorf("code: got %v, want SCHEMA_INVALID", result["code"])
			}
		})
	}
}

// TestDevDone_NoFileSystemSideEffect verifies that a gm_dev_done call does not
// create files or any file-system mutations (params are stored in memory only).
func TestDevDone_NoFileSystemSideEffect(t *testing.T) {
	dir := t.TempDir()
	before, _ := os.ReadDir(dir)

	_, sockPath := startTestBroker(t, nil)
	call(t, sockPath, "gm_dev_done", "c1", map[string]any{
		"summary":   "done",
		"commitMsg": "feat: done (001)",
		"prTitle":   "feat: done",
		"prBody":    "Closes #1",
	})

	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Errorf("unexpected file system change: %d entries before, %d after", len(before), len(after))
	}
}

// TestDevDone_ResultAbsentBeforeCall verifies that DevDoneResult returns false
// before gm_dev_done has been called.
func TestDevDone_ResultAbsentBeforeCall(t *testing.T) {
	broker, _ := startTestBroker(t, nil)
	if _, ok := broker.DevDoneResult(); ok {
		t.Error("DevDoneResult: expected false before any gm_dev_done call")
	}
}

// ---------------------------------------------------------------------------
// gm_review_submit
// ---------------------------------------------------------------------------

// TestReviewSubmit_ValidPayload verifies that a well-formed gm_review_submit
// returns a structured echo with no side effects.
func TestReviewSubmit_ValidPayload(t *testing.T) {
	_, sockPath := startTestBroker(t, nil)

	params := map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "LGTM — all checks pass.",
	}
	result := call(t, sockPath, "gm_review_submit", "c1", params)

	if result["ok"] != true {
		t.Errorf("ok: got %v, want true", result["ok"])
	}
	echo, _ := result["echo"].(map[string]any)
	if echo == nil {
		t.Fatalf("echo: expected object, got %v", result["echo"])
	}
	if echo["verdict"] != "approved" {
		t.Errorf("echo.verdict: got %v, want approved", echo["verdict"])
	}
}

// TestReviewSubmit_InvalidVerdict rejects an unrecognised verdict value.
func TestReviewSubmit_InvalidVerdict(t *testing.T) {
	_, sockPath := startTestBroker(t, nil)

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "maybe",
		"mergeConfidence": "low",
		"body":            "not sure",
	})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "SCHEMA_INVALID" {
		t.Errorf("code: got %v, want SCHEMA_INVALID", result["code"])
	}
}

// TestReviewSubmit_ChangesRequested verifies that "changes_requested" is a valid verdict.
func TestReviewSubmit_ChangesRequested(t *testing.T) {
	_, sockPath := startTestBroker(t, nil)

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "changes_requested",
		"mergeConfidence": "low",
		"body":            "Please fix the lint errors.",
	})

	if result["ok"] != true {
		t.Errorf("ok: got %v, want true", result["ok"])
	}
}

// TestReviewSubmit_NoSideEffect confirms no file-system mutations occur.
func TestReviewSubmit_NoSideEffect(t *testing.T) {
	dir := t.TempDir()
	before, _ := os.ReadDir(dir)

	_, sockPath := startTestBroker(t, nil)
	call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "LGTM",
	})

	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Errorf("unexpected file system change: %d entries before, %d after", len(before), len(after))
	}
}

// ---------------------------------------------------------------------------
// Socket lifecycle
// ---------------------------------------------------------------------------

// TestBroker_SocketPermissions verifies the socket file has 0600 mode (owner
// only) so only the spawned agent subprocess can reach it.
func TestBroker_SocketPermissions(t *testing.T) {
	_, sockPath := startTestBroker(t, func(_ context.Context) (string, error) { return "", nil })

	fi, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("socket permissions: got %04o, want 0600", fi.Mode().Perm())
	}
}

// TestBroker_ShutdownRemovesSocket verifies that Shutdown cleans up the socket.
func TestBroker_ShutdownRemovesSocket(t *testing.T) {
	sockPath := filepath.Join(shortTempDir(t), "gm.sock")
	b, err := StartWithFetcher(sockPath, func(_ context.Context) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("StartWithFetcher: %v", err)
	}

	b.Shutdown()

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file still exists after Shutdown")
	}
}
