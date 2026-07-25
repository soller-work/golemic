package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golemic/internal/agent"
	"golemic/internal/telemetry"
)

// ---------------------------------------------------------------------------
// Unit: parseActivityUsage
// ---------------------------------------------------------------------------

func writeUsageActivityLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l + "\n")
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func muLine(input, output, cacheRead, cacheWrite float64) string {
	usage := map[string]any{
		"input":      input,
		"output":     output,
		"cacheRead":  cacheRead,
		"cacheWrite": cacheWrite,
	}
	b, _ := json.Marshal(map[string]any{
		"type":  "message_update",
		"usage": usage,
	})
	return string(b)
}

func muLineNoCacheFields(input, output float64) string {
	usage := map[string]any{
		"input":  input,
		"output": output,
	}
	b, _ := json.Marshal(map[string]any{
		"type":  "message_update",
		"usage": usage,
	})
	return string(b)
}

func TestParseActivityUsage_SumsKnownValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev-r1-a0.activity.jsonl")
	writeUsageActivityLines(t, path, []string{
		muLine(100, 20, 50, 10),
		muLine(200, 30, 0, 5),
		`{"type":"tool_execution_start","toolName":"bash","args":{}}`,
		muLine(150, 40, 100, 0),
	})

	got := parseActivityUsage(path)

	if got.InputTokens != 450 {
		t.Errorf("InputTokens: got %d, want 450", got.InputTokens)
	}
	if got.OutputTokens != 90 {
		t.Errorf("OutputTokens: got %d, want 90", got.OutputTokens)
	}
	if got.CacheReadTokens != 150 {
		t.Errorf("CacheReadTokens: got %d, want 150", got.CacheReadTokens)
	}
	if got.CacheWriteTokens != 15 {
		t.Errorf("CacheWriteTokens: got %d, want 15", got.CacheWriteTokens)
	}
	if got.Turns != 3 {
		t.Errorf("Turns: got %d, want 3", got.Turns)
	}
}

func TestParseActivityUsage_NoCacheFields_YieldsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev-r1-a0.activity.jsonl")
	writeUsageActivityLines(t, path, []string{
		muLineNoCacheFields(100, 20),
		muLineNoCacheFields(200, 30),
	})

	got := parseActivityUsage(path)

	if got.InputTokens != 300 {
		t.Errorf("InputTokens: got %d, want 300", got.InputTokens)
	}
	if got.OutputTokens != 50 {
		t.Errorf("OutputTokens: got %d, want 50", got.OutputTokens)
	}
	if got.CacheReadTokens != 0 {
		t.Errorf("CacheReadTokens: got %d, want 0", got.CacheReadTokens)
	}
	if got.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens: got %d, want 0", got.CacheWriteTokens)
	}
	if got.Turns != 2 {
		t.Errorf("Turns: got %d, want 2", got.Turns)
	}
}

func TestParseActivityUsage_MissingFile_YieldsZero(t *testing.T) {
	got := parseActivityUsage("/nonexistent/path/dev-r1-a0.activity.jsonl")
	if got != (TokenUsage{}) {
		t.Errorf("expected zero TokenUsage for missing file, got %+v", got)
	}
}

func TestParseActivityUsage_EmptyFile_YieldsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev-r1-a0.activity.jsonl")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}

	got := parseActivityUsage(path)
	if got != (TokenUsage{}) {
		t.Errorf("expected zero TokenUsage for empty file, got %+v", got)
	}
}

func TestParseActivityUsage_MalformedLines_SkippedGracefully(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dev-r1-a0.activity.jsonl")
	writeUsageActivityLines(t, path, []string{
		"not json at all",
		`{"type":"message_update","usage":` + `"bad_scalar"}`,
		muLine(50, 10, 0, 0),
		`{broken`,
	})

	got := parseActivityUsage(path)

	if got.InputTokens != 50 {
		t.Errorf("InputTokens: got %d, want 50", got.InputTokens)
	}
	if got.Turns != 1 {
		t.Errorf("Turns: got %d, want 1", got.Turns)
	}
}

// ---------------------------------------------------------------------------
// Unit: wrapEndSpanWithUsage
// ---------------------------------------------------------------------------

