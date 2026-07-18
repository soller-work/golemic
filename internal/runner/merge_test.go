package runner

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
)

// ---------------------------------------------------------------------------
// riskLabelFromIssue unit tests
// ---------------------------------------------------------------------------

func TestRiskLabelFromIssue_LowOnly(t *testing.T) {
	labels := []issueLabel{{Name: "risk:low"}}
	if got := riskLabelFromIssue(labels); got != "risk:low" {
		t.Errorf("got %q, want %q", got, "risk:low")
	}
}

func TestRiskLabelFromIssue_MediumOnly(t *testing.T) {
	labels := []issueLabel{{Name: "risk:medium"}}
	if got := riskLabelFromIssue(labels); got != "risk:medium" {
		t.Errorf("got %q, want %q", got, "risk:medium")
	}
}

func TestRiskLabelFromIssue_HighOnly(t *testing.T) {
	labels := []issueLabel{{Name: "risk:high"}}
	if got := riskLabelFromIssue(labels); got != "risk:high" {
		t.Errorf("got %q, want %q", got, "risk:high")
	}
}

func TestRiskLabelFromIssue_Absent(t *testing.T) {
	labels := []issueLabel{{Name: "bug"}, {Name: "enhancement"}}
	if got := riskLabelFromIssue(labels); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRiskLabelFromIssue_NoLabels(t *testing.T) {
	if got := riskLabelFromIssue(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRiskLabelFromIssue_HighWinsOverLow(t *testing.T) {
	labels := []issueLabel{{Name: "risk:low"}, {Name: "risk:high"}}
	if got := riskLabelFromIssue(labels); got != "risk:high" {
		t.Errorf("most restrictive should win; got %q, want %q", got, "risk:high")
	}
}

func TestRiskLabelFromIssue_MediumWinsOverLow(t *testing.T) {
	labels := []issueLabel{{Name: "risk:low"}, {Name: "risk:medium"}}
	if got := riskLabelFromIssue(labels); got != "risk:medium" {
		t.Errorf("most restrictive should win; got %q, want %q", got, "risk:medium")
	}
}

// ---------------------------------------------------------------------------
// DT-001 gate evaluation — all six rows (AC-001 to AC-006)
// ---------------------------------------------------------------------------

// writeReviewEventForMerge writes a review_submitted event with verdict and confidence.
func writeReviewEventForMerge(t *testing.T, logPath, verdict, confidence string) {
	t.Helper()
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer w.Close() //nolint:errcheck
	payload, _ := json.Marshal(map[string]string{
		"verdict":         verdict,
		"mergeConfidence": confidence,
	})
	if err := w.Write(eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "test-run",
		Payload: payload,
	}); err != nil {
		t.Fatalf("write event: %v", err)
	}
}

func makeRunnerWithLabels(labels []issueLabel) *Runner {
	return &Runner{
		issue: &issueData{Labels: labels},
	}
}

// DT-001 row 1: approved + high + risk:low → proceed
func TestEvaluateAutoMergeGate_RiskLow_Proceeds_AC001(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "high")
	r := makeRunnerWithLabels([]issueLabel{{Name: "risk:low"}})
	proceed, reason := r.evaluateAutoMergeGate(logPath)
	if !proceed {
		t.Errorf("expected proceed, got skip with reason %q", reason)
	}
}

// DT-001 row 2: approved + high + risk:medium → proceed
func TestEvaluateAutoMergeGate_RiskMedium_Proceeds_AC002(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "high")
	r := makeRunnerWithLabels([]issueLabel{{Name: "risk:medium"}})
	proceed, reason := r.evaluateAutoMergeGate(logPath)
	if !proceed {
		t.Errorf("expected proceed, got skip with reason %q", reason)
	}
}

