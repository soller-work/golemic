package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/eventlog"
	"golemic/internal/prompt"
)

// ---------------------------------------------------------------------------
// Proof check 1 & 2: precheck computes fingerprints and writes event
// ---------------------------------------------------------------------------

// TestReviewerPrecheck_WritesEvent verifies that runReviewerPrecheck writes a
// reviewer_precheck event with exitCode, both fingerprints, and ok correctly set.
func TestReviewerPrecheck_WritesEvent(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	logPath := filepath.Join(homeDir, "events.jsonl")
	worktreePath := t.TempDir()

	r := newPrecheckRunner(t, homeDir, repoRoot)

	const beforeFP = "sha256:aaa"
	const afterFP = "sha256:aaa"
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		res := &reviewerPrecheckResult{
			OK: true, Command: "true", ExitCode: 0,
			BeforeFingerprint: beforeFP, AfterFingerprint: afterFP,
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}

	block, err := r.runReviewerPrecheck(worktreePath, logPath)
	if err != nil {
		t.Fatalf("runReviewerPrecheck returned error: %v", err)
	}
	if block == "" {
		t.Error("expected non-empty precheck block")
	}

	var reader eventlog.Reader
	events, readErr := reader.Read(logPath)
	if readErr != nil {
		t.Fatalf("read event log: %v", readErr)
	}
	payload := assertPrecheckEvent(t, events, "reviewer_precheck event not found in event log")
	if payload["exitCode"].(float64) != 0 {
		t.Errorf("exitCode: got %v, want 0", payload["exitCode"])
	}
	if !payload["ok"].(bool) {
		t.Error("ok: got false, want true")
	}
	if payload["beforeFingerprint"].(string) != beforeFP {
		t.Errorf("beforeFingerprint: got %q, want %q", payload["beforeFingerprint"], beforeFP)
	}
	if payload["afterFingerprint"].(string) != afterFP {
		t.Errorf("afterFingerprint: got %q, want %q", payload["afterFingerprint"], afterFP)
	}
}

// TestReviewerPrecheck_OkFalseOnFailedVerify verifies ok=false when exitCode != 0
// and the run still proceeds (no error returned).
func TestReviewerPrecheck_OkFalseOnFailedVerify(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	logPath := filepath.Join(homeDir, "events2.jsonl")
	worktreePath := t.TempDir()

	r := newPrecheckRunner(t, homeDir, repoRoot)
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		res := &reviewerPrecheckResult{
			OK: false, Command: "false", ExitCode: 1,
			Stdout:            "build error",
			BeforeFingerprint: "sha256:bbb", AfterFingerprint: "sha256:bbb",
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}

	block, err := r.runReviewerPrecheck(worktreePath, logPath)
	if err != nil {
		t.Fatalf("runReviewerPrecheck returned error: %v", err)
	}
	if !strings.Contains(block, "changes_requested") {
		t.Errorf("block for ok=false must contain 'changes_requested'; got: %s", block)
	}
	if !strings.Contains(block, "ok: false") {
		t.Errorf("block must contain 'ok: false'; got: %s", block)
	}

	var reader eventlog.Reader
	events, _ := reader.Read(logPath)
	payload := assertPrecheckEvent(t, events, "reviewer_precheck event not found")
	if payload["ok"].(bool) {
		t.Error("ok must be false for failed verify")
	}
	if payload["exitCode"].(float64) != 1 {
		t.Errorf("exitCode: got %v, want 1", payload["exitCode"])
	}
}

// TestReviewerPrecheck_OkFalseOnTreeMutation verifies ok=false when tree was mutated.
func TestReviewerPrecheck_OkFalseOnTreeMutation(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	logPath := filepath.Join(homeDir, "events3.jsonl")
	worktreePath := t.TempDir()

	r := newPrecheckRunner(t, homeDir, repoRoot)
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		res := &reviewerPrecheckResult{
			OK: false, Command: "touch /tmp/mutated", ExitCode: 0,
			BeforeFingerprint: "sha256:before", AfterFingerprint: "sha256:after",
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}

	block, err := r.runReviewerPrecheck(worktreePath, logPath)
	if err != nil {
		t.Fatalf("runReviewerPrecheck returned error: %v", err)
	}
	if !strings.Contains(block, "changes_requested") {
		t.Errorf("tree-mutating precheck must produce changes_requested instruction; got: %s", block)
	}
	if !strings.Contains(block, "tree-mutated: true") {
		t.Errorf("block must indicate tree-mutated: true; got: %s", block)
	}
}

// ---------------------------------------------------------------------------
// Proof check 4: large output truncation
// ---------------------------------------------------------------------------

// TestReviewerPrecheck_LargeOutputTruncated verifies oversized output is truncated
// to the header plus the last ≈8 KB with an explicit truncation marker.
func TestReviewerPrecheck_LargeOutputTruncated(t *testing.T) {
	large := strings.Repeat("x", precheckTailBytes+2000)

	res := &reviewerPrecheckResult{
		OK: false, Command: "big-cmd", ExitCode: 1,
		Stdout:            large,
		BeforeFingerprint: "sha256:fp1", AfterFingerprint: "sha256:fp1",
	}

	block := buildReviewerPrecheckBlock(res)

	if !strings.Contains(block, "bytes truncated") {
		t.Error("large output must include a truncation marker in the precheck block")
	}
	const maxAllowed = precheckTailBytes + 2*1024
	if len(block) > maxAllowed {
		t.Errorf("precheck block too large: %d bytes (max %d)", len(block), maxAllowed)
	}
}