func TestWrapEndSpanWithUsage_MergesAttrs(t *testing.T) {
	var capturedStatus string
	var capturedAttrs map[string]any
	base := func(status string, attrs map[string]any) {
		capturedStatus = status
		capturedAttrs = attrs
	}

	u := TokenUsage{
		InputTokens:      100,
		OutputTokens:     20,
		CacheReadTokens:  50,
		CacheWriteTokens: 10,
		Turns:            3,
	}
	wrapped := wrapEndSpanWithUsage(base, u)
	wrapped("ok", map[string]any{"outcome": "success"})

	if capturedStatus != "ok" {
		t.Errorf("status: got %q, want %q", capturedStatus, "ok")
	}
	if capturedAttrs["input_tokens"] != int64(100) {
		t.Errorf("input_tokens: got %v", capturedAttrs["input_tokens"])
	}
	if capturedAttrs["output_tokens"] != int64(20) {
		t.Errorf("output_tokens: got %v", capturedAttrs["output_tokens"])
	}
	if capturedAttrs["cache_read_tokens"] != int64(50) {
		t.Errorf("cache_read_tokens: got %v", capturedAttrs["cache_read_tokens"])
	}
	if capturedAttrs["cache_write_tokens"] != int64(10) {
		t.Errorf("cache_write_tokens: got %v", capturedAttrs["cache_write_tokens"])
	}
	if capturedAttrs["turns"] != 3 {
		t.Errorf("turns: got %v", capturedAttrs["turns"])
	}
	if capturedAttrs["outcome"] != "success" {
		t.Errorf("outcome attr should pass through: got %v", capturedAttrs["outcome"])
	}
}

func TestWrapEndSpanWithUsage_NilExtraAttrs(t *testing.T) {
	var capturedAttrs map[string]any
	base := func(_ string, attrs map[string]any) { capturedAttrs = attrs }

	wrapped := wrapEndSpanWithUsage(base, TokenUsage{InputTokens: 5, Turns: 1})
	wrapped("ok", nil)

	if capturedAttrs["input_tokens"] != int64(5) {
		t.Errorf("input_tokens missing when extra is nil: got %v", capturedAttrs["input_tokens"])
	}
}

// ---------------------------------------------------------------------------
// Unit: buildTokenUsageAggregate
// ---------------------------------------------------------------------------

func TestBuildTokenUsageAggregate_GroupsByRoleAndRound(t *testing.T) {
	log := []invocationTokenUsage{
		{Role: "dev", Round: 1, Attempt: 0, Usage: TokenUsage{InputTokens: 100, OutputTokens: 10, Turns: 2}},
		{Role: "dev", Round: 1, Attempt: 1, Usage: TokenUsage{InputTokens: 50, OutputTokens: 5, Turns: 1}},
		{Role: "reviewer", Round: 1, Attempt: 0, Usage: TokenUsage{InputTokens: 200, OutputTokens: 20, Turns: 3}},
		{Role: "dev", Round: 2, Attempt: 0, Usage: TokenUsage{InputTokens: 80, OutputTokens: 8, CacheReadTokens: 40, Turns: 1}},
	}

	agg := buildTokenUsageAggregate(log)

	devR1 := agg["dev"]["1"]
	if devR1.InputTokens != 150 {
		t.Errorf("dev/round1 InputTokens: got %d, want 150", devR1.InputTokens)
	}
	if devR1.OutputTokens != 15 {
		t.Errorf("dev/round1 OutputTokens: got %d, want 15", devR1.OutputTokens)
	}
	if devR1.Turns != 3 {
		t.Errorf("dev/round1 Turns: got %d, want 3", devR1.Turns)
	}

	revR1 := agg["reviewer"]["1"]
	if revR1.InputTokens != 200 {
		t.Errorf("reviewer/round1 InputTokens: got %d, want 200", revR1.InputTokens)
	}

	devR2 := agg["dev"]["2"]
	if devR2.InputTokens != 80 {
		t.Errorf("dev/round2 InputTokens: got %d, want 80", devR2.InputTokens)
	}
	if devR2.CacheReadTokens != 40 {
		t.Errorf("dev/round2 CacheReadTokens: got %d, want 40", devR2.CacheReadTokens)
	}
}