// DT-001 row 3: approved + high + risk:high → skip
func TestEvaluateAutoMergeGate_RiskHigh_Skips_AC003(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "high")
	r := makeRunnerWithLabels([]issueLabel{{Name: "risk:high"}})
	proceed, reason := r.evaluateAutoMergeGate(logPath)
	if proceed {
		t.Error("expected skip, got proceed")
	}
	if reason != "risk:high" {
		t.Errorf("reason: got %q, want %q", reason, "risk:high")
	}
}

// DT-001 row 4: approved + high + absent label → skip with "no risk label"
func TestEvaluateAutoMergeGate_NoRiskLabel_Skips_AC004(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "high")
	r := makeRunnerWithLabels([]issueLabel{{Name: "bug"}})
	proceed, reason := r.evaluateAutoMergeGate(logPath)
	if proceed {
		t.Error("expected skip, got proceed")
	}
	if reason != "no risk label" {
		t.Errorf("reason: got %q, want %q", reason, "no risk label")
	}
}

// DT-001 row 5: approved + low + any → skip with "confidence low"
func TestEvaluateAutoMergeGate_ConfidenceLow_Skips_AC005(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "low")
	r := makeRunnerWithLabels([]issueLabel{{Name: "risk:low"}})
	proceed, reason := r.evaluateAutoMergeGate(logPath)
	if proceed {
		t.Error("expected skip, got proceed")
	}
	if reason != "confidence low" {
		t.Errorf("reason: got %q, want %q", reason, "confidence low")
	}
}

// DT-001 row 4 (conflicting): risk:low + risk:high → most restrictive = risk:high → skip
func TestEvaluateAutoMergeGate_ConflictingLabels_MostRestrictiveWins(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "high")
	r := makeRunnerWithLabels([]issueLabel{{Name: "risk:low"}, {Name: "risk:high"}})
	proceed, reason := r.evaluateAutoMergeGate(logPath)
	if proceed {
		t.Error("expected skip, got proceed")
	}
	if reason != "risk:high" {
		t.Errorf("reason: got %q, want %q", reason, "risk:high")
	}
}

// ---------------------------------------------------------------------------
// latestMergeConfidence unit tests
// ---------------------------------------------------------------------------

func TestLatestMergeConfidence_High(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "high")
	r := &Runner{}
	conf, err := r.latestMergeConfidence(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf != "high" {
		t.Errorf("got %q, want %q", conf, "high")
	}
}

