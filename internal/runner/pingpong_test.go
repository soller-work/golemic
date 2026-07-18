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
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/prompt"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writePROpenedEvent appends a pr_opened event to the event log.
func writePROpenedEvent(t *testing.T, logPath string, prNumber int) {
	t.Helper()
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer w.Close() //nolint:errcheck

	payload, _ := json.Marshal(map[string]string{"prNumber": fmt.Sprintf("%d", prNumber)})
	if err := w.Write(eventlog.Event{
		Type:    eventlog.EventPROpened,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "test-run",
		Payload: payload,
	}); err != nil {
		t.Fatalf("write pr_opened event: %v", err)
	}
}

// writeReviewEvent appends a review_submitted event with verdict and body.
func writeReviewEvent(t *testing.T, logPath, verdict, body string) {
	t.Helper()
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer w.Close() //nolint:errcheck

	payload, _ := json.Marshal(map[string]string{"verdict": verdict, "body": body, "mergeConfidence": "high"})
	if err := w.Write(eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "test-run",
		Payload: payload,
	}); err != nil {
		t.Fatalf("write review_submitted event: %v", err)
	}
}

// handleGitCmd responds to all git subcommands needed by worktree helpers.
func handleGitCmd(args []string) (string, error) {
	sub := ""
	if len(args) >= 3 && args[0] == "-C" {
		sub = args[2]
	} else if len(args) >= 1 {
		sub = args[0]
	}
	switch sub {
	case "fetch", "worktree", "config", "branch":
		return "", nil
	case "rev-parse":
		return "abc123\n", nil
	case "status":
		return "", nil // clean
	}
	return "", fmt.Errorf("not mocked: git %v", args)
}

// extractBodyArg returns the value of the --body flag from args, or empty string.
func extractBodyArg(args []string) string {
	for i, a := range args {
		if a == "--body" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// pingPongExecutor is a fakeExecutor that handles all worktree/git/gh commands
// needed by orchestrate, and records gh pr comment calls.
func pingPongExecutor(commentFails bool, commentCalls *[]string) *fakeExecutor {
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" {
				return handleGitCmd(args)
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name != "gh" {
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			}
			if len(args) >= 1 && args[0] == "issue" {
				return `{"title":"T","body":"B"}`, nil
			}
			if len(args) < 2 || args[0] != "pr" {
				return "[]", nil
			}
			if args[1] != "comment" {
				return "[]", nil
			}
			body := extractBodyArg(args)
			if commentCalls != nil {
				*commentCalls = append(*commentCalls, body)
			}
			if commentFails {
				return "", fmt.Errorf("gh pr comment failed")
			}
			return "", nil
		},
	}
}

// setupPingPongRunner creates a minimal Runner configured for orchestrate unit tests.
func setupPingPongRunner(t *testing.T, exec *fakeExecutor) (*Runner, string, *bytes.Buffer) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	// Write guidelines so RenderDev, RenderDevRetry, and RenderReviewer can read them
	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "reviewer.md"), []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	r := New(exec, homeDir, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = project
	r.runID = "issue-42-test"
	r.branchName = "golemic/issue-42"
	r.creds = creds
	r.cfg = &config.Config{
		VerifyCommand: "go test",
		TimeoutMinutes: 30,
		Models:         config.Models{Dev: "claude-3-5-sonnet-20241022", Reviewer: "claude-3-5-sonnet-20241022"},
	}
	r.issue = &issueData{Number: 42, Title: "Test Issue", Body: "Test body"}

	var stderr bytes.Buffer
	r.SetStderr(&stderr)

	return r, filepath.Join(homeDir, ".golemic", project, "runs", "issue-42-test", "events.jsonl"), &stderr
}

// makeOrchestrateFakeAgent returns a runAgentFn that simulates agent behavior.
// agentRounds is a sequence: each entry describes one agent call in order.
type agentRoundConfig struct {
	role      string // "dev" or "reviewer"
	verdict   string // for reviewer: verdict to write
	body      string // for reviewer: body to write
	exitCode  int    // non-zero = failure
	doTimeout bool   // simulate agent.ErrTimeout
}

// capturedPrompts records the UserPrompt passed to each dev call.
type promptCapture struct {
	devPrompts []string
}

// writeAgentEvents writes the expected event-log side-effects for a fake agent call.
func writeAgentEvents(t *testing.T, cfg agent.RoleConfig, round agentRoundConfig, isFirstDevCall bool) {
	t.Helper()
	switch cfg.Role {
	case "dev":
		if isFirstDevCall {
			writePROpenedEvent(t, cfg.EventLogPath, 99)
		}
	case "reviewer":
		if round.verdict != "" {
			writeReviewEvent(t, cfg.EventLogPath, round.verdict, round.body)
		}
	}
}

