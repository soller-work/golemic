package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golemic/internal/agent"
	"golemic/internal/telemetry"
)

// ---------------------------------------------------------------------------
// Test sinks
// ---------------------------------------------------------------------------

// recordingSink captures all emitted telemetry records in order.
type recordingSink struct {
	mu      sync.Mutex
	records []telemetry.Record
}

func (s *recordingSink) Emit(r telemetry.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return nil
}

func (s *recordingSink) all() []telemetry.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]telemetry.Record, len(s.records))
	copy(out, s.records)
	return out
}

// telemetryFailingSink returns an error on every Emit call.
type telemetryFailingSink struct{}

func (telemetryFailingSink) Emit(telemetry.Record) error {
	return fmt.Errorf("TELEMETRY_WRITE_FAILED: injected error")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// readTelemetryFile reads all records from a telemetry.jsonl file.
func readTelemetryFile(t *testing.T, path string) []telemetry.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open telemetry file %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck

	var records []telemetry.Record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var rec telemetry.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse telemetry record %q: %v", line, err)
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan telemetry file: %v", err)
	}
	return records
}

// assertSpanEndValid checks that a span.end record has valid duration_ms and status.
func assertSpanEndValid(t *testing.T, end telemetry.Record) {
	t.Helper()
	if end.DurationMS == nil || *end.DurationMS < 0 {
		t.Errorf("span %q end record has invalid duration_ms", end.SpanID)
	}
	if end.Status == "" {
		t.Errorf("span %q end record missing status", end.SpanID)
	}
}

// assertPairedSpans checks that every span.start has a matching span.end with
// duration_ms >= 0 and a non-empty status.
func assertPairedSpans(t *testing.T, records []telemetry.Record) {
	t.Helper()
	starts, ends := indexByKind(records)
	for spanID, start := range starts {
		end, ok := ends[spanID]
		if !ok {
			t.Errorf("span %q (name=%s) has start but no matching end", spanID, start.Name)
			continue
		}
		assertSpanEndValid(t, end)
	}
	for spanID := range ends {
		if _, ok := starts[spanID]; !ok {
			t.Errorf("span %q has end but no matching start", spanID)
		}
	}
}

// indexByKind splits records into starts and ends maps keyed by span_id.
func indexByKind(records []telemetry.Record) (starts, ends map[string]telemetry.Record) {
	starts = make(map[string]telemetry.Record, len(records)/2+1)
	ends = make(map[string]telemetry.Record, len(records)/2+1)
	for _, r := range records {
		switch r.Kind {
		case telemetry.KindSpanStart:
			starts[r.SpanID] = r
		case telemetry.KindSpanEnd:
			ends[r.SpanID] = r
		}
	}
	return
}

// assertRunSpanAttrs asserts the run span has pid=os.Getpid() and all records share trace_id.
func assertRunSpanAttrs(t *testing.T, records []telemetry.Record, runID string) {
	t.Helper()
	runSpans := spansWithName(records, telemetry.SpanRun)
	if len(runSpans) != 1 {
		t.Fatalf("expected 1 run span.start, got %d", len(runSpans))
	}
	pid, ok := runSpans[0].Attrs["pid"]
	if !ok {
		t.Error("run span.start missing pid attribute")
	} else if int(pid.(float64)) != os.Getpid() {
		t.Errorf("run span pid: got %v, want %d", pid, os.Getpid())
	}
	expectedTraceID := telemetry.TraceID(runID)
	for i, rec := range records {
		if rec.TraceID != expectedTraceID {
			t.Errorf("record[%d] trace_id: got %q, want %q", i, rec.TraceID, expectedTraceID)
		}
	}
}

// assertSpanNamesPresent checks that at least one worktree.create and agent.turn span exist.
func assertSpanNamesPresent(t *testing.T, records []telemetry.Record) {
	t.Helper()
	if len(spansWithName(records, telemetry.SpanWorktreeCreate)) == 0 {
		t.Error("expected at least one worktree.create span")
	}
	if len(spansWithName(records, telemetry.SpanAgentTurn)) == 0 {
		t.Error("expected at least one agent.turn span")
	}
}