func TestLatestMergeConfidence_Low(t *testing.T) {
	logPath := newLogPath(t)
	writeReviewEventForMerge(t, logPath, "approved", "low")
	r := &Runner{}
	conf, err := r.latestMergeConfidence(logPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conf != "low" {
		t.Errorf("got %q, want %q", conf, "low")
	}
}

func TestLatestMergeConfidence_MissingEvent(t *testing.T) {
	logPath := newLogPath(t)
	r := &Runner{}
	_, err := r.latestMergeConfidence(logPath)
	if err == nil {
		t.Fatal("expected error for missing event log")
	}
}

// ---------------------------------------------------------------------------
// nopWriter — a worktree.EventWriter that discards all events (test helper)
// ---------------------------------------------------------------------------

type nopWriter struct{}

func (nopWriter) Write(eventlog.Event) error { return nil }

// ---------------------------------------------------------------------------
// Merge phase: up-to-date shortcut skips rebase, verify, push (BR-003)
// ---------------------------------------------------------------------------

func TestRunMergePhase_UpToDate_SquashMerges(t *testing.T) { //nolint:cyclop
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 42)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	var calls []string
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			calls = append(calls, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
			if name == "git" && len(args) >= 2 && args[0] == "merge-base" {
				return "", nil // is-ancestor check passes → up to date
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			calls = append(calls, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return "", nil // not called on up-to-date path, but just in case
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "abc123\n", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:  exec,
		issueNum:  5,
		runID:     "test-run",
		repoRoot:  "/repo",
		homeDir:   t.TempDir(),
		issue:     &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:       &config.Config{Project: "proj"},
		creds:     mustLoadCreds(t),
		branchName: "golemic/issue-5",
	}

	outcome := r.runMergePhase(nopWriter{}, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	// Verify no push was made (up-to-date shortcut)
	for _, c := range calls {
		if strings.Contains(c, "push") {
			t.Errorf("push should not happen on up-to-date path, got call: %q", c)
		}
		if strings.Contains(c, "rebase") {
			t.Errorf("rebase should not happen on up-to-date path, got call: %q", c)
		}
	}
}

// ---------------------------------------------------------------------------
// Merge phase: skip path writes automerge_skipped and returns success (BR-008)
// ---------------------------------------------------------------------------

func TestRunMergePhase_RiskHigh_WritesAutomergeSkipped(t *testing.T) {
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 10)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	r := &Runner{
		executor:  &fakeExecutor{},
		issueNum:  10,
		runID:     "test-run",
		repoRoot:  "/repo",
		homeDir:   t.TempDir(),
		issue:     &issueData{Labels: []issueLabel{{Name: "risk:high"}}},
		cfg:       &config.Config{Project: "proj"},
		creds:     mustLoadCreds(t),
		branchName: "golemic/issue-10",
	}

	var written []eventlog.Event
	w := &recordingWriter{events: &written}

	outcome := r.runMergePhase(w, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q (skip must be success)", outcome, outcomeSuccess)
	}

	var found bool
	for _, ev := range written {
		if ev.Type == eventlog.EventAutomergeSkipped {
			found = true
			var payload map[string]string
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if payload["reason"] != "risk:high" {
				t.Errorf("reason: got %q, want %q", payload["reason"], "risk:high")
			}
		}
	}
	if !found {
		t.Error("automerge_skipped event not written")
	}
}

func TestRunMergePhase_NoRiskLabel_WritesAutomergeSkipped(t *testing.T) {
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 11)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	r := &Runner{
		executor:  &fakeExecutor{},
		issueNum:  11,
		runID:     "test-run",
		repoRoot:  "/repo",
		homeDir:   t.TempDir(),
		issue:     &issueData{Labels: []issueLabel{{Name: "bug"}}},
		cfg:       &config.Config{Project: "proj"},
		creds:     mustLoadCreds(t),
		branchName: "golemic/issue-11",
	}

	var written []eventlog.Event
	w := &recordingWriter{events: &written}
	outcome := r.runMergePhase(w, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	found := false
	for _, ev := range written {
		if ev.Type == eventlog.EventAutomergeSkipped {
			found = true
			var payload map[string]string
			_ = json.Unmarshal(ev.Payload, &payload)
			if payload["reason"] != "no risk label" {
				t.Errorf("reason: got %q, want %q", payload["reason"], "no risk label")
			}
		}
	}
	if !found {
		t.Error("automerge_skipped event not written")
	}
}

func TestRunMergePhase_ConfidenceLow_WritesAutomergeSkipped(t *testing.T) {
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 12)
	writeReviewEventForMerge(t, logPath, "approved", "low")

	r := &Runner{
		executor:  &fakeExecutor{},
		issueNum:  12,
		runID:     "test-run",
		repoRoot:  "/repo",
		homeDir:   t.TempDir(),
		issue:     &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:       &config.Config{Project: "proj"},
		creds:     mustLoadCreds(t),
		branchName: "golemic/issue-12",
	}

	var written []eventlog.Event
	w := &recordingWriter{events: &written}
	outcome := r.runMergePhase(w, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	found := false
	for _, ev := range written {
		if ev.Type == eventlog.EventAutomergeSkipped {
			found = true
			var payload map[string]string
			_ = json.Unmarshal(ev.Payload, &payload)
			if payload["reason"] != "confidence low" {
				t.Errorf("reason: got %q, want %q", payload["reason"], "confidence low")
			}
		}
	}
	if !found {
		t.Error("automerge_skipped event not written")
	}
}

// ---------------------------------------------------------------------------
// Merge phase: rebase conflict → automerge_failed + merge_failed (BR-005, AC-006)
// ---------------------------------------------------------------------------

func TestRunMergePhase_RebaseConflict_AutomergeFailed_AC006(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 20)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	var abortCalled bool
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) >= 2 && args[0] == "merge-base" {
				return "", fmt.Errorf("exit 1") // not up to date
			}
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "rebase" && len(args) >= 2 && args[1] == "origin/main" {
				return "", fmt.Errorf("CONFLICT (content): merge conflict in foo.go")
			}
			if name == "git" && args[0] == "rebase" && len(args) >= 2 && args[1] == "--abort" {
				abortCalled = true
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			// PR comment
			if name == "gh" && args[0] == "pr" && args[1] == "comment" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:  exec,
		issueNum:  20,
		runID:     "test-run",
		repoRoot:  "/repo",
		homeDir:   t.TempDir(),
		issue:     &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:       &config.Config{Project: "proj"},
		creds:     mustLoadCreds(t),
		branchName: "golemic/issue-20",
		stderr:    &strings.Builder{},
	}
	buf := &strings.Builder{}
	r.stderr = buf

	var written []eventlog.Event
	w := &recordingWriter{events: &written}
	outcome := r.runMergePhase(w, logPath)

	if outcome != outcomeMergeFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeMergeFailed)
	}
	if !abortCalled {
		t.Error("git rebase --abort should have been called on conflict (BR-005)")
	}
	found := false
	for _, ev := range written {
		if ev.Type == eventlog.EventAutomergeFailed {
			found = true
		}
	}
	if !found {
		t.Error("automerge_failed event not written")
	}
}