func makeOrchestrateFakeAgent(t *testing.T, rounds []agentRoundConfig, capture *promptCapture) func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	callIdx := 0
	devCallCount := 0
	return func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if callIdx >= len(rounds) {
			t.Errorf("unexpected agent call #%d (only %d configured)", callIdx+1, len(rounds))
			return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected call")
		}
		round := rounds[callIdx]
		callIdx++

		if round.role != "" && cfg.Role != round.role {
			t.Errorf("agent call #%d: expected role %q, got %q", callIdx, round.role, cfg.Role)
		}
		if cfg.Role == "dev" && capture != nil {
			capture.devPrompts = append(capture.devPrompts, cfg.UserPrompt)
		}
		if round.doTimeout {
			return 0, agent.TranscriptPaths{}, agent.ErrTimeout
		}
		isFirst := cfg.Role == "dev" && devCallCount == 0
		if cfg.Role == "dev" {
			devCallCount++
		}
		writeAgentEvents(t, cfg, round, isFirst)
		return round.exitCode, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
	}
}

func runOrchestrate(t *testing.T, r *Runner, logPath string) string {
	t.Helper()
	// Ensure the log directory exists
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Write run_started so the log file exists
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

	// Reopen for orchestrate's writer
	writer, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open orchestrate writer: %v", err)
	}
	defer writer.Close() //nolint:errcheck

	return r.orchestrate(writer, logPath)
}

// ---------------------------------------------------------------------------
// AC-001: Approved in round 1 ends as success
// ---------------------------------------------------------------------------

func TestPingPong_ApprovedRound1_AC001(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if len(commentCalls) != 0 {
		t.Errorf("no escalation comment expected, got %d", len(commentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC-002: Changes requested then approved in round 2 ends as success
// ---------------------------------------------------------------------------

func TestPingPong_ChangesRequestedThenApproved_AC002(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)
	capture := &promptCapture{}

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix the typo in README", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM now", exitCode: 0},
	}, capture))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	// Dev was re-invoked once (2 total dev calls)
	if len(capture.devPrompts) != 2 {
		t.Errorf("expected 2 dev calls, got %d", len(capture.devPrompts))
	}
	// Second dev prompt must contain verbatim findings
	if !strings.Contains(capture.devPrompts[1], "Fix the typo in README") {
		t.Errorf("dev retry prompt must contain verbatim findings, got: %s", capture.devPrompts[1])
	}
	if len(commentCalls) != 0 {
		t.Errorf("no escalation comment expected, got %d", len(commentCalls))
	}
}

// assertEscalationComment checks that the comment contains required fields (BR-007).
func assertEscalationComment(t *testing.T, comment string, issueNum, prNum, roundCount int) {
	t.Helper()
	for _, want := range []string{fmt.Sprintf("%d", issueNum), fmt.Sprintf("%d", prNum), fmt.Sprintf("%d", roundCount)} {
		if !strings.Contains(comment, want) {
			t.Errorf("escalation comment missing %q, got: %s", want, comment)
		}
	}
	if !strings.Contains(comment, "No merge") && !strings.Contains(comment, "no merge") {
		t.Errorf("escalation comment must state no merge happened, got: %s", comment)
	}
	if !strings.Contains(comment, "Human review") && !strings.Contains(comment, "human review") {
		t.Errorf("escalation comment must require human review, got: %s", comment)
	}
}

// ---------------------------------------------------------------------------
// AC-003: Three unsatisfied rounds escalate with PR comment
// ---------------------------------------------------------------------------

func TestPingPong_ThreeChangesRequestedEscalates_AC003(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)
	capture := &promptCapture{}

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix A", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix B", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix C", exitCode: 0},
	}, capture))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeEscalated {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeEscalated)
	}
	if len(capture.devPrompts) != 3 {
		t.Errorf("expected 3 dev calls (1 initial + 2 retries), got %d", len(capture.devPrompts))
	}
	if len(commentCalls) != 1 {
		t.Errorf("expected 1 escalation comment, got %d", len(commentCalls))
		return
	}
	assertEscalationComment(t, commentCalls[0], 42, 99, 3)
}

// ---------------------------------------------------------------------------
// AC-004: Dev failure inside a retry round terminates as dev_failed
// ---------------------------------------------------------------------------