// spansWithName returns all span.start records with the given name.
func spansWithName(records []telemetry.Record, name string) []telemetry.Record {
	var out []telemetry.Record
	for _, r := range records {
		if r.Kind == telemetry.KindSpanStart && r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

// createGuidelines writes minimal dev.md and reviewer.md guideline files under repoRoot.
func createGuidelines(t *testing.T, repoRoot string) {
	t.Helper()
	guidelinesDir := filepath.Join(repoRoot, ".golemic", "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev.md", "reviewer.md"} {
		if err := os.WriteFile(filepath.Join(guidelinesDir, name), []byte("# Guidelines"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// attrRound extracts the round attribute as int, tolerating both int and float64
// (recording sinks store int; JSON-parsed records store float64).
func attrRound(attrs map[string]any) int {
	switch v := attrs["round"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// telemetryGitHandler handles git commands for the full-run telemetry executor.
func telemetryGitHandler(repoRoot string, args []string) (string, error) {
	sub := ""
	if len(args) >= 3 && args[0] == "-C" {
		sub = args[2]
	} else if len(args) >= 1 {
		sub = args[0]
	}
	switch sub {
	case "rev-parse":
		if len(args) >= 3 && args[0] == "-C" {
			return "abc123\n", nil // worktree base SHA
		}
		return repoRoot + "\n", nil // for resolveHostRepo
	case "fetch", "worktree", "config", "branch", "status", "ls-remote":
		return "", nil
	}
	return "", fmt.Errorf("not mocked: git %v", args)
}

// telemetryGhHandler handles gh commands for the full-run telemetry executor.
func telemetryGhHandler(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("gh: no args")
	}
	switch args[0] {
	case "issue":
		return `{"title":"Test Issue","body":"Test body"}`, nil
	case "pr":
		if len(args) >= 2 && args[1] == "comment" {
			return "", nil
		}
		return "[]", nil // pr list → no collision; pr checks → no checks
	}
	return "", fmt.Errorf("not mocked: gh %v", args)
}

// setupTelemetryFullRunExecutor builds an executor that handles all git and gh
// commands needed for a complete golemic Run() with a fake agent.
func setupTelemetryFullRunExecutor(repoRoot string) *fakeExecutor {
	return &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name != "git" {
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			}
			return telemetryGitHandler(repoRoot, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name != "gh" {
				return "", fmt.Errorf("not mocked: %s %v", name, args)
			}
			return telemetryGhHandler(args)
		},
	}
}

// makeTelemetryFakeAgent returns a fake agent that drives a successful single-round
// run: dev writes pr_opened, reviewer writes review approved.
func makeTelemetryFakeAgent(t *testing.T) func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	devCalled := false
	return func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			if !devCalled {
				devCalled = true
				writePROpenedEvent(t, cfg.EventLogPath, 99)
			}
		case "reviewer":
			writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
		}
		return 0, agent.TranscriptPaths{}, nil
	}
}

// ---------------------------------------------------------------------------
// AC-001: Full Run() emits paired spans to telemetry.jsonl including run span
// ---------------------------------------------------------------------------

func TestTelemetry_FullRun_PairedSpansInFile_AC001(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)
	exec := setupTelemetryFullRunExecutor(repoRoot)

	createGuidelines(t, repoRoot)

	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetRunAgentFn(makeTelemetryFakeAgent(t))
	var stdout, stderr bytes.Buffer
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()
	if exitCode != 0 {
		t.Fatalf("Run() returned %d; stderr: %s", exitCode, stderr.String())
	}

	// Locate the run directory and telemetry.jsonl
	runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no run directory found in %s: %v", runsDir, err)
	}
	runID := entries[0].Name()
	telemetryPath := filepath.Join(runsDir, runID, "telemetry.jsonl")
	if _, err := os.Stat(telemetryPath); err != nil {
		t.Fatalf("telemetry.jsonl not created: %v", err)
	}

	records := readTelemetryFile(t, telemetryPath)
	assertPairedSpans(t, records)
	assertRunSpanAttrs(t, records, runID)
	assertSpanNamesPresent(t, records)
}

// ---------------------------------------------------------------------------
// AC-002: span.start is present in records before span.end (start-before-end ordering)
// ---------------------------------------------------------------------------

func TestTelemetry_AgentTurnSpanStartBeforeEnd_AC002(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	sink := &recordingSink{}
	r.sink = sink
	r.traceID = telemetry.TraceID(r.runID)

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}

	records := sink.all()

	// Build index map: spanID → list of record positions by kind
	startIdx := map[string]int{}
	endIdx := map[string]int{}
	for i, rec := range records {
		switch rec.Kind {
		case telemetry.KindSpanStart:
			startIdx[rec.SpanID] = i
		case telemetry.KindSpanEnd:
			endIdx[rec.SpanID] = i
		}
	}

	// Every start must appear before its end
	for spanID, si := range startIdx {
		ei, hasEnd := endIdx[spanID]
		if !hasEnd {
			t.Errorf("span %q has start (idx=%d) but no end", spanID, si)
			continue
		}
		if si >= ei {
			t.Errorf("span %q: start at index %d is not before end at index %d", spanID, si, ei)
		}
	}

	// Specifically check agent.turn spans
	agentTurnStarts := spansWithName(records, telemetry.SpanAgentTurn)
	if len(agentTurnStarts) < 2 {
		t.Errorf("expected at least 2 agent.turn spans (dev + reviewer), got %d", len(agentTurnStarts))
	}
}

// ---------------------------------------------------------------------------
// AC-003: telemetry.enabled=false → no telemetry.jsonl created
// ---------------------------------------------------------------------------

