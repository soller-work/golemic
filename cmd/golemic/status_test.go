package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/health"
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// statusFixture sets up a temp home dir and a repo with config, returning the
// home dir, repo root, and a helper to create run directories.
func statusFixture(t *testing.T) (homeDir, repoRoot string, makeRun func(id string, events []string, telLines []string)) {
	t.Helper()
	homeDir = t.TempDir()
	repoRoot = t.TempDir()

	// Create minimal .golemic/config.json
	cfgDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := `{"project":"testproject","verify_command":"go test"}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatal(err)
	}
	runsBase := filepath.Join(homeDir, ".golemic", "testproject", "runs")

	makeRun = func(id string, events []string, telLines []string) {
		runDir := filepath.Join(runsBase, id)
		if err := os.MkdirAll(runDir, 0755); err != nil {
			t.Fatal(err)
		}
		if len(events) > 0 {
			content := strings.Join(events, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
		if len(telLines) > 0 {
			content := strings.Join(telLines, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(runDir, "telemetry.jsonl"), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return homeDir, repoRoot, makeRun
}

// statusRun calls runStatus directly, injecting a fake executor that returns
// repoRoot for git rev-parse.
func statusRun(t *testing.T, homeDir, repoRoot string, extraArgs ...string) (int, string, string) {
	t.Helper()

	// Temporarily override HOME so os.UserHomeDir returns our test homeDir.
	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HOME", origHome) }()

	exec := fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	args := append([]string{"golemic", "status"}, extraArgs...)
	var stdout, stderr bytes.Buffer
	code := runStatus(args, &stdout, &stderr, exec)
	return code, stdout.String(), stderr.String()
}

func eventsLine(evType, runID string, ts time.Time, payload map[string]interface{}) string {
	payloadBytes, _ := json.Marshal(payload)
	ev, _ := json.Marshal(map[string]interface{}{
		"type":    evType,
		"ts":      ts.Format(time.RFC3339),
		"runId":   runID,
		"payload": json.RawMessage(payloadBytes),
	})
	return string(ev)
}

// ---------------------------------------------------------------------------
// AC-001: Finished and failed runs classified (integration)
// ---------------------------------------------------------------------------

func TestStatusCmd_FinishedFailed_AC001(t *testing.T) {
	homeDir, repoRoot, makeRun := statusFixture(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	makeRun("issue-1-20260718T100000Z", []string{
		eventsLine("run_started", "issue-1-20260718T100000Z", now.Add(-2*time.Hour), map[string]interface{}{"issue": 1}),
		eventsLine("run_finished", "issue-1-20260718T100000Z", now.Add(-1*time.Hour), map[string]interface{}{"outcome": "success"}),
	}, nil)
	makeRun("issue-2-20260718T110000Z", []string{
		eventsLine("run_started", "issue-2-20260718T110000Z", now.Add(-1*time.Hour), map[string]interface{}{"issue": 2}),
		eventsLine("run_finished", "issue-2-20260718T110000Z", now.Add(-30*time.Minute), map[string]interface{}{"outcome": "dev_failed"}),
	}, nil)

	code, stdout, _ := statusRun(t, homeDir, repoRoot)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if !strings.Contains(stdout, "finished") {
		t.Errorf("expected 'finished' in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "failed") {
		t.Errorf("expected 'failed' in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "dev_failed") {
		t.Errorf("expected 'dev_failed' in output, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// AC-005: JSON parity and single-run form (integration)
// ---------------------------------------------------------------------------

func TestStatusCmd_JSONParity_AC005(t *testing.T) {
	homeDir, repoRoot, makeRun := statusFixture(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	makeRun("issue-1-20260718T100000Z", []string{
		eventsLine("run_started", "issue-1-20260718T100000Z", now.Add(-2*time.Hour), map[string]interface{}{"issue": 1}),
		eventsLine("run_finished", "issue-1-20260718T100000Z", now.Add(-1*time.Hour), map[string]interface{}{"outcome": "success"}),
	}, nil)

	code, stdout, _ := statusRun(t, homeDir, repoRoot, "--json")
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}

	var runs []health.RunHealth
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &runs); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nOutput:\n%s", err, stdout)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run in JSON, got %d", len(runs))
	}
	r := runs[0]
	if r.RunID == "" {
		t.Error("run_id is empty")
	}
	if r.Status != health.StatusFinished {
		t.Errorf("status: got %q, want %q", r.Status, health.StatusFinished)
	}
	if r.Liveness == "" {
		t.Error("liveness is empty")
	}
}

func TestStatusCmd_SingleRunForm_AC005(t *testing.T) {
	homeDir, repoRoot, makeRun := statusFixture(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	makeRun("issue-1-20260718T100000Z", []string{
		eventsLine("run_started", "issue-1-20260718T100000Z", now.Add(-1*time.Hour), map[string]interface{}{"issue": 1}),
		eventsLine("run_finished", "issue-1-20260718T100000Z", now.Add(-30*time.Minute), map[string]interface{}{"outcome": "success"}),
	}, nil)
	makeRun("issue-2-20260718T110000Z", []string{
		eventsLine("run_started", "issue-2-20260718T110000Z", now.Add(-30*time.Minute), map[string]interface{}{"issue": 2}),
	}, nil)

	code, stdout, _ := statusRun(t, homeDir, repoRoot, "issue-1-20260718T100000Z")
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if strings.Contains(stdout, "issue-2-20260718T110000Z") {
		t.Errorf("single-run form should not show other runs, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "issue-1-20260718T100000Z") {
		t.Errorf("expected run ID in output, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// AC-006: Empty RunsDir and missing runId (integration)
// ---------------------------------------------------------------------------

func TestStatusCmd_EmptyRunsDir_AC006(t *testing.T) {
	homeDir, repoRoot, _ := statusFixture(t)

	code, stdout, _ := statusRun(t, homeDir, repoRoot)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if !strings.Contains(stdout, "no runs found") {
		t.Errorf("expected 'no runs found', got:\n%s", stdout)
	}
}

func TestStatusCmd_EmptyRunsDir_JSON_AC006(t *testing.T) {
	homeDir, repoRoot, _ := statusFixture(t)

	code, stdout, _ := statusRun(t, homeDir, repoRoot, "--json")
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("expected '[]', got:\n%s", stdout)
	}
}

func TestStatusCmd_MissingRunId_AC006(t *testing.T) {
	homeDir, repoRoot, _ := statusFixture(t)

	code, _, stderr := statusRun(t, homeDir, repoRoot, "issue-99-20260718T999999Z")
	if code == 0 {
		t.Fatal("expected non-zero exit code for missing runId")
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("expected 'not found' in stderr, got:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// AC-007: No writes (integration)
// ---------------------------------------------------------------------------

func TestStatusCmd_NoWrites_AC007(t *testing.T) {
	homeDir, repoRoot, makeRun := statusFixture(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	makeRun("issue-1-20260718T100000Z", []string{
		eventsLine("run_started", "issue-1-20260718T100000Z", now.Add(-1*time.Hour), map[string]interface{}{"issue": 1}),
	}, nil)

	runsDir := filepath.Join(homeDir, ".golemic", "testproject", "runs")
	before := dirSnapshot(t, runsDir)

	statusRun(t, homeDir, repoRoot)

	after := dirSnapshot(t, runsDir)
	for path, bMtime := range before {
		aMtime, ok := after[path]
		if !ok {
			t.Errorf("file %q was deleted", path)
			continue
		}
		if !bMtime.Equal(aMtime) {
			t.Errorf("file %q was modified", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("file %q was created", path)
		}
	}
}

// dirSnapshot recursively records mtime for all files under dir.
func dirSnapshot(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	result := make(map[string]time.Time)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		result[path] = info.ModTime()
		return nil
	})
	if err != nil {
		t.Fatalf("dirSnapshot: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// Integration: newest-first table ordering
// ---------------------------------------------------------------------------

func TestStatusCmd_NewestFirst(t *testing.T) {
	homeDir, repoRoot, makeRun := statusFixture(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	makeRun("issue-1-20260718T090000Z", []string{
		eventsLine("run_started", "issue-1-20260718T090000Z", now.Add(-3*time.Hour), map[string]interface{}{"issue": 1}),
	}, nil)
	makeRun("issue-1-20260718T110000Z", []string{
		eventsLine("run_started", "issue-1-20260718T110000Z", now.Add(-1*time.Hour), map[string]interface{}{"issue": 1}),
	}, nil)

	code, stdout, _ := statusRun(t, homeDir, repoRoot)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	// First data line (after header) should be the newer run
	found := false
	for i, line := range lines {
		if strings.Contains(line, "issue-1-20260718T110000Z") {
			if i < 2 {
				found = true
			}
			break
		}
	}
	if !found {
		t.Errorf("expected newer run on first data line; output:\n%s", stdout)
	}

	idx1 := strings.Index(stdout, "issue-1-20260718T110000Z")
	idx2 := strings.Index(stdout, "issue-1-20260718T090000Z")
	if idx1 >= idx2 {
		t.Errorf("expected newer run before older run; output:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Integration: --stalled-after flag
// ---------------------------------------------------------------------------

func TestStatusCmd_StalledAfterFlag(t *testing.T) {
	homeDir, repoRoot, makeRun := statusFixture(t)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	runID := "issue-5-20260718T100000Z"
	startTS := now.Add(-10 * time.Minute)
	spanTS := now.Add(-6 * time.Minute)
	pid := 99999 // definitely dead in test; we check stdout regardless

	makeRun(runID, []string{
		eventsLine("run_started", runID, startTS, map[string]interface{}{"issue": 5}),
	}, []string{
		fmt.Sprintf(`{"kind":"span.start","trace_id":"abc","span_id":"s1","name":"run","start_ts":%q,"attributes":{"pid":%d}}`, startTS.Format(time.RFC3339), pid),
		fmt.Sprintf(`{"kind":"span.start","trace_id":"abc","span_id":"s2","name":"agent.turn","start_ts":%q}`, spanTS.Format(time.RFC3339)),
	})

	// With a long stalled-after (1h) → should not be stalled (6m < 1h)
	code, stdout, _ := statusRun(t, homeDir, repoRoot, "--stalled-after", "1h")
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	if strings.Contains(stdout, "stalled") {
		t.Errorf("expected no stalled with 1h threshold, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Integration: runId path separator check
// ---------------------------------------------------------------------------

func TestStatusCmd_PathSeparatorInRunId(t *testing.T) {
	homeDir, repoRoot, _ := statusFixture(t)

	code, _, stderr := statusRun(t, homeDir, repoRoot, "../../etc/passwd")
	if code == 0 {
		t.Fatal("expected non-zero exit code for runId with path separators")
	}
	if !strings.Contains(stderr, "path separators") {
		t.Errorf("expected path separator error, got:\n%s", stderr)
	}
}
