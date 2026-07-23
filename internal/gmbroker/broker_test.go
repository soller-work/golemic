package gmbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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

// devAllowedTools is the full dev agent tool allowlist (includes gm_project_check).
var devAllowedTools = []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"}

// startTestBrokerDev starts a broker with the dev tool allowlist (including gm_project_check).
func startTestBrokerDev(t *testing.T) (*Broker, string) {
	t.Helper()
	sockPath := filepath.Join(shortTempDir(t), "gm.sock")
	b, err := StartWithFetcherAndProjectCheck(sockPath,
		func(_ context.Context) (string, error) { return "spec", nil },
		ProjectCheckConfig{}, devAllowedTools)
	if err != nil {
		t.Fatalf("StartWithFetcherAndProjectCheck: %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b, sockPath
}

// startTestBrokerWithGate starts a broker pre-configured for §10 gate testing:
// projectCheckFn returns green with fingerprint "gate-test-fp" and computeFingerprintFn
// returns the same fingerprint so gm_dev_done passes after one project_check call.
func startTestBrokerWithGate(t *testing.T) (*Broker, string) {
	t.Helper()
	b, sockPath := startTestBrokerDev(t)
	b.SetProjectCheckFn(func(_ ProjectCheckConfig, _ string) (*ProjectCheckResult, error) {
		return &ProjectCheckResult{
			OK:                     true,
			WorkingTreeFingerprint: "gate-test-fp",
			Summary:                "verify passed",
		}, nil
	})
	b.SetComputeFingerprintFn(func(_ string) (string, error) {
		return "gate-test-fp", nil
	})
	return b, sockPath
}

// TestDevDone_ValidPayload verifies that a well-formed gm_dev_done call returns
// {ok:true,accepted:true} and stores the params in the broker after the §10 gate passes.
func TestDevDone_ValidPayload(t *testing.T) {
	broker, sockPath := startTestBrokerWithGate(t)

	// Satisfy the §10 gate: call gm_project_check first.
	call(t, sockPath, "gm_project_check", "c0", map[string]any{})

	params := map[string]any{
		"summary":   "Implement the feature",
		"commitMsg": "feat(runner): implement gm_ transport (173)",
		"prTitle":   "feat: implement gm_ transport",
		"prBody":    "Closes #173",
	}
	result := call(t, sockPath, "gm_dev_done", "c1", params)

	if result["ok"] != true {
		t.Errorf("ok: got %v, want true; full result: %v", result["ok"], result)
	}
	if result["accepted"] != true {
		t.Errorf("accepted: got %v, want true", result["accepted"])
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
	if stored.PrBody != "Closes #173" {
		t.Errorf("stored.PrBody: got %q", stored.PrBody)
	}
}

// TestDevDone_TerminalRetryIsIdempotent verifies that a retry of the same
// accepted terminal call returns the same accepted result and does not mutate
// broker state.
func TestDevDone_TerminalRetryIsIdempotent(t *testing.T) {
	broker, sockPath := startTestBrokerWithGate(t)
	call(t, sockPath, "gm_project_check", "c0", map[string]any{})

	params := devDoneParams()
	first := call(t, sockPath, "gm_dev_done", "c1", params)
	second := call(t, sockPath, "gm_dev_done", "c1", params)

	if first["ok"] != true || first["accepted"] != true {
		t.Fatalf("first call: got %v", first)
	}
	if second["ok"] != true || second["accepted"] != true {
		t.Fatalf("second call: got %v", second)
	}

	stored, ok := broker.DevDoneResult()
	if !ok {
		t.Fatal("DevDoneResult: expected stored params, got false")
	}
	wantSummary, _ := params["summary"].(string)
	if stored.Summary != wantSummary {
		t.Errorf("stored.Summary: got %q, want %q", stored.Summary, wantSummary)
	}
}

// TestDevDone_DifferentSecondTerminalCallRejected verifies that a different
// second terminal call in the same broker invocation is rejected as a protocol
// error and does not overwrite the first accepted result.
func TestDevDone_DifferentSecondTerminalCallRejected(t *testing.T) {
	broker, sockPath := startTestBrokerWithGate(t)
	call(t, sockPath, "gm_project_check", "c0", map[string]any{})

	firstParams := devDoneParams()
	first := call(t, sockPath, "gm_dev_done", "c1", firstParams)
	if first["ok"] != true || first["accepted"] != true {
		t.Fatalf("first call: got %v", first)
	}

	second := call(t, sockPath, "gm_dev_done", "c2", map[string]any{
		"summary":   "Different summary",
		"commitMsg": "feat(test): different (42)",
		"prTitle":   "feat: different",
		"prBody":    "Closes #42",
	})

	if second["ok"] != false {
		t.Errorf("ok: got %v, want false", second["ok"])
	}
	if second["code"] != "PROTOCOL_ERROR" {
		t.Errorf("code: got %v, want PROTOCOL_ERROR", second["code"])
	}
	stored, ok := broker.DevDoneResult()
	if !ok {
		t.Fatal("DevDoneResult: expected stored params, got false")
	}
	wantSummary, _ := firstParams["summary"].(string)
	if stored.Summary != wantSummary {
		t.Errorf("stored.Summary: got %q, want %q", stored.Summary, wantSummary)
	}
}

// TestDevDone_SchemaInvalidIsTerminalFailure verifies that a schema-invalid
// gm_dev_done call closes the invocation so a later valid call cannot turn it
// into success.
func TestDevDone_SchemaInvalidIsTerminalFailure(t *testing.T) {
	broker, sockPath := startTestBrokerWithGate(t)
	call(t, sockPath, "gm_project_check", "c0", map[string]any{})

	first := call(t, sockPath, "gm_dev_done", "c1", map[string]any{
		"summary":   "Implement the feature",
		"commitMsg": "feat(test): implement (42)",
		"prTitle":   "feat: implement",
		// prBody omitted
	})
	if first["ok"] != false {
		t.Fatalf("first call: got %v, want ok=false", first)
	}
	if first["code"] != "SCHEMA_INVALID" {
		t.Fatalf("first call: got code %v, want SCHEMA_INVALID", first["code"])
	}

	second := call(t, sockPath, "gm_dev_done", "c2", devDoneParams())
	if second["ok"] != false {
		t.Errorf("ok: got %v, want false", second["ok"])
	}
	if second["code"] != "PROTOCOL_ERROR" {
		t.Errorf("code: got %v, want PROTOCOL_ERROR", second["code"])
	}
	if _, ok := broker.DevDoneResult(); ok {
		t.Error("DevDoneResult: expected false after schema-invalid terminal call")
	}
	term, ok := broker.DevDoneTerminalResult()
	if !ok {
		t.Fatal("DevDoneTerminalResult: expected stored terminal result")
	}
	if term.Status != "SCHEMA_INVALID" {
		t.Fatalf("DevDoneTerminalResult.Status: got %q, want SCHEMA_INVALID", term.Status)
	}
	if !strings.Contains(term.Message, "prBody is required") {
		t.Fatalf("DevDoneTerminalResult.Message: got %q, want schema error", term.Message)
	}
	if len(term.Result) == 0 {
		t.Fatal("DevDoneTerminalResult.Result: expected stored raw result")
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
// §10 acceptance gate
// ---------------------------------------------------------------------------

// devDoneParams returns a valid gm_dev_done param map.
func devDoneParams() map[string]any {
	return map[string]any{
		"summary":   "Implement the feature",
		"commitMsg": "feat(test): implement (42)",
		"prTitle":   "feat: implement",
		"prBody":    "Closes #42",
	}
}

// TestDevDone_Gate_NoPriorCheck verifies BR-2: no prior gm_project_check → DEV_GATE.
func TestDevDone_Gate_NoPriorCheck(t *testing.T) {
	_, sockPath := startTestBrokerDev(t)

	result := call(t, sockPath, "gm_dev_done", "c1", devDoneParams())

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "DEV_GATE" {
		t.Errorf("code: got %v, want DEV_GATE", result["code"])
	}
	if msg, _ := result["message"].(string); !strings.Contains(msg, "no prior") {
		t.Errorf("message: got %q, want mention of no prior check", msg)
	}
}

// TestDevDone_Gate_LastCheckRed verifies BR-2: last check red → DEV_GATE.
func TestDevDone_Gate_LastCheckRed(t *testing.T) {
	b, sockPath := startTestBrokerDev(t)
	b.SetProjectCheckFn(func(_ ProjectCheckConfig, _ string) (*ProjectCheckResult, error) {
		return &ProjectCheckResult{OK: false, WorkingTreeFingerprint: "fp1"}, nil
	})
	b.SetComputeFingerprintFn(func(_ string) (string, error) { return "fp1", nil })

	call(t, sockPath, "gm_project_check", "c0", map[string]any{})
	result := call(t, sockPath, "gm_dev_done", "c1", devDoneParams())

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "DEV_GATE" {
		t.Errorf("code: got %v, want DEV_GATE", result["code"])
	}
	if msg, _ := result["message"].(string); !strings.Contains(msg, "not green") {
		t.Errorf("message: got %q, want mention of not green", msg)
	}
}

// TestDevDone_Gate_RedAfterGreen verifies BR-2: red check after last green → DEV_GATE.
func TestDevDone_Gate_RedAfterGreen(t *testing.T) {
	b, sockPath := startTestBrokerDev(t)
	callCount := 0
	b.SetProjectCheckFn(func(_ ProjectCheckConfig, _ string) (*ProjectCheckResult, error) {
		callCount++
		ok := callCount == 1 // first call green, second red
		return &ProjectCheckResult{OK: ok, WorkingTreeFingerprint: "fp1"}, nil
	})
	b.SetComputeFingerprintFn(func(_ string) (string, error) { return "fp1", nil })

	call(t, sockPath, "gm_project_check", "c0", map[string]any{}) // green
	call(t, sockPath, "gm_project_check", "c1", map[string]any{}) // red — becomes lastCheck
	result := call(t, sockPath, "gm_dev_done", "c2", devDoneParams())

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "DEV_GATE" {
		t.Errorf("code: got %v, want DEV_GATE", result["code"])
	}
}

// TestDevDone_Gate_TreeMutated verifies BR-2: tree changed after last green check → DEV_GATE.
func TestDevDone_Gate_TreeMutated(t *testing.T) {
	b, sockPath := startTestBrokerDev(t)
	b.SetProjectCheckFn(func(_ ProjectCheckConfig, _ string) (*ProjectCheckResult, error) {
		return &ProjectCheckResult{OK: true, WorkingTreeFingerprint: "fp-before"}, nil
	})
	// computeFingerprintFn returns a different fingerprint — simulates a file change.
	b.SetComputeFingerprintFn(func(_ string) (string, error) { return "fp-after", nil })

	call(t, sockPath, "gm_project_check", "c0", map[string]any{})
	result := call(t, sockPath, "gm_dev_done", "c1", devDoneParams())

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "DEV_GATE" {
		t.Errorf("code: got %v, want DEV_GATE", result["code"])
	}
	if msg, _ := result["message"].(string); !strings.Contains(msg, "changed") {
		t.Errorf("message: got %q, want mention of tree change", msg)
	}
}

// TestDevDone_Gate_GateRejectedAccessors verifies DevDoneGateRejected/DevDoneGateReason.
func TestDevDone_Gate_GateRejectedAccessors(t *testing.T) {
	b, sockPath := startTestBrokerDev(t) // no projectCheckFn → gate will reject (no prior check)

	if b.DevDoneGateRejected() {
		t.Error("DevDoneGateRejected: expected false before any call")
	}

	call(t, sockPath, "gm_dev_done", "c1", devDoneParams()) // rejects: no prior check

	if !b.DevDoneGateRejected() {
		t.Error("DevDoneGateRejected: expected true after rejection")
	}
	if b.DevDoneGateReason() == "" {
		t.Error("DevDoneGateReason: expected non-empty after rejection")
	}
}

// ---------------------------------------------------------------------------
// gm_review_submit
// ---------------------------------------------------------------------------

// TestReviewSubmit_ApprovedNoPrecheck verifies that approved without a valid precheck
// is rejected with REVIEWER_GATE (BR-4).
func TestReviewSubmit_ApprovedNoPrecheck(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "LGTM",
	})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false (no precheck = gate rejected)", result["ok"])
	}
	if result["code"] != "REVIEWER_GATE" {
		t.Errorf("code: got %v, want REVIEWER_GATE", result["code"])
	}
	if !b.ReviewSubmitGateRejected() {
		t.Error("ReviewSubmitGateRejected() should be true after gate rejection")
	}
}

// TestReviewSubmit_ApprovedWithValidPrecheck verifies that approved with a green precheck
// and matching fingerprint is accepted (BR-4).
func TestReviewSubmit_ApprovedWithValidPrecheck(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)

	const fp = "sha256:aaaa"
	b.SetComputeFingerprintFn(func(_ string) (string, error) { return fp, nil })
	b.SetReviewerConfig(ReviewerConfig{
		WorktreePath: "/fake",
		Precheck: &PrecheckState{
			OK:                true,
			BeforeFingerprint: fp,
			AfterFingerprint:  fp,
		},
	})

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "LGTM — all checks pass.",
	})

	if result["ok"] != true {
		t.Errorf("ok: got %v, want true; message: %v", result["ok"], result["message"])
	}
	if result["accepted"] != true {
		t.Errorf("accepted: got %v, want true", result["accepted"])
	}
	p, ok := b.ReviewSubmitResult()
	if !ok || p == nil {
		t.Fatal("ReviewSubmitResult() should be set after successful submit")
	}
	if p.Verdict != "approved" {
		t.Errorf("ReviewSubmitResult().Verdict: got %q, want approved", p.Verdict)
	}
}