func TestPingPong_DevFailureInRetryRound_AC004(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "fix this", exitCode: 0},
		{role: "dev", exitCode: 1}, // dev retry fails
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeDevFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeDevFailed)
	}
	if len(commentCalls) != 0 {
		t.Errorf("no escalation comment expected on dev failure, got %d", len(commentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC-005: Reviewer timeout inside a retry round terminates as timeout
// ---------------------------------------------------------------------------

func TestPingPong_ReviewerTimeoutInRetryRound_AC005(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "fix this", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", doTimeout: true}, // reviewer times out in round 2
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeTimeout {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeTimeout)
	}
	if len(commentCalls) != 0 {
		t.Errorf("no escalation comment expected on reviewer timeout, got %d", len(commentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC-006: Failed escalation comment still terminates escalated
// ---------------------------------------------------------------------------

func TestPingPong_CommentFailureStillEscalated_AC006(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(true, &commentCalls) // comment fails

	r, logPath, stderr := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix A", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix B", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix C", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeEscalated {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeEscalated)
	}
	// Comment was attempted but failed; error should be logged
	if !strings.Contains(stderr.String(), "Warning") {
		t.Errorf("stderr should contain warning about comment failure, got: %s", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC-007: Empty findings body terminates review_failed
// ---------------------------------------------------------------------------

func TestPingPong_EmptyFindings_AC007(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "", exitCode: 0}, // empty body
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeReviewFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeReviewFailed)
	}
	if len(commentCalls) != 0 {
		t.Errorf("no escalation comment expected on empty findings, got %d", len(commentCalls))
	}
}

// ---------------------------------------------------------------------------
// RenderDevRetry verbatim findings embedding
// ---------------------------------------------------------------------------

func TestRenderDevRetry_VerbatimFindings(t *testing.T) {
	dir := t.TempDir()
	guidelinesPath := filepath.Join(dir, "dev.md")
	if err := os.WriteFile(guidelinesPath, []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	findings := "Fix the null pointer dereference in handler.go line 42"
	p, err := prompt.RenderDevRetry(findings, prompt.Issue{Number: 42, Title: "T", Body: "B"}, "golemic/issue-42", "go test", guidelinesPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(p, findings) {
		t.Errorf("dev retry prompt must contain verbatim findings %q, got: %s", findings, p)
	}
}

func TestRenderDevRetry_EmptyFindingsError(t *testing.T) {
	dir := t.TempDir()
	guidelinesPath := filepath.Join(dir, "dev.md")
	if err := os.WriteFile(guidelinesPath, []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := prompt.RenderDevRetry("", prompt.Issue{Number: 42, Title: "T", Body: "B"}, "golemic/issue-42", "go test", guidelinesPath)
	if err == nil {
		t.Fatal("expected EMPTY_FINDINGS error, got nil")
	}
	if !strings.Contains(err.Error(), "EMPTY_FINDINGS") {
		t.Errorf("expected EMPTY_FINDINGS in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Escalation comment is deterministic (BR-007)
// ---------------------------------------------------------------------------

func TestPostEscalationComment_Deterministic(t *testing.T) {
	var calls1, calls2 []string
	exec1 := pingPongExecutor(false, &calls1)
	exec2 := pingPongExecutor(false, &calls2)

	homeDir, repoRoot, project := setupRunnerTest(t)
	creds := loadTestCreds(t, homeDir, project)

	makeRunner := func(exec *fakeExecutor) *Runner {
		r := &Runner{
			executor:   exec,
			repoRoot:   repoRoot,
			homeDir:    homeDir,
			project:    project,
			issueNum:   42,
			runID:      "issue-42-det",
			branchName: "golemic/issue-42",
			creds:      creds,
			issue:      &issueData{Number: 42, Title: "T", Body: "B"},
			cfg:        &config.Config{},
		}
		var buf bytes.Buffer
		r.SetStderr(&buf)
		return r
	}

	r1 := makeRunner(exec1)
	r1.postEscalationComment(99, 3)

	r2 := makeRunner(exec2)
	r2.postEscalationComment(99, 3)

	if len(calls1) != 1 || len(calls2) != 1 {
		t.Fatalf("expected 1 comment each, got %d and %d", len(calls1), len(calls2))
	}
	if calls1[0] != calls2[0] {
		t.Errorf("escalation comment is not deterministic:\n  run1: %s\n  run2: %s", calls1[0], calls2[0])
	}
}

// ---------------------------------------------------------------------------
// countReviewSubmittedEvents and latestReviewBody helpers
// ---------------------------------------------------------------------------

func TestCountReviewSubmittedEvents(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)
	if count := r.countReviewSubmittedEvents(logPath); count != 0 {
		t.Errorf("empty log: got %d, want 0", count)
	}

	writeReviewSubmittedEvent(t, logPath, "approved")
	if count := r.countReviewSubmittedEvents(logPath); count != 1 {
		t.Errorf("after 1 event: got %d, want 1", count)
	}

	writeReviewSubmittedEvent(t, logPath, "changes_requested")
	writeReviewSubmittedEvent(t, logPath, "changes_requested")
	if count := r.countReviewSubmittedEvents(logPath); count != 3 {
		t.Errorf("after 3 events: got %d, want 3", count)
	}
}

func TestLatestReviewBody(t *testing.T) {
	r := &Runner{}
	logPath := newLogPath(t)

	writeReviewEvent(t, logPath, "changes_requested", "first finding")
	writeReviewEvent(t, logPath, "changes_requested", "second finding")

	body, err := r.latestReviewBody(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "second finding" {
		t.Errorf("expected %q, got %q", "second finding", body)
	}
}
