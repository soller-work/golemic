package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Probe helpers
// ---------------------------------------------------------------------------

func aliveProbe(_ int) string { return LivenessAlive }
func deadProbe(_ int) string  { return LivenessDead }

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// ---------------------------------------------------------------------------
// Fixture builders
// ---------------------------------------------------------------------------

// writeEvents writes a minimal events.jsonl with the given lines to a run dir.
func writeEvents(t *testing.T, runDir string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeTelemetry writes a minimal telemetry.jsonl to a run dir.
func writeTelemetry(t *testing.T, runDir string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(runDir, "telemetry.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// runStartedLine returns an events.jsonl run_started line.
func runStartedLine(issue int, runID string, ts time.Time) string {
	payload, _ := json.Marshal(map[string]interface{}{"issue": issue, "runId": runID})
	ev, _ := json.Marshal(map[string]interface{}{
		"type":    "run_started",
		"ts":      ts.Format(time.RFC3339),
		"runId":   runID,
		"payload": json.RawMessage(payload),
	})
	return string(ev)
}

// runFinishedLine returns an events.jsonl run_finished line.
func runFinishedLine(outcome, runID string, ts time.Time) string {
	payload, _ := json.Marshal(map[string]string{"outcome": outcome})
	ev, _ := json.Marshal(map[string]interface{}{
		"type":    "run_finished",
		"ts":      ts.Format(time.RFC3339),
		"runId":   runID,
		"payload": json.RawMessage(payload),
	})
	return string(ev)
}

// spanStartLine returns a telemetry.jsonl span.start line.
func spanStartLine(spanID, name, startTS string, attrs map[string]interface{}) string {
	if attrs == nil {
		attrs = map[string]interface{}{}
	}
	rec, _ := json.Marshal(map[string]interface{}{
		"kind":       "span.start",
		"trace_id":   "abc",
		"span_id":    spanID,
		"name":       name,
		"start_ts":   startTS,
		"attributes": attrs,
	})
	return string(rec)
}

// spanEndLine returns a telemetry.jsonl span.end line.
func spanEndLine(spanID, endTS string, durationMS int64) string {
	rec, _ := json.Marshal(map[string]interface{}{
		"kind":        "span.end",
		"trace_id":    "abc",
		"span_id":     spanID,
		"end_ts":      endTS,
		"duration_ms": durationMS,
		"status":      "ok",
	})
	return string(rec)
}

// ---------------------------------------------------------------------------
// AC-001: Finished and failed runs classified from events.jsonl
// ---------------------------------------------------------------------------

func TestClassify_Finished_AC001(t *testing.T) {
	dir := t.TempDir()
	runID := "issue-1-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	writeEvents(t, runDir, []string{
		runStartedLine(1, runID, startTS),
		runFinishedLine("success", runID, startTS.Add(5*time.Minute)),
	})

	c := &Classifier{Probe: deadProbe, Now: fixedNow(startTS.Add(10 * time.Minute))}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Status != StatusFinished {
		t.Errorf("status: got %q, want %q", h.Status, StatusFinished)
	}
	if h.Outcome != "success" {
		t.Errorf("outcome: got %q, want %q", h.Outcome, "success")
	}
}

func TestClassify_Failed_AC001(t *testing.T) {
	dir := t.TempDir()
	runID := "issue-2-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	writeEvents(t, runDir, []string{
		runStartedLine(2, runID, startTS),
		runFinishedLine("dev_failed", runID, startTS.Add(5*time.Minute)),
	})

	c := &Classifier{Probe: deadProbe, Now: fixedNow(startTS.Add(10 * time.Minute))}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Status != StatusFailed {
		t.Errorf("status: got %q, want %q", h.Status, StatusFailed)
	}
	if h.Outcome != "dev_failed" {
		t.Errorf("outcome: got %q, want %q", h.Outcome, "dev_failed")
	}
}

// ---------------------------------------------------------------------------
// AC-002: Running vs stalled distinguished by open-span age
// ---------------------------------------------------------------------------

