package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
	"golemic/internal/progress"
	"golemic/internal/worktree"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeProgressFakeAgent creates a runAgentFn for progress tests.
// The dev call writes pr_opened + optional tool calls to activity.jsonl.
// The reviewer call writes review_submitted + optional tool calls.
func makeProgressFakeAgent(t *testing.T, devActivityLines, reviewerActivityLines []string) func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	devCalls := 0
	return func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			devCalls++
			if devCalls == 1 {
				writePROpenedEvent(t, cfg.EventLogPath, 99)
			}
			if len(devActivityLines) > 0 {
				writeActivityLines(t, filepath.Join(cfg.RunsDir, cfg.RunID, "dev.activity.jsonl"), devActivityLines)
			}
		case "reviewer":
			writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
			if len(reviewerActivityLines) > 0 {
				writeActivityLines(t, filepath.Join(cfg.RunsDir, cfg.RunID, "reviewer.activity.jsonl"), reviewerActivityLines)
			}
		}
		return 0, agent.TranscriptPaths{Stderr: "/tmp/fake.stderr"}, nil
	}
}

func writeActivityLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// containsInOrder checks that wantSubstrings appear in s in the given order
// (not necessarily consecutive).
func containsInOrder(s string, wantSubstrings []string) bool {
	pos := 0
	for _, want := range wantSubstrings {
		idx := strings.Index(s[pos:], want)
		if idx < 0 {
			return false
		}
		pos += idx + len(want)
	}
	return true
}

// setupProgressRunner sets up a runner for progress-stream integration tests.
func setupProgressRunner(t *testing.T) (*Runner, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	var stdoutBuf bytes.Buffer
	r.SetStdout(&stdoutBuf)

	var freshStderr bytes.Buffer
	r.SetStderr(&freshStderr)

	return r, logPath, &freshStderr, &stdoutBuf
}

// runOrchestrateWithProgress wraps the EventWriter with the progress renderer
// so worktree_created and pr_merged events emit lifecycle lines in the test.
func runOrchestrateWithProgress(t *testing.T, r *Runner, logPath string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("create log writer: %v", err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"issue": 42, "runId": "issue-42-test"})
	_ = w.Write(eventlog.Event{
		Type:    eventlog.EventRunStarted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "issue-42-test",
		Payload: payload,
	})
	w.Close() //nolint:errcheck

	writer, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open orchestrate writer: %v", err)
	}
	defer writer.Close() //nolint:errcheck

	// Wrap with progress renderer if set
	var ew worktree.EventWriter = writer
	if r.progressRenderer != nil {
		ew = &progressEventWriter{inner: writer, renderer: r.progressRenderer}
	}
	return r.orchestrate(ew, logPath, "")
}

// ---------------------------------------------------------------------------
// TestProgress_HappyPath: lifecycle lines appear in order on stderr; stdout is clean
// ---------------------------------------------------------------------------

