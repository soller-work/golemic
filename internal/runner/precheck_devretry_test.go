package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"golemic/internal/config"
	"golemic/internal/eventlog"
	"golemic/internal/telemetry"
)

// ---------------------------------------------------------------------------
// AC-P1/P2: precheck !ok — verify-red class routes to dev-retry, reviewer LLM skipped
// ---------------------------------------------------------------------------

func TestPrecheckNotOk_VerifyRed_SkipsReviewerAndDrivesDevRetry(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)
	capture := &promptCapture{}

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.cfg.MaxReviewRounds = 5

	// Precheck: verify exits non-zero on first round, ok on second.
	var precheckCalls int
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		precheckCalls++
		if precheckCalls == 1 {
			res := &reviewerPrecheckResult{
				OK: false, Command: "go test ./...", ExitCode: 1,
				Stdout:            "build error output",
				BeforeFingerprint: "sha:aaa", AfterFingerprint: "sha:aaa",
			}
			writeReviewerPrecheckEvent(r, evLogPath, res)
			return buildReviewerPrecheckBlock(res), nil
		}
		res := &reviewerPrecheckResult{
			OK: true, Command: "go test ./...", ExitCode: 0,
			BeforeFingerprint: "sha:aaa", AfterFingerprint: "sha:aaa",
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}

	// Agent sequence: dev (initial), dev-retry from precheck, reviewer (approved).
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "dev", exitCode: 0}, // dev-retry driven by precheck !ok
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, capture))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	// 2 dev calls: initial + precheck-driven retry
	if len(capture.devPrompts) != 2 {
		t.Errorf("expected 2 dev calls, got %d", len(capture.devPrompts))
	}
	// Dev-retry prompt must encode the verify-failure class and output
	retryPrompt := capture.devPrompts[1]
	if !strings.Contains(retryPrompt, "Reviewer Precheck Failed") {
		t.Errorf("dev-retry prompt must contain failure heading; got: %s", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "exit code") || !strings.Contains(retryPrompt, "1") {
		t.Errorf("dev-retry prompt must encode exit code; got: %s", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "go test ./...") {
		t.Errorf("dev-retry prompt must name the verify command; got: %s", retryPrompt)
	}
	if !strings.Contains(retryPrompt, "build error output") {
		t.Errorf("dev-retry prompt must include verify output tail; got: %s", retryPrompt)
	}
}

// ---------------------------------------------------------------------------
// AC-P2: precheck !ok — tree-mutated class routes to dev-retry with correct findings
// ---------------------------------------------------------------------------

func TestPrecheckNotOk_TreeMutated_SkipsReviewerAndDrivesDevRetry(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)
	capture := &promptCapture{}

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.cfg.MaxReviewRounds = 5

	var precheckCalls int
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		precheckCalls++
		if precheckCalls == 1 {
			res := &reviewerPrecheckResult{
				OK: false, Command: "go test ./...", ExitCode: 0,
				BeforeFingerprint: "sha:before", AfterFingerprint: "sha:after",
			}
			writeReviewerPrecheckEvent(r, evLogPath, res)
			return buildReviewerPrecheckBlock(res), nil
		}
		res := &reviewerPrecheckResult{
			OK: true, Command: "go test ./...", ExitCode: 0,
			BeforeFingerprint: "sha:stable", AfterFingerprint: "sha:stable",
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "dev", exitCode: 0}, // dev-retry from precheck tree-mutation
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, capture))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if len(capture.devPrompts) != 2 {
		t.Errorf("expected 2 dev calls, got %d", len(capture.devPrompts))
	}
	// Dev-retry prompt must name the tree-mutation failure class
	retryPrompt := capture.devPrompts[1]
	if !strings.Contains(retryPrompt, "mutated") {
		t.Errorf("dev-retry prompt must encode tree-mutation failure class; got: %s", retryPrompt)
	}
}