func TestTelemetry_Disabled_NoFileCreated_AC003(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)

	// Override config with telemetry disabled
	configJSON := fmt.Sprintf(`{"project":%q,"verify_command":"go test","telemetry":{"enabled":false}}`, project)
	configPath := filepath.Join(repoRoot, ".golemic", "config.json")
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	exec := setupTelemetryFullRunExecutor(repoRoot)
	createGuidelines(t, repoRoot)

	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetRunAgentFn(makeTelemetryFakeAgent(t))
	var stdout, stderr bytes.Buffer
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()
	if exitCode != 0 {
		t.Fatalf("Run() returned %d; stderr: %s", exitCode, stderr.String())
	}

	runsDir := filepath.Join(homeDir, ".golemic", project, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no run directory found: %v", err)
	}
	for _, entry := range entries {
		telemetryPath := filepath.Join(runsDir, entry.Name(), "telemetry.jsonl")
		if _, statErr := os.Stat(telemetryPath); statErr == nil {
			t.Errorf("telemetry.jsonl must not exist when telemetry is disabled: %s", telemetryPath)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-004: Failing sink does not abort the run
// ---------------------------------------------------------------------------

func TestTelemetry_FailingSink_DoesNotAbortRun_AC004(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	r.SetSink(telemetryFailingSink{})
	r.traceID = telemetry.TraceID(r.runID)

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("failing sink must not change run outcome; got %q, want %q", outcome, outcomeSuccess)
	}
}

// TestTelemetry_FailingSink_ViaRun_AC004 verifies the failing sink at the full
// Run() level: StartSpan errors are swallowed and the exit code is unaffected.
func TestTelemetry_FailingSink_ViaRun_AC004(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)
	exec := setupTelemetryFullRunExecutor(repoRoot)
	createGuidelines(t, repoRoot)

	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetRunAgentFn(makeTelemetryFakeAgent(t))
	r.SetSink(telemetryFailingSink{})
	var stdout, stderr bytes.Buffer
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)

	exitCode := r.Run()
	if exitCode != 0 {
		t.Errorf("failing sink must not affect exit code; got %d, stderr: %s", exitCode, stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC-006: Telemetry records contain no secrets or content
// ---------------------------------------------------------------------------

func TestTelemetry_NoSecretsInRecords_AC006(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	sink := &recordingSink{}
	r.sink = sink
	r.traceID = telemetry.TraceID(r.runID)

	// findings contains a ghp_-like token to prove it does not leak from
	// reviewer body into telemetry attributes.
	findings := "Fix the null-pointer in auth.go; token ref: ghp_FINDINGS_BODY_MUST_NOT_APPEAR"

	// Credentials written by setupRunnerTest contain ghp_dev_test_token and
	// ghp_rev_test_token; the sink must never record those either.
	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: findings, exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}

	allowedAttrKeys := map[string]bool{
		"run_id": true, "issue": true, "project": true, "role": true,
		"round": true, "model": true, "status": true, "outcome": true,
		"pid": true, "worktree": true,
	}

	secretStrings := []string{
		"ghp_dev_test_token",
		"ghp_rev_test_token",
		findings,
		"ghp_FINDINGS_BODY_MUST_NOT_APPEAR",
	}

	for _, rec := range sink.all() {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		line := string(data)

		for _, secret := range secretStrings {
			if strings.Contains(line, secret) {
				t.Errorf("telemetry record contains secret/findings %q: %s", secret, line)
			}
		}

		// Catch any ghp_ token not already listed above.
		if strings.Contains(line, "ghp_") {
			t.Errorf("telemetry record contains a ghp_ token: %s", line)
		}

		for key := range rec.Attrs {
			if !allowedAttrKeys[key] {
				t.Errorf("unexpected attribute key %q in record: %s", key, line)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// AC-005: Ping-pong rounds encoded as attributes, not in span name
// ---------------------------------------------------------------------------

func TestTelemetry_RoundAttributes_NotInSpanName_AC005(t *testing.T) {
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	sink := &recordingSink{}
	r.sink = sink
	r.traceID = telemetry.TraceID(r.runID)

	r.SetRunAgentFn(makeOrchestrateFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Fix the typo", exitCode: 0},
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}

	records := sink.all()
	agentStarts := spansWithName(records, telemetry.SpanAgentTurn)

	// Must have dev round 1, reviewer round 1, dev round 2, reviewer round 2
	if len(agentStarts) < 4 {
		t.Fatalf("expected >= 4 agent.turn spans, got %d", len(agentStarts))
	}

	// All agent.turn spans must share the same name
	for _, s := range agentStarts {
		if s.Name != telemetry.SpanAgentTurn {
			t.Errorf("agent.turn span has wrong name %q", s.Name)
		}
	}

	// Collect (role, round) pairs; must see both rounds for dev and reviewer
	type roleRound struct {
		role  string
		round int
	}
	seen := map[roleRound]bool{}
	for _, s := range agentStarts {
		role, _ := s.Attrs["role"].(string)
		seen[roleRound{role, attrRound(s.Attrs)}] = true
	}
	for _, want := range []roleRound{{"dev", 1}, {"reviewer", 1}, {"dev", 2}, {"reviewer", 2}} {
		if !seen[want] {
			t.Errorf("missing agent.turn span with role=%q round=%d", want.role, want.round)
		}
	}
}