func TestClassify_Running_AC002(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-3-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-5 * time.Minute) // run started 5m ago
	spanTS := now.Add(-2 * time.Minute)  // agent.turn started 2m ago (within 30m threshold)
	pid := 12345

	writeEvents(t, runDir, []string{runStartedLine(3, runID, startTS)})
	writeTelemetry(t, runDir, []string{
		spanStartLine("run-span", "run", startTS.Format(time.RFC3339), map[string]interface{}{"pid": pid}),
		spanStartLine("agent-span", "agent.turn", spanTS.Format(time.RFC3339), nil),
	})

	c := &Classifier{Probe: aliveProbe, Now: fixedNow(now), StalledAfter: 30 * time.Minute}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Status != StatusRunning {
		t.Errorf("status: got %q, want %q", h.Status, StatusRunning)
	}
	if h.CurrentPhase != "agent.turn" {
		t.Errorf("current_phase: got %q, want %q", h.CurrentPhase, "agent.turn")
	}
	if h.Liveness != LivenessAlive {
		t.Errorf("liveness: got %q, want %q", h.Liveness, LivenessAlive)
	}
}

func TestClassify_Stalled_AC002(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-4-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-35 * time.Minute) // run started 35m ago
	spanTS := now.Add(-31 * time.Minute)  // agent.turn started 31m ago (over 30m threshold)
	pid := 12345

	writeEvents(t, runDir, []string{runStartedLine(4, runID, startTS)})
	writeTelemetry(t, runDir, []string{
		spanStartLine("run-span", "run", startTS.Format(time.RFC3339), map[string]interface{}{"pid": pid}),
		spanStartLine("agent-span", "agent.turn", spanTS.Format(time.RFC3339), nil),
	})

	c := &Classifier{Probe: aliveProbe, Now: fixedNow(now), StalledAfter: 30 * time.Minute}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Status != StatusStalled {
		t.Errorf("status: got %q, want %q", h.Status, StatusStalled)
	}
	if h.CurrentPhase != "agent.turn" {
		t.Errorf("current_phase: got %q, want %q", h.CurrentPhase, "agent.turn")
	}
}

// ---------------------------------------------------------------------------
// AC-003: Wedged run detected when pid is dead
// ---------------------------------------------------------------------------

func TestClassify_Wedged_AC003(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-5-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-10 * time.Minute)
	pid := 99999

	writeEvents(t, runDir, []string{runStartedLine(5, runID, startTS)})
	writeTelemetry(t, runDir, []string{
		spanStartLine("run-span", "run", startTS.Format(time.RFC3339), map[string]interface{}{"pid": pid}),
	})

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Status != StatusWedged {
		t.Errorf("status: got %q, want %q", h.Status, StatusWedged)
	}
	if h.Liveness != LivenessDead {
		t.Errorf("liveness: got %q, want %q", h.Liveness, LivenessDead)
	}
}

// ---------------------------------------------------------------------------
// AC-004: Missing telemetry falls back to events-only with indeterminate liveness
// ---------------------------------------------------------------------------

func TestClassify_MissingTelemetry_AC004(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-6-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-5 * time.Minute)

	// events.jsonl present, telemetry.jsonl absent
	writeEvents(t, runDir, []string{runStartedLine(6, runID, startTS)})

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Liveness != LivenessIndeterminate {
		t.Errorf("liveness: got %q, want %q", h.Liveness, LivenessIndeterminate)
	}
	// Status from events alone: no run_finished → running (indeterminate)
	if h.Status != StatusRunning {
		t.Errorf("status: got %q, want %q", h.Status, StatusRunning)
	}
}

func TestClassify_MissingTelemetry_FinishedEvent_AC004(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-7-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-5 * time.Minute)

	writeEvents(t, runDir, []string{
		runStartedLine(7, runID, startTS),
		runFinishedLine("success", runID, startTS.Add(3*time.Minute)),
	})
	// No telemetry.jsonl

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Status != StatusFinished {
		t.Errorf("status: got %q, want %q", h.Status, StatusFinished)
	}
	if h.Liveness != LivenessIndeterminate {
		t.Errorf("liveness: got %q, want %q", h.Liveness, LivenessIndeterminate)
	}
}

// ---------------------------------------------------------------------------
// AC-005: All-runs sorting, single-run form
// ---------------------------------------------------------------------------