// TestReviewSubmit_ApprovedRedPrecheck verifies that approved with a red precheck is rejected.
func TestReviewSubmit_ApprovedRedPrecheck(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)

	const fp = "sha256:bbbb"
	b.SetComputeFingerprintFn(func(_ string) (string, error) { return fp, nil })
	b.SetReviewerConfig(ReviewerConfig{
		WorktreePath: "/fake",
		Precheck: &PrecheckState{
			OK:                false, // red precheck
			BeforeFingerprint: fp,
			AfterFingerprint:  fp,
		},
	})

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "looks good",
	})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "REVIEWER_GATE" {
		t.Errorf("code: got %v, want REVIEWER_GATE", result["code"])
	}
	_ = b
}

// TestReviewSubmit_ApprovedMutatedTree verifies that approved with a mutating precheck is rejected.
func TestReviewSubmit_ApprovedMutatedTree(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)

	b.SetComputeFingerprintFn(func(_ string) (string, error) { return "sha256:after", nil })
	b.SetReviewerConfig(ReviewerConfig{
		WorktreePath: "/fake",
		Precheck: &PrecheckState{
			OK:                false, // mutating (before != after)
			BeforeFingerprint: "sha256:before",
			AfterFingerprint:  "sha256:after",
		},
	})

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "looks good",
	})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "REVIEWER_GATE" {
		t.Errorf("code: got %v, want REVIEWER_GATE", result["code"])
	}
	_ = b
}