// ---------------------------------------------------------------------------
// Merge phase: gh pr merge failure → automerge_failed (BR-006, AC-011)
// ---------------------------------------------------------------------------

func TestRunMergePhase_MergeFailure_AutomergeFailed_AC011(t *testing.T) { //nolint:cyclop
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 30)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "merge-base" {
				return "", nil // up to date → skip rebase/push
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "", fmt.Errorf("gh pr merge failed: branch protection required")
			}
			if name == "gh" && args[0] == "pr" && args[1] == "comment" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:  exec,
		issueNum:  30,
		runID:     "test-run",
		repoRoot:  "/repo",
		homeDir:   t.TempDir(),
		issue:     &issueData{Labels: []issueLabel{{Name: "risk:medium"}}},
		cfg:       &config.Config{Project: "proj"},
		creds:     mustLoadCreds(t),
		branchName: "golemic/issue-30",
	}
	buf := &strings.Builder{}
	r.stderr = buf

	var written []eventlog.Event
	w := &recordingWriter{events: &written}
	outcome := r.runMergePhase(w, logPath)

	if outcome != outcomeMergeFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeMergeFailed)
	}
	found := false
	for _, ev := range written {
		if ev.Type == eventlog.EventAutomergeFailed {
			found = true
		}
	}
	if !found {
		t.Error("automerge_failed event not written")
	}
}

// ---------------------------------------------------------------------------
// Cleanup: runs on success and skip, not on merge_failed (BR-006, BR-008)
// ---------------------------------------------------------------------------

// This is tested implicitly via Run() — the outcome determines cleanup.
// The unit-level test verifies the outcome constants are correct.
func TestOutcomeMergeFailed_ConstantValue(t *testing.T) {
	if outcomeMergeFailed != "merge_failed" {
		t.Errorf("outcomeMergeFailed: got %q, want %q", outcomeMergeFailed, "merge_failed")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type recordingWriter struct {
	events *[]eventlog.Event
}

func (w *recordingWriter) Write(ev eventlog.Event) error {
	*w.events = append(*w.events, ev)
	return nil
}

func mustLoadCreds(t *testing.T) *credentials.Credentials { //nolint:unused
	t.Helper()
	homeDir, _, project := setupRunnerTest(t)
	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	return creds
}