func TestBuildTokenUsageAggregate_EmptyLog_ReturnsEmptyMap(t *testing.T) {
	agg := buildTokenUsageAggregate(nil)
	if len(agg) != 0 {
		t.Errorf("expected empty aggregate for empty log, got %v", agg)
	}
}

// ---------------------------------------------------------------------------
// Integration: agent.turn span end carries token usage attrs
// ---------------------------------------------------------------------------

// writeActivityForInvocation creates an activity file for a given invocation with
// known usage values so tests can assert on the resulting aggregates.
func writeActivityForInvocation(t *testing.T, runsDir, runID, role string, round, attempt int, lines []string) string {
	t.Helper()
	path := filepath.Join(runsDir, runID, fmt.Sprintf("%s-r%d-a%d.activity.jsonl", role, round, attempt))
	writeUsageActivityLines(t, path, lines)
	return path
}

// buildTokenUsageRunner sets up a pingpong-style runner with a recording sink.
func buildTokenUsageRunner(t *testing.T) (*Runner, string, *recordingSink) {
	t.Helper()
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)
	sink := &recordingSink{}
	r.sink = sink
	r.traceID = telemetry.TraceID(r.runID)
	return r, logPath, sink
}

// agentFnWithUsage returns a fake agent that writes known usage lines per invocation.
func agentFnWithUsage(t *testing.T, runsDir string, lines []string) func(context.Context, agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	return func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		actPath := writeActivityForInvocation(t, runsDir, cfg.RunID, cfg.Role, cfg.Round, cfg.Attempt, lines)
		satisfyBrokerGate(t, cfg)
		return 0, agent.TranscriptPaths{Stdout: actPath, Stderr: "/tmp/stderr"}, nil
	}
}

// agentFnNoActivity returns a fake agent that does NOT write any activity file.
func agentFnNoActivity(t *testing.T) func(context.Context, agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	return func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		satisfyBrokerGate(t, cfg)
		return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
	}
}

// satisfyBrokerGate sends the correct broker messages for each role.
func satisfyBrokerGate(t *testing.T, cfg agent.RoleConfig) {
	t.Helper()
	switch cfg.Role {
	case "dev":
		if !sendGMProjectCheck(cfg.Env) {
			t.Error("sendGMProjectCheck failed")
		}
		if !sendGMDevDone(cfg.Env) {
			t.Error("sendGMDevDone failed")
		}
	case "reviewer":
		writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM", cfg.Round, ciTestHeadSHA)
	}
}

// agentTurnEndSpans returns the span.end records for agent.turn spans.
func agentTurnEndSpans(records []telemetry.Record) []telemetry.Record {
	starts, _ := indexByKind(records)
	var out []telemetry.Record
	for _, rec := range records {
		if rec.Kind != telemetry.KindSpanEnd {
			continue
		}
		if s, ok := starts[rec.SpanID]; ok && s.Name == telemetry.SpanAgentTurn {
			out = append(out, rec)
		}
	}
	return out
}

func TestTokenUsage_AgentTurnSpanCarriesUsageAttrs(t *testing.T) {
	r, logPath, sink := buildTokenUsageRunner(t)
	runsDir := filepath.Join(ppHome, ".golemic", ppProject, "runs")
	r.SetRunAgentFn(agentFnWithUsage(t, runsDir, []string{
		muLine(100, 20, 50, 10),
		muLine(200, 30, 0, 5),
	}))

	if outcome := runOrchestrate(t, r, logPath); outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}

	turns := agentTurnEndSpans(sink.all())
	if len(turns) < 2 {
		t.Fatalf("expected >=2 agent.turn span ends, got %d", len(turns))
	}
	for _, rec := range turns {
		if rec.Attrs == nil {
			t.Errorf("agent.turn span %q end has nil attrs", rec.SpanID)
			continue
		}
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "turns"} {
			if _, ok := rec.Attrs[key]; !ok {
				t.Errorf("agent.turn span %q missing attr %q", rec.SpanID, key)
			}
		}
		if v, _ := rec.Attrs["input_tokens"].(int64); v != 300 {
			t.Errorf("span %q: input_tokens %v, want 300", rec.SpanID, rec.Attrs["input_tokens"])
		}
		if v, _ := rec.Attrs["turns"].(int); v != 2 {
			t.Errorf("span %q: turns %v, want 2", rec.SpanID, rec.Attrs["turns"])
		}
	}
}