func TestClassifyAll_NewestFirst_AC005(t *testing.T) {
	runsDir := t.TempDir()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	ids := []string{
		"issue-1-20260718T100000Z",
		"issue-2-20260718T110000Z",
		"issue-3-20260718T090000Z",
	}
	for _, id := range ids {
		runDir := filepath.Join(runsDir, id)
		ts := now.Add(-1 * time.Hour)
		writeEvents(t, runDir, []string{
			runStartedLine(1, id, ts),
			runFinishedLine("success", id, ts.Add(time.Minute)),
		})
	}

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	runs, err := c.ClassifyAll(runsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	// Newest-first: 11:00 > 10:00 > 09:00
	wantOrder := []string{
		"issue-2-20260718T110000Z",
		"issue-1-20260718T100000Z",
		"issue-3-20260718T090000Z",
	}
	for i, want := range wantOrder {
		if runs[i].RunID != want {
			t.Errorf("runs[%d]: got %q, want %q", i, runs[i].RunID, want)
		}
	}
}

func TestClassifyOne_SingleRun_AC005(t *testing.T) {
	runsDir := t.TempDir()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	runID := "issue-9-20260718T100000Z"
	runDir := filepath.Join(runsDir, runID)
	startTS := now.Add(-5 * time.Minute)

	writeEvents(t, runDir, []string{
		runStartedLine(9, runID, startTS),
		runFinishedLine("success", runID, startTS.Add(time.Minute)),
	})

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.RunID != runID {
		t.Errorf("run_id: got %q, want %q", h.RunID, runID)
	}
	if h.Status != StatusFinished {
		t.Errorf("status: got %q, want %q", h.Status, StatusFinished)
	}
}

// ---------------------------------------------------------------------------
// AC-006: Empty result and missing runId
// ---------------------------------------------------------------------------

func TestClassifyAll_EmptyRunsDir_AC006(t *testing.T) {
	runsDir := t.TempDir()
	c := &Classifier{Probe: deadProbe}
	runs, err := c.ClassifyAll(runsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestClassifyAll_MissingRunsDir_AC006(t *testing.T) {
	c := &Classifier{Probe: deadProbe}
	runs, err := c.ClassifyAll("/nonexistent/path/runs")
	if err != nil {
		t.Fatalf("missing runs dir should not error, got: %v", err)
	}
	if runs != nil {
		t.Errorf("expected nil slice, got %v", runs)
	}
}

func TestClassifyOne_MissingRunId_AC006(t *testing.T) {
	c := &Classifier{Probe: deadProbe}
	_, err := c.ClassifyOne("/nonexistent/path/issue-1-20260718T100000Z")
	if err == nil {
		t.Fatal("expected RUN_NOT_FOUND error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Unit: open-span detection and partial-line tolerance
// ---------------------------------------------------------------------------

func TestReadTelemetry_OpenSpanDetection(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	pid := 42
	startTS := now.Add(-5 * time.Minute).Format(time.RFC3339)
	spanTS := now.Add(-2 * time.Minute).Format(time.RFC3339)

	runDir := filepath.Join(dir, "issue-1-20260718T100000Z")
	writeTelemetry(t, runDir, []string{
		spanStartLine("s1", "run", startTS, map[string]interface{}{"pid": pid}),
		spanStartLine("s2", "agent.turn", spanTS, nil),
		// span.end for run (closed), agent.turn is still open
		spanEndLine("s1", now.Format(time.RFC3339), 300000),
	})

	tel := readTelemetry(filepath.Join(runDir, "telemetry.jsonl"))
	if tel == nil {
		t.Fatal("expected telemetry data, got nil")
	}
	if tel.RunSpanPID == nil || *tel.RunSpanPID != pid {
		t.Errorf("RunSpanPID: got %v, want %d", tel.RunSpanPID, pid)
	}
	if len(tel.OpenSpans) != 1 {
		t.Fatalf("expected 1 open span, got %d", len(tel.OpenSpans))
	}
	if tel.OpenSpans[0].Name != "agent.turn" {
		t.Errorf("open span name: got %q, want %q", tel.OpenSpans[0].Name, "agent.turn")
	}
}

func TestReadTelemetry_PartialLastLine_Tolerated(t *testing.T) {
	dir := t.TempDir()
	pid := 99
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	startTS := now.Add(-1 * time.Minute).Format(time.RFC3339)
	runDir := filepath.Join(dir, "issue-1-20260718T100000Z")

	// Write valid record then a partial/malformed last line.
	content := spanStartLine("s1", "run", startTS, map[string]interface{}{"pid": pid}) + "\n"
	content += `{"kind":"span.start","span_id":"s2","name":"agent` // truncated
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "telemetry.jsonl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tel := readTelemetry(filepath.Join(runDir, "telemetry.jsonl"))
	if tel == nil {
		t.Fatal("expected telemetry data, got nil")
	}
	if tel.RunSpanPID == nil || *tel.RunSpanPID != pid {
		t.Errorf("RunSpanPID: got %v, want %d", tel.RunSpanPID, pid)
	}
}

// ---------------------------------------------------------------------------
// Unit: stalled threshold uses StalledAfter override (BR-006)
// ---------------------------------------------------------------------------

func TestClassify_StalledAfterOverride_BR006(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-8-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-10 * time.Minute)
	spanTS := now.Add(-6 * time.Minute) // 6 minutes old
	pid := 12345

	writeEvents(t, runDir, []string{runStartedLine(8, runID, startTS)})
	writeTelemetry(t, runDir, []string{
		spanStartLine("s1", "run", startTS.Format(time.RFC3339), map[string]interface{}{"pid": pid}),
		spanStartLine("s2", "agent.turn", spanTS.Format(time.RFC3339), nil),
	})

	// With 30m threshold → running (6m < 30m)
	c1 := &Classifier{Probe: aliveProbe, Now: fixedNow(now), StalledAfter: 30 * time.Minute}
	h1, _ := c1.ClassifyOne(runDir)
	if h1.Status != StatusRunning {
		t.Errorf("with 30m threshold: got %q, want running", h1.Status)
	}

	// With 5m threshold → stalled (6m > 5m)
	c2 := &Classifier{Probe: aliveProbe, Now: fixedNow(now), StalledAfter: 5 * time.Minute}
	h2, _ := c2.ClassifyOne(runDir)
	if h2.Status != StatusStalled {
		t.Errorf("with 5m threshold: got %q, want stalled", h2.Status)
	}
}

// ---------------------------------------------------------------------------
// Unit: issueFromRunID
// ---------------------------------------------------------------------------

func TestIssueFromRunID(t *testing.T) {
	tests := []struct {
		id   string
		want int
	}{
		{"issue-13-20260718T102319Z", 13},
		{"issue-1-20260718T102319Z", 1},
		{"issue-100-20260718T000000Z", 100},
		{"not-a-run-id", 0},
		{"issue-abc-20260718T000000Z", 0},
	}
	for _, tc := range tests {
		got := issueFromRunID(tc.id)
		if got != tc.want {
			t.Errorf("issueFromRunID(%q): got %d, want %d", tc.id, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Unit: fmtDuration
// ---------------------------------------------------------------------------

func TestFmtDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{-time.Minute, "0s"},
	}
	for _, tc := range tests {
		got := fmtDuration(tc.d)
		if got != tc.want {
			t.Errorf("fmtDuration(%v): got %q, want %q", tc.d, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Unit: timestampSuffix sorting
// ---------------------------------------------------------------------------

func TestTimestampSuffix(t *testing.T) {
	// Higher timestamp → newer run → should sort first (descending)
	a := timestampSuffix("issue-13-20260718T120000Z")
	b := timestampSuffix("issue-9-20260718T100000Z")
	if a <= b {
		t.Errorf("expected %q > %q", a, b)
	}
}

// ---------------------------------------------------------------------------
// Unit: No-writes proof (AC-007)
// ---------------------------------------------------------------------------

func TestClassify_NoWrites_AC007(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-10-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-5 * time.Minute)

	writeEvents(t, runDir, []string{runStartedLine(10, runID, startTS)})

	// Capture file info before
	before := fileInfoSnapshot(t, runDir)

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	_, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Capture file info after and compare
	after := fileInfoSnapshot(t, runDir)
	for name, bInfo := range before {
		aInfo, ok := after[name]
		if !ok {
			t.Errorf("file %q was deleted", name)
			continue
		}
		if bInfo.ModTime() != aInfo.ModTime() {
			t.Errorf("file %q was modified", name)
		}
		if bInfo.Size() != aInfo.Size() {
			t.Errorf("file %q changed size", name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("file %q was created", name)
		}
	}
}

func fileInfoSnapshot(t *testing.T, dir string) map[string]os.FileInfo {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	out := make(map[string]os.FileInfo, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		out[e.Name()] = info
	}
	return out
}

// ---------------------------------------------------------------------------
// Unit: JSON field set parity
// ---------------------------------------------------------------------------

func TestRunHealth_JSONFieldSet(t *testing.T) {
	pid := 12345
	h := RunHealth{
		RunID:         "issue-1-20260718T100000Z",
		Issue:         1,
		Status:        StatusRunning,
		CurrentPhase:  "agent.turn",
		AgeOrDuration: "5m",
		PID:           &pid,
		Liveness:      LivenessAlive,
	}
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	required := []string{"run_id", "issue", "status", "current_phase", "age_or_duration", "pid", "liveness"}
	for _, field := range required {
		if _, ok := m[field]; !ok {
			t.Errorf("JSON missing field %q", field)
		}
	}
}

// ---------------------------------------------------------------------------
// Unit: ClassifyAll over runs with same issue but different timestamps
// ---------------------------------------------------------------------------

func TestClassifyAll_SortByTimestamp_NotIssueNumber(t *testing.T) {
	runsDir := t.TempDir()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	// issue-9 started later than issue-13
	ids := []struct {
		id string
		ts time.Time
	}{
		{"issue-13-20260718T090000Z", now.Add(-3 * time.Hour)},
		{"issue-9-20260718T110000Z", now.Add(-1 * time.Hour)},
	}
	for _, x := range ids {
		runDir := filepath.Join(runsDir, x.id)
		writeEvents(t, runDir, []string{runStartedLine(1, x.id, x.ts)})
	}

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	runs, err := c.ClassifyAll(runsDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	// issue-9 (started later = newer) should be first
	if runs[0].RunID != "issue-9-20260718T110000Z" {
		t.Errorf("expected issue-9 first, got %q", runs[0].RunID)
	}
}

// ---------------------------------------------------------------------------
// Unit: indeterminate liveness probe
// ---------------------------------------------------------------------------

func TestClassify_IndeterminateLiveness(t *testing.T) {
	indeterminateProbe := func(_ int) string { return LivenessIndeterminate }

	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-11-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-5 * time.Minute)
	pid := 12345

	writeEvents(t, runDir, []string{runStartedLine(11, runID, startTS)})
	writeTelemetry(t, runDir, []string{
		spanStartLine("s1", "run", startTS.Format(time.RFC3339), map[string]interface{}{"pid": pid}),
	})

	c := &Classifier{Probe: indeterminateProbe, Now: fixedNow(now)}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Status != StatusIndeterminate {
		t.Errorf("status: got %q, want %q", h.Status, StatusIndeterminate)
	}
	if h.Liveness != LivenessIndeterminate {
		t.Errorf("liveness: got %q, want %q", h.Liveness, LivenessIndeterminate)
	}
}

// ---------------------------------------------------------------------------
// Unit: ReadTelemetry with no pid → telemetryData has nil PID
// ---------------------------------------------------------------------------

func TestReadTelemetry_NoPid(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	startTS := now.Add(-1 * time.Minute).Format(time.RFC3339)
	runDir := filepath.Join(dir, "issue-1-20260718T100000Z")
	// run span has no pid attribute
	writeTelemetry(t, runDir, []string{
		spanStartLine("s1", "run", startTS, nil),
	})

	tel := readTelemetry(filepath.Join(runDir, "telemetry.jsonl"))
	if tel == nil {
		t.Fatal("expected telemetry data, got nil")
	}
	if tel.RunSpanPID != nil {
		t.Errorf("expected nil PID, got %d", *tel.RunSpanPID)
	}
}

// Verify that a run with telemetry but no pid falls back to indeterminate (BR-007).
func TestClassify_TelemetryNoPid_Indeterminate(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	runID := "issue-12-20260718T100000Z"
	runDir := filepath.Join(dir, runID)
	startTS := now.Add(-5 * time.Minute)

	writeEvents(t, runDir, []string{runStartedLine(12, runID, startTS)})
	writeTelemetry(t, runDir, []string{
		spanStartLine("s1", "run", startTS.Format(time.RFC3339), nil), // no pid
	})

	c := &Classifier{Probe: deadProbe, Now: fixedNow(now)}
	h, err := c.ClassifyOne(runDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Liveness != LivenessIndeterminate {
		t.Errorf("liveness: got %q, want %q", h.Liveness, LivenessIndeterminate)
	}
	if h.PID != nil {
		t.Errorf("expected nil PID")
	}
}

// ---------------------------------------------------------------------------