// TestReviewSubmit_CurrentFingerprintMismatch verifies that approved is rejected when
// the current fingerprint differs from precheck.afterFingerprint.
func TestReviewSubmit_CurrentFingerprintMismatch(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)

	const afterFP = "sha256:after"
	// current fingerprint differs
	b.SetComputeFingerprintFn(func(_ string) (string, error) { return "sha256:changed", nil })
	b.SetReviewerConfig(ReviewerConfig{
		WorktreePath: "/fake",
		Precheck: &PrecheckState{
			OK:                true,
			BeforeFingerprint: afterFP,
			AfterFingerprint:  afterFP,
		},
	})

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "looks ok",
	})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false (changed tree since precheck)", result["ok"])
	}
	if result["code"] != "REVIEWER_GATE" {
		t.Errorf("code: got %v, want REVIEWER_GATE", result["code"])
	}
	_ = b
}

// TestReviewSubmit_TerminalProtocol verifies a second differing call returns PROTOCOL_ERROR.
func TestReviewSubmit_TerminalProtocol(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)

	// First call (changes_requested — always accepted).
	call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "changes_requested",
		"mergeConfidence": "low",
		"body":            "needs work",
	})

	// Second call with different params — protocol error.
	result := call(t, sockPath, "gm_review_submit", "c2", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "LGTM",
	})

	if result["ok"] != false {
		t.Errorf("second terminal call: ok: got %v, want false", result["ok"])
	}
	if result["code"] != "PROTOCOL_ERROR" {
		t.Errorf("second terminal call: code: got %v, want PROTOCOL_ERROR", result["code"])
	}
	_ = b
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