// ---------------------------------------------------------------------------
// Integration: run_finished payload carries per-role/round token aggregate
// ---------------------------------------------------------------------------

// assertAggUsage checks a single role/round entry in the token usage aggregate.
func assertAggUsage(t *testing.T, agg map[string]map[string]TokenUsage, role, round string, wantInput, wantCacheRead int64) {
	t.Helper()
	u, ok := agg[role][round]
	if !ok {
		t.Fatalf("aggregate missing %s/round%s", role, round)
	}
	if u.InputTokens != wantInput {
		t.Errorf("%s/round%s inputTokens: got %d, want %d", role, round, u.InputTokens, wantInput)
	}
	if u.CacheReadTokens != wantCacheRead {
		t.Errorf("%s/round%s cacheReadTokens: got %d, want %d", role, round, u.CacheReadTokens, wantCacheRead)
	}
}

// TestTokenUsage_RunFinishedPayloadCarriesAggregate verifies that the runner's
// tokenUsageLog produces the correct per-role/round aggregate after orchestrate.
func TestTokenUsage_RunFinishedPayloadCarriesAggregate(t *testing.T) {
	r, logPath, _ := buildTokenUsageRunner(t)
	runsDir := filepath.Join(ppHome, ".golemic", ppProject, "runs")
	r.SetRunAgentFn(agentFnWithUsage(t, runsDir, []string{muLine(100, 20, 50, 10)}))

	if outcome := runOrchestrate(t, r, logPath); outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}
	if len(r.tokenUsageLog) < 2 {
		t.Fatalf("expected >=2 token usage entries, got %d", len(r.tokenUsageLog))
	}

	agg := buildTokenUsageAggregate(r.tokenUsageLog)
	assertAggUsage(t, agg, "dev", "1", 100, 50)
	assertAggUsage(t, agg, "reviewer", "1", 100, 50)
}

// ---------------------------------------------------------------------------
// Graceful degradation: missing activity file → zeros, run completes normally
// ---------------------------------------------------------------------------

func TestTokenUsage_MissingActivityFile_RunCompletesWithZeroUsage(t *testing.T) {
	r, logPath, _ := buildTokenUsageRunner(t)
	r.SetRunAgentFn(agentFnNoActivity(t))

	if outcome := runOrchestrate(t, r, logPath); outcome != outcomeSuccess {
		t.Fatalf("expected success with missing activity files, got %q", outcome)
	}
	for _, entry := range r.tokenUsageLog {
		if entry.Usage != (TokenUsage{}) {
			t.Errorf("%s/round%d/attempt%d: expected zero usage, got %+v",
				entry.Role, entry.Round, entry.Attempt, entry.Usage)
		}
	}
}

// ---------------------------------------------------------------------------
// Behavioral invariant: usage capture does not alter agent outcomes
// ---------------------------------------------------------------------------

func TestTokenUsage_UsageCaptureDoesNotAlterOutcomes(t *testing.T) {
	r, logPath, _ := buildTokenUsageRunner(t)
	runsDir := filepath.Join(ppHome, ".golemic", ppProject, "runs")

	var capturedCfgs []agent.RoleConfig
	inner := agentFnWithUsage(t, runsDir, []string{muLine(100, 20, 50, 10)})
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		capturedCfgs = append(capturedCfgs, cfg)
		return inner(ctx, cfg)
	})

	if outcome := runOrchestrate(t, r, logPath); outcome != outcomeSuccess {
		t.Fatalf("expected success, got %q", outcome)
	}
	if len(capturedCfgs) < 2 {
		t.Fatalf("expected >=2 agent calls, got %d", len(capturedCfgs))
	}
	for _, cfg := range capturedCfgs {
		if cfg.UserPrompt == "" {
			t.Errorf("role %q: UserPrompt empty — usage capture must not clear prompts", cfg.Role)
		}
	}
}