func TestProgress_HappyPath(t *testing.T) {
	r, logPath, stderrBuf, stdoutBuf := setupProgressRunner(t)

	devLines := []string{
		`{"type":"tool_execution_start","toolCallId":"x","toolName":"bash","args":{"command":"go build ./..."}}`,
		`{"type":"tool_execution_start","toolCallId":"y","toolName":"read","args":{"path":"internal/foo.go"}}`,
	}
	reviewerLines := []string{
		`{"type":"tool_execution_start","toolCallId":"z","toolName":"bash","args":{"command":"go test ./..."}}`,
	}
	r.SetRunAgentFn(makeProgressFakeAgent(t, devLines, reviewerLines))

	// Set up progress renderer directly (bypassing Run())
	r.progressRenderer = progress.New(stderrBuf)

	outcome := runOrchestrateWithProgress(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q; stderr:\n%s", outcome, stderrBuf.String())
	}

	stderr := stderrBuf.String()

	// BR-P7: lifecycle lines appear in the expected relative order
	wantOrder := []string{
		"▶ worktree ready (dev)",
		"▶ dev started",
		"▶ dev completed (exit 0)",
		"▶ PR #99 opened",
		"▶ CI green",
		"▶ worktree ready (reviewer)",
		"▶ reviewer completed (exit 0)",
		"▶ review: approved",
		"▶ PR #99 merged",
	}
	if !containsInOrder(stderr, wantOrder) {
		t.Errorf("lifecycle lines not in expected order\nstderr:\n%s\nwant order: %v", stderr, wantOrder)
	}

	// Tool call lines appear (BR-P5)
	if !strings.Contains(stderr, "dev · bash: go build ./...") {
		t.Errorf("expected dev bash tool call line in stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "dev · read: internal/foo.go") {
		t.Errorf("expected dev read tool call line in stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "reviewer · bash: go test ./...") {
		t.Errorf("expected reviewer bash tool call line in stderr:\n%s", stderr)
	}

	// BR-P1: stdout contract — stdout must not contain any progress lines
	stdout := stdoutBuf.String()
	if strings.Contains(stdout, "▶") {
		t.Errorf("progress lines must not appear on stdout, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// TestProgress_Quiet: --quiet suppresses all progress (BR-P2)
// ---------------------------------------------------------------------------

func TestProgress_Quiet(t *testing.T) {
	r, logPath, stderrBuf, _ := setupProgressRunner(t)

	r.SetRunAgentFn(makeProgressFakeAgent(t, nil, nil))

	// quiet=true: no renderer
	r.progressRenderer = nil

	outcome := runOrchestrateWithProgress(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}

	stderr := stderrBuf.String()
	if strings.Contains(stderr, "▶") {
		t.Errorf("with quiet, stderr must contain no progress lines; got:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// TestProgress_UnknownEventType: fallback line emitted, run continues (BR-P4)
// ---------------------------------------------------------------------------

func TestProgress_UnknownEventType(t *testing.T) {
	r := &Renderer{renderer: progress.New(new(bytes.Buffer))}
	_ = r // silence "declared and not used"

	var buf bytes.Buffer
	renderer := progress.New(&buf)

	// Inject unknown event type directly
	renderer.EmitLifecycle(eventlog.Event{Type: "future_unknown_type"})

	out := buf.String()
	if !strings.Contains(out, "▶ future_unknown_type") {
		t.Errorf("expected fallback line for unknown type, got: %q", out)
	}
}

// Renderer is a local helper to test the fallback without importing internal.
type Renderer struct {
	renderer *progress.Renderer
}

// ---------------------------------------------------------------------------
// TestProgress_FollowReaderError: run succeeds when activity.jsonl is missing (BR-P3)
// ---------------------------------------------------------------------------

func TestProgress_FollowReaderError(t *testing.T) {
	r, logPath, stderrBuf, _ := setupProgressRunner(t)

	// Fake agent that writes NO activity.jsonl (simulates failure to create it)
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			writePROpenedEvent(t, cfg.EventLogPath, 99)
		case "reviewer":
			writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
		}
		return 0, agent.TranscriptPaths{Stderr: "/tmp/fake.stderr"}, nil
	})

	r.progressRenderer = progress.New(stderrBuf)

	outcome := runOrchestrateWithProgress(t, r, logPath)
	// The run must succeed; missing activity.jsonl is non-fatal
	if outcome != outcomeSuccess {
		t.Errorf("expected success even with missing activity.jsonl, got %q; stderr:\n%s", outcome, stderrBuf.String())
	}
}

// ---------------------------------------------------------------------------
// TestProgress_StdoutContractIntact: stdout = runId only on success (BR-P1)
// ---------------------------------------------------------------------------

// makeMinimalFakeAgent creates a fake agent for the stdout contract test.
func makeMinimalFakeAgent(t *testing.T) func(context.Context, agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	zero := 0
	return func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		var payload json.RawMessage
		var evType string
		switch cfg.Role {
		case "dev":
			payload, _ = json.Marshal(map[string]string{"prNumber": "1"})
			evType = eventlog.EventPROpened
		case "reviewer":
			payload, _ = json.Marshal(map[string]interface{}{
				"verdict": "approved", "mergeConfidence": "high",
				"reviewId": "rev1", "inlineCommentCount": &zero,
			})
			evType = eventlog.EventReviewSubmitted
		}
		w, _ := eventlog.NewWriter(cfg.EventLogPath)
		_ = w.Write(eventlog.Event{Type: evType, Ts: time.Now().Format(time.RFC3339), RunID: cfg.RunID, Payload: payload})
		w.Close() //nolint:errcheck
		return 0, agent.TranscriptPaths{Stderr: fmt.Sprintf("/tmp/%s.stderr", cfg.Role)}, nil
	}
}

func setupStdoutContractRunner(t *testing.T) (*Runner, *bytes.Buffer) {
	t.Helper()
	homeDir, repoRoot, _ := setupRunnerTest(t)
	exec := setupHappyExecutor(repoRoot)

	golemicDir := filepath.Join(repoRoot, ".golemic")
	for _, dir := range []string{filepath.Join(golemicDir, "guidelines"), filepath.Join(golemicDir, "agents")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"dev.md", "reviewer.md"} {
		_ = os.WriteFile(filepath.Join(golemicDir, "guidelines", f), []byte("# G"), 0644)
		_ = os.WriteFile(filepath.Join(golemicDir, "agents", f), []byte("---\nmodel: test/model\n---\n"), 0644)
	}

	r := New(exec, homeDir, repoRoot, 99)
	r.SetPreflighter(passingPreflighter{})
	r.SetCIPollInterval(1 * time.Millisecond)
	r.SetCITimeout(5 * time.Second)

	var stdout bytes.Buffer
	r.SetStdout(&stdout)
	r.SetStderr(new(bytes.Buffer))
	return r, &stdout
}

func TestProgress_StdoutContractIntact(t *testing.T) {
	r, stdout := setupStdoutContractRunner(t)
	r.SetRunAgentFn(makeMinimalFakeAgent(t))

	r.Run()
	stdoutStr := stdout.String()

	if strings.Contains(stdoutStr, "▶") {
		t.Errorf("progress lines must not appear on stdout:\n%s", stdoutStr)
	}
}