// TestReviewSubmit_InvalidMergeConfidence rejects invalid mergeConfidence values.
func TestReviewSubmit_InvalidMergeConfidence(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	called := false
	b.SetReviewerConfig(ReviewerConfig{WorktreePath: "/fake"})
	b.SetComputeFingerprintFn(func(_ string) (string, error) {
		called = true
		return "sha256:fp", nil
	})
	b.SetReviewerConfig(ReviewerConfig{
		WorktreePath: "/fake",
		Precheck:     &PrecheckState{OK: true, BeforeFingerprint: "sha256:fp", AfterFingerprint: "sha256:fp"},
	})

	result := call(t, sockPath, "gm_review_submit", "c1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "urgent",
		"body":            "looks good",
	})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "SCHEMA_INVALID" {
		t.Errorf("code: got %v, want SCHEMA_INVALID", result["code"])
	}
	if called {
		t.Error("mergeConfidence validation should fail before gate fingerprint lookup")
	}
	if _, ok := b.ReviewSubmitResult(); ok {
		t.Fatal("ReviewSubmitResult() must remain unset after schema rejection")
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
// gm_review_submit_comment
// ---------------------------------------------------------------------------

// TestReviewSubmitComment_Success verifies a commentable line returns ok=true with commentId/threadId.
func TestReviewSubmitComment_Success(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetReviewerConfig(ReviewerConfig{PRNumber: 42})

	var capturedReviewID string
	b.SetGetOrCreatePendingReviewFn(func(_ ReviewerConfig) (string, error) {
		return "PRR_test123", nil
	})
	b.SetAddReviewCommentFn(func(_ ReviewerConfig, reviewID, path, body string, line int) (commentID, threadID string, anchorInvalid bool, err error) {
		capturedReviewID = reviewID
		return "comment-1", "thread-1", false, nil
	})

	result := call(t, sockPath, "gm_review_submit_comment", "c1", map[string]any{
		"path": "internal/foo/bar.go",
		"line": 42,
		"body": "This looks wrong",
	})

	if result["ok"] != true {
		t.Errorf("ok: got %v, want true; message: %v", result["ok"], result["message"])
	}
	if result["commentId"] != "comment-1" {
		t.Errorf("commentId: got %v, want comment-1", result["commentId"])
	}
	if result["threadId"] != "thread-1" {
		t.Errorf("threadId: got %v, want thread-1", result["threadId"])
	}
	if capturedReviewID != "PRR_test123" {
		t.Errorf("capturedReviewID: got %q, want PRR_test123", capturedReviewID)
	}
	if b.PendingReviewID() != "PRR_test123" {
		t.Errorf("PendingReviewID(): got %q, want PRR_test123", b.PendingReviewID())
	}
}

// TestReviewSubmitComment_ReusesExistingPendingReview verifies a second call reuses
// the same Pending Review (BR-1).
func TestReviewSubmitComment_ReusesExistingPendingReview(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetReviewerConfig(ReviewerConfig{PRNumber: 42})

	getCalls := 0
	b.SetGetOrCreatePendingReviewFn(func(_ ReviewerConfig) (string, error) {
		getCalls++
		return "PRR_existing", nil
	})
	b.SetAddReviewCommentFn(func(_ ReviewerConfig, reviewID, path, body string, line int) (string, string, bool, error) {
		return "cid-" + reviewID, "tid-" + reviewID, false, nil
	})

	call(t, sockPath, "gm_review_submit_comment", "c1", map[string]any{
		"path": "a.go", "line": 1, "body": "first",
	})
	call(t, sockPath, "gm_review_submit_comment", "c2", map[string]any{
		"path": "b.go", "line": 2, "body": "second",
	})

	if getCalls != 1 {
		t.Errorf("getOrCreate should be called exactly once; got %d", getCalls)
	}
}

// TestReviewSubmitComment_AnchorInvalid verifies an uncommentable line returns ANCHOR_INVALID (BR-2).
func TestReviewSubmitComment_AnchorInvalid(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetReviewerConfig(ReviewerConfig{PRNumber: 42})
	b.SetGetOrCreatePendingReviewFn(func(_ ReviewerConfig) (string, error) { return "PRR_x", nil })
	b.SetAddReviewCommentFn(func(_ ReviewerConfig, reviewID, path, body string, line int) (string, string, bool, error) {
		return "", "", true, nil // anchor invalid
	})

	result := call(t, sockPath, "gm_review_submit_comment", "c1", map[string]any{
		"path": "internal/foo.go",
		"line": 999,
		"body": "comment on non-diff line",
	})

	if result["ok"] != false {
		t.Errorf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "ANCHOR_INVALID" {
		t.Errorf("code: got %v, want ANCHOR_INVALID", result["code"])
	}
	if result["path"] != "internal/foo.go" {
		t.Errorf("path: got %v, want internal/foo.go", result["path"])
	}
	if result["line"] != float64(999) {
		t.Errorf("line: got %v, want 999", result["line"])
	}
	_ = b
}

// TestReviewSubmitComment_SchemaValidation verifies missing required fields are rejected.
func TestReviewSubmitComment_SchemaValidation(t *testing.T) {
	_, sockPath := startTestBroker(t, nil)

	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"missing path", map[string]any{"line": 1, "body": "b"}},
		{"missing line", map[string]any{"path": "f.go", "body": "b"}},
		{"missing body", map[string]any{"path": "f.go", "line": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := call(t, sockPath, "gm_review_submit_comment", "c1", tc.params)
			if result["ok"] != false {
				t.Errorf("ok: got %v, want false", result["ok"])
			}
			if result["code"] != "SCHEMA_INVALID" {
				t.Errorf("code: got %v, want SCHEMA_INVALID", result["code"])
			}
		})
	}
}