// TestReviewerPrecheck_GreenNoOutputBlock verifies a green precheck produces only
// a short summary line without including stdout/stderr.
func TestReviewerPrecheck_GreenNoOutputBlock(t *testing.T) {
	res := &reviewerPrecheckResult{
		OK: true, Command: "go test ./...", ExitCode: 0,
		Stdout:            "lots of test output that should not appear",
		BeforeFingerprint: "sha256:fp", AfterFingerprint: "sha256:fp",
	}
	block := buildReviewerPrecheckBlock(res)
	if strings.Contains(block, "lots of test output") {
		t.Error("green precheck block must not include stdout/stderr body")
	}
	if !strings.Contains(block, "ok: true") {
		t.Error("green precheck block must contain 'ok: true'")
	}
	if strings.Contains(block, "changes_requested") {
		t.Error("green precheck block must not contain 'changes_requested' instruction")
	}
}

// ---------------------------------------------------------------------------
// Proof check 5: tool allowlist excludes edit, write, gm_project_check
// ---------------------------------------------------------------------------

// TestReviewerToolAllowlist_NoEditNoWrite verifies gmReviewerToolNames excludes
// edit, write, and gm_project_check, and includes gm_pr_view and gm_repo_tree.
func TestReviewerToolAllowlist_NoEditNoWrite(t *testing.T) {
	for _, banned := range []string{"edit", "write", "gm_project_check"} {
		if containsTool(gmReviewerToolNames, banned) {
			t.Errorf("gmReviewerToolNames must not include %q; got: %v", banned, gmReviewerToolNames)
		}
	}
	for _, required := range []string{"gm_pr_view", "gm_repo_tree"} {
		if !containsTool(gmReviewerToolNames, required) {
			t.Errorf("gmReviewerToolNames must include %q; got: %v", required, gmReviewerToolNames)
		}
	}
}

// ---------------------------------------------------------------------------
// Proof check 7: reviewer prompt uses gm_pr_view/gm_repo_tree, not verify/git diff
// ---------------------------------------------------------------------------

// TestReviewerPrompt_DiscoveryTools verifies the rendered reviewer prompt instructs
// gm_pr_view/gm_repo_tree/read for discovery and retains the CLI submit terminal step.
func TestReviewerPrompt_DiscoveryTools(t *testing.T) {
	_, repoRoot, _ := setupRunnerTest(t)
	guidelinesPath := filepath.Join(repoRoot, ".golemic", "guidelines", "reviewer.md")
	if err := os.MkdirAll(filepath.Dir(guidelinesPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guidelinesPath, []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	p, err := prompt.RenderReviewer(42, prompt.Issue{Number: 1, Title: "T"}, "my-unique-verify-9x3", guidelinesPath, false, "")
	if err != nil {
		t.Fatalf("RenderReviewer: %v", err)
	}

	for _, required := range []string{"gm_pr_view", "gm_repo_tree", "gm_slice_get"} {
		if !strings.Contains(p, required) {
			t.Errorf("reviewer prompt must contain %q", required)
		}
	}
	if strings.Contains(p, "my-unique-verify-9x3") {
		t.Error("reviewer prompt must not include the verify command as a runnable step")
	}
	if strings.Contains(p, "git diff") {
		t.Error("reviewer prompt must not instruct git diff")
	}
	if !strings.Contains(p, "golemic submit-review") {
		t.Error("reviewer prompt must retain 'golemic submit-review' terminal step")
	}
	if !strings.Contains(p, "golemic review-comment") {
		t.Error("reviewer prompt must retain 'golemic review-comment' step")
	}
}

// TestReviewerPrompt_PrecheckBlockInjected verifies the precheck block is injected
// into the reviewer prompt when provided.
func TestReviewerPrompt_PrecheckBlockInjected(t *testing.T) {
	_, repoRoot, _ := setupRunnerTest(t)
	guidelinesPath := filepath.Join(repoRoot, ".golemic", "guidelines", "reviewer.md")
	os.MkdirAll(filepath.Dir(guidelinesPath), 0755)           //nolint:errcheck
	os.WriteFile(guidelinesPath, []byte("# Guidelines"), 0644) //nolint:errcheck

	precheckBlock := "## Precheck Result\n\nok: false | command: `go test` | exitCode: 1 | tree-mutated: false\n"
	p, err := prompt.RenderReviewer(42, prompt.Issue{Number: 1, Title: "T"}, "go test", guidelinesPath, false, precheckBlock)
	if err != nil {
		t.Fatalf("RenderReviewer: %v", err)
	}
	if !strings.Contains(p, "## Precheck Result") {
		t.Error("reviewer prompt must contain the injected precheck block")
	}
	if !strings.Contains(p, "ok: false") {
		t.Error("reviewer prompt must contain precheck ok: false")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newPrecheckRunner creates a minimal Runner wired up for precheck unit tests.
func newPrecheckRunner(t *testing.T, homeDir, repoRoot string) *Runner {
	t.Helper()
	r := New(nil, homeDir, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = "testproject"
	r.runID = "r-pre-" + t.Name()
	if len(r.runID) > 50 {
		r.runID = r.runID[:50]
	}
	r.turnCounter = 1
	return r
}



// assertPrecheckEvent finds the reviewer_precheck event in events and returns its payload map.
func assertPrecheckEvent(t *testing.T, events []eventlog.Event, notFoundMsg string) map[string]interface{} {
	t.Helper()
	var found *eventlog.Event
	for i := range events {
		if events[i].Type == eventlog.EventReviewerPrecheck {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal(notFoundMsg)
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(found.Payload, &payload); err != nil {
		t.Fatalf("unmarshal precheck payload: %v", err)
	}
	return payload
}