// ---------------------------------------------------------------------------
// AC-P3: precheck !ok + rounds exhausted → escalate, no dev-retry
// ---------------------------------------------------------------------------

func TestPrecheckNotOk_RoundsExhausted_Escalates(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.cfg.MaxReviewRounds = 1

	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		res := &reviewerPrecheckResult{
			OK: false, Command: "go test", ExitCode: 1,
			BeforeFingerprint: "sha:x", AfterFingerprint: "sha:x",
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}

	// Only dev (initial) — no dev-retry since MaxReviewRounds=1 means escalate immediately
	capture := &promptCapture{}
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
	}, capture))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeEscalated {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeEscalated)
	}
	// No dev-retry: only 1 dev call (the initial one)
	if len(capture.devPrompts) != 1 {
		t.Errorf("expected 1 dev call (no retry on exhaustion), got %d", len(capture.devPrompts))
	}
	// Escalation comment must have been posted
	if len(commentCalls) != 1 {
		t.Errorf("expected 1 escalation comment, got %d", len(commentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC-P4 (BR-P5): green precheck → reviewer LLM runs unchanged, no dev-retry
// ---------------------------------------------------------------------------

func TestPrecheckGreen_ReviewerRunsUnchanged(t *testing.T) {
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)
	capture := &promptCapture{}

	r, logPath, _ := setupPingPongRunner(t, exec)
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		res := &reviewerPrecheckResult{
			OK: true, Command: "go test", ExitCode: 0,
			BeforeFingerprint: "sha:stable", AfterFingerprint: "sha:stable",
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, capture))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	// Only 1 dev call (initial, no dev-retry)
	if len(capture.devPrompts) != 1 {
		t.Errorf("expected 1 dev call (no retry on green precheck), got %d", len(capture.devPrompts))
	}
	if len(commentCalls) != 0 {
		t.Errorf("expected no escalation comment, got %d", len(commentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC-P5: precheck run emits durationMs in event + SpanReviewerPrecheck span
// ---------------------------------------------------------------------------

func TestPrecheckImpl_EmitsDurationMsAndSpan(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	logPath := newLogPath(t)

	sink := &recordingSink{}
	r := newPrecheckRunner(t, homeDir, repoRoot)
	r.sink = sink
	r.cfg = &config.Config{VerifyCommand: ""}
	r.executor = &fakeExecutor{
		runFunc: func(_ string, _ ...string) (string, error) { return "", nil },
	}

	_, result, err := runReviewerPrecheckImpl(r, t.TempDir(), logPath, "")
	if err != nil {
		t.Fatalf("runReviewerPrecheckImpl: %v", err)
	}

	if result.DurationMs < 0 {
		t.Errorf("DurationMs must be >= 0, got %d", result.DurationMs)
	}
	assertPrecheckEventHasDurationMs(t, logPath)
	assertSpanReviewerPrecheckEmitted(t, sink)
}

func assertPrecheckEventHasDurationMs(t *testing.T, logPath string) {
	t.Helper()
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	payload := assertPrecheckEvent(t, events, "reviewer_precheck event not found")
	if _, ok := payload["durationMs"]; !ok {
		t.Error("reviewer_precheck event payload must contain durationMs field")
	}
}

func assertSpanReviewerPrecheckEmitted(t *testing.T, sink *recordingSink) {
	t.Helper()
	records := sink.all()
	foundStart := false
	foundEnd := false
	for _, rec := range records {
		if rec.Kind == telemetry.KindSpanStart && rec.Name == telemetry.SpanReviewerPrecheck {
			foundStart = true
		}
		if rec.Kind == telemetry.KindSpanEnd && rec.DurationMS != nil && *rec.DurationMS >= 0 {
			foundEnd = true
		}
	}
	if !foundStart {
		t.Error("SpanReviewerPrecheck span.start not emitted to telemetry sink")
	}
	if !foundEnd {
		t.Error("SpanReviewerPrecheck span.end with durationMs not emitted to telemetry sink")
	}
}

// ---------------------------------------------------------------------------
// AC-P6: buildPrecheckFindings encodes failure class and output tail
// ---------------------------------------------------------------------------

func TestBuildPrecheckFindings_VerifyRed(t *testing.T) {
	res := &reviewerPrecheckResult{
		OK: false, Command: "go test ./...", ExitCode: 2,
		Stdout:            "FAIL: some test failed",
		BeforeFingerprint: "sha:a", AfterFingerprint: "sha:a",
	}
	findings := buildPrecheckFindings(res)
	if !strings.Contains(findings, "exit code") || !strings.Contains(findings, "2") {
		t.Errorf("findings must encode exit code 2; got: %s", findings)
	}
	if !strings.Contains(findings, "go test ./...") {
		t.Errorf("findings must name the command; got: %s", findings)
	}
	if !strings.Contains(findings, "FAIL: some test failed") {
		t.Errorf("findings must include output tail; got: %s", findings)
	}
}

func TestBuildPrecheckFindings_TreeMutated(t *testing.T) {
	res := &reviewerPrecheckResult{
		OK: false, Command: "go fmt ./...", ExitCode: 0,
		Stdout:            "formatted main.go",
		BeforeFingerprint: "sha:before", AfterFingerprint: "sha:after",
	}
	findings := buildPrecheckFindings(res)
	if !strings.Contains(findings, "mutated") {
		t.Errorf("findings must encode tree-mutation failure class; got: %s", findings)
	}
	if !strings.Contains(findings, "go fmt ./...") {
		t.Errorf("findings must name the command; got: %s", findings)
	}
}

// ---------------------------------------------------------------------------
// AC-P7: precheck !ok round writes a review_submitted(changes_requested) event
// ---------------------------------------------------------------------------

func TestPrecheckNotOk_WritesReviewSubmittedEvent(t *testing.T) {
	r, logPath, _ := runPrecheckNotOkOrchestrate(t)
	_ = r
	assertPrecheckReviewSubmittedEvent(t, logPath)
}

// runPrecheckNotOkOrchestrate runs orchestrate with a first-round !ok precheck
// followed by a green precheck + approved reviewer. Returns the runner and log path.
func runPrecheckNotOkOrchestrate(t *testing.T) (*Runner, string, *[]string) {
	t.Helper()
	var commentCalls []string
	exec := pingPongExecutor(false, &commentCalls)
	r, logPath, _ := setupPingPongRunner(t, exec)
	r.cfg.MaxReviewRounds = 5

	var precheckCalls int
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		precheckCalls++
		ok := precheckCalls > 1
		exitCode := 0
		if !ok {
			exitCode = 1
		}
		res := &reviewerPrecheckResult{
			OK: ok, Command: "go test", ExitCode: exitCode,
			BeforeFingerprint: "sha:x", AfterFingerprint: "sha:x",
		}
		writeReviewerPrecheckEvent(r, evLogPath, res)
		return buildReviewerPrecheckBlock(res), nil
	}
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))
	if outcome := runOrchestrate(t, r, logPath); outcome != outcomeSuccess {
		t.Fatalf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	return r, logPath, &commentCalls
}

// assertPrecheckReviewSubmittedEvent checks the event log contains a synthetic
// review_submitted(changes_requested, source=precheck) event.
func assertPrecheckReviewSubmittedEvent(t *testing.T, logPath string) {
	t.Helper()
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	for _, ev := range events {
		if ev.Type != eventlog.EventReviewSubmitted {
			continue
		}
		var p struct {
			Source  string `json:"source"`
			Verdict string `json:"verdict"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Source == "precheck" && p.Verdict == "changes_requested" { //nolint:errcheck
			return
		}
	}
	t.Error("expected review_submitted(changes_requested, source=precheck) event in log")
}

// captureSink alias — use recordingSink from telemetry_integration_test.go.
var _ telemetry.Sink = (*recordingSink)(nil)