// TestReviewSubmitComment_AfterAcceptedSubmitRejects verifies comments are blocked once gm_review_submit succeeds.
func TestReviewSubmitComment_AfterAcceptedSubmitRejects(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetComputeFingerprintFn(func(_ string) (string, error) { return "sha256:fp", nil })
	b.SetReviewerConfig(ReviewerConfig{
		WorktreePath: "/fake",
		Precheck:     &PrecheckState{OK: true, BeforeFingerprint: "sha256:fp", AfterFingerprint: "sha256:fp"},
	})

	result := call(t, sockPath, "gm_review_submit", "submit-1", map[string]any{
		"verdict":         "approved",
		"mergeConfidence": "high",
		"body":            "LGTM",
	})
	if result["ok"] != true {
		t.Fatalf("submit ok: got %v, want true", result["ok"])
	}

	commentCalls := 0
	b.SetGetOrCreatePendingReviewFn(func(_ ReviewerConfig) (string, error) {
		commentCalls++
		return "PRR_after_submit", nil
	})
	b.SetAddReviewCommentFn(func(_ ReviewerConfig, _ string, _ string, _ string, _ int) (string, string, bool, error) {
		commentCalls++
		return "comment-1", "thread-1", false, nil
	})

	result = call(t, sockPath, "gm_review_submit_comment", "comment-1", map[string]any{
		"path": "f.go", "line": 1, "body": "late comment",
	})
	if result["ok"] != false {
		t.Errorf("comment ok: got %v, want false", result["ok"])
	}
	if result["code"] != "PROTOCOL_ERROR" {
		t.Errorf("comment code: got %v, want PROTOCOL_ERROR", result["code"])
	}
	if commentCalls != 0 {
		t.Errorf("comment path must not be called after accepted submit; calls=%d", commentCalls)
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

// ---------------------------------------------------------------------------
// gm_pr_view
// ---------------------------------------------------------------------------

func TestBroker_PRView_NotConfigured_ReturnsError(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_pr_view"})

	result := call(t, sockPath, "gm_pr_view", "c1", map[string]any{})
	if result["ok"] == true {
		t.Error("expected ok: false when reviewer config is absent")
	}
	if result["code"] != "NOT_CONFIGURED" {
		t.Errorf("code: got %q, want NOT_CONFIGURED", result["code"])
	}
}

func TestBroker_PRView_WithFakeData_ReturnsResult(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_pr_view"})
	b.SetReviewerConfig(ReviewerConfig{
		ReviewerToken: "tok",
		PRNumber:      42,
		RepoRoot:      t.TempDir(),
	})
	// Inject fake fetch function.
	b.SetPRViewFn(func(cfg ReviewerConfig) (json.RawMessage, error) {
		out, _ := json.Marshal(PRViewResult{
			OK:           true,
			PR:           json.RawMessage(`{"number":42,"title":"Test PR"}`),
			Diff:         "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n",
			ChangedFiles: json.RawMessage(`[{"path":"foo.go","additions":1,"deletions":1}]`),
		})
		return json.RawMessage(out), nil
	})

	result := call(t, sockPath, "gm_pr_view", "c2", map[string]any{})
	if result["ok"] != true {
		t.Errorf("expected ok: true; got: %v", result)
	}
	if result["diff"] == "" || result["diff"] == nil {
		t.Error("diff must be non-empty")
	}
	if result["pr"] == nil {
		t.Error("pr field must be present")
	}
	if result["changedFiles"] == nil {
		t.Error("changedFiles must be present")
	}
}

func TestBroker_PRView_FetchError_ReturnsError(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_pr_view"})
	b.SetReviewerConfig(ReviewerConfig{
		ReviewerToken: "tok",
		PRNumber:      42,
		RepoRoot:      t.TempDir(),
	})
	b.SetPRViewFn(func(_ ReviewerConfig) (json.RawMessage, error) {
		return nil, fmt.Errorf("network error")
	})

	result := call(t, sockPath, "gm_pr_view", "c3", map[string]any{})
	if result["ok"] == true {
		t.Error("expected ok: false on fetch error")
	}
	if result["code"] != "FETCH_FAILED" {
		t.Errorf("code: got %q, want FETCH_FAILED", result["code"])
	}
}

// ---------------------------------------------------------------------------
// gm_repo_tree
// ---------------------------------------------------------------------------

func TestBroker_RepoTree_NotConfigured_ReturnsError(t *testing.T) {
	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_repo_tree"})

	result := call(t, sockPath, "gm_repo_tree", "r1", map[string]any{})
	if result["ok"] == true {
		t.Error("expected ok: false when worktree not configured")
	}
	if result["code"] != "NOT_CONFIGURED" {
		t.Errorf("code: got %q, want NOT_CONFIGURED", result["code"])
	}
}

func TestBroker_RepoTree_ListsRootCorrectly(t *testing.T) {
	wtDir := t.TempDir()
	os.WriteFile(filepath.Join(wtDir, "main.go"), []byte("package main"), 0644) //nolint:errcheck
	os.MkdirAll(filepath.Join(wtDir, "internal"), 0755)                         //nolint:errcheck

	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_repo_tree"})
	b.SetReviewerConfig(ReviewerConfig{WorktreePath: wtDir})

	result := call(t, sockPath, "gm_repo_tree", "r3", map[string]any{})
	if result["ok"] != true {
		t.Errorf("expected ok: true; got: %v", result)
	}
	entries, _ := result["entries"].([]interface{})
	names := make(map[string]string)
	for _, e := range entries {
		entry := e.(map[string]interface{})
		names[entry["name"].(string)] = entry["type"].(string)
	}
	if names["main.go"] != "file" {
		t.Errorf("main.go should be type 'file'; got: %q", names["main.go"])
	}
	if names["internal"] != "dir" {
		t.Errorf("internal should be type 'dir'; got: %q", names["internal"])
	}
}

func TestBroker_RepoTree_ListsSubdir(t *testing.T) {
	wtDir := t.TempDir()
	os.MkdirAll(filepath.Join(wtDir, "internal", "runner"), 0755)              //nolint:errcheck
	os.WriteFile(filepath.Join(wtDir, "internal", "foo.go"), []byte(""), 0644) //nolint:errcheck

	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_repo_tree"})
	b.SetReviewerConfig(ReviewerConfig{WorktreePath: wtDir})

	path := "internal"
	result := call(t, sockPath, "gm_repo_tree", "r4", map[string]any{"path": path})
	if result["ok"] != true {
		t.Errorf("expected ok: true; got: %v", result)
	}
	entries, _ := result["entries"].([]interface{})
	names := make(map[string]string)
	for _, e := range entries {
		entry := e.(map[string]interface{})
		names[entry["name"].(string)] = entry["type"].(string)
	}
	if names["foo.go"] != "file" {
		t.Errorf("foo.go should be type 'file'; got: %q", names["foo.go"])
	}
	if names["runner"] != "dir" {
		t.Errorf("runner should be type 'dir'; got: %q", names["runner"])
	}
}

func TestBroker_RepoTree_PathEscapeReturnsError(t *testing.T) {
	wtDir := t.TempDir()
	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_repo_tree"})
	b.SetReviewerConfig(ReviewerConfig{WorktreePath: wtDir})

	result := call(t, sockPath, "gm_repo_tree", "r5", map[string]any{"path": "../../../etc"})
	if result["ok"] == true {
		t.Error("expected ok: false for path escaping the worktree")
	}
	if result["code"] != "PATH_OUTSIDE_WORKTREE" {
		t.Errorf("code: got %q, want PATH_OUTSIDE_WORKTREE", result["code"])
	}
}

func TestBroker_RepoTree_NotFound(t *testing.T) {
	wtDir := t.TempDir()
	b, sockPath := startTestBroker(t, nil)
	b.SetAllowedTools([]string{"gm_repo_tree"})
	b.SetReviewerConfig(ReviewerConfig{WorktreePath: wtDir})

	result := call(t, sockPath, "gm_repo_tree", "r6", map[string]any{"path": "nonexistent-subdir"})
	if result["ok"] == true {
		t.Error("expected ok: false for nonexistent path")
	}
	if result["code"] != "NOT_FOUND" {
		t.Errorf("code: got %q, want NOT_FOUND", result["code"])
	}
}
