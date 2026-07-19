package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
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

func TestRunMergePhase_UpToDate_SquashMerges(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 42)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	var calls []string
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			calls = append(calls, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && len(args) >= 2 && args[0] == "merge-base" {
				return "", nil // is-ancestor check passes → up to date
			}
			if name == "git" && args[0] == "ls-remote" {
				return "", nil // branch already gone; skip delete push
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			calls = append(calls, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return `[{"name":"verify","bucket":"pass","link":""}]`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "abc123\n", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:               exec,
		issueNum:               5,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                t.TempDir(),
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:                    &config.Config{Project: "proj"},
		creds:                  mustLoadCreds(t),
		branchName:             "golemic/issue-5",
		stderr:                 &strings.Builder{},
		ciTimeoutOverride:      100 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
	}

	outcome := r.runMergePhase(nopWriter{}, logPath)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	// Verify no push or rebase was made (up-to-date path)
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
		executor:   &fakeExecutor{},
		issueNum:   10,
		runID:      "test-run",
		repoRoot:   "/repo",
		homeDir:    t.TempDir(),
		issue:      &issueData{Labels: []issueLabel{{Name: "risk:high"}}},
		cfg:        &config.Config{Project: "proj"},
		creds:      mustLoadCreds(t),
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
		executor:   &fakeExecutor{},
		issueNum:   11,
		runID:      "test-run",
		repoRoot:   "/repo",
		homeDir:    t.TempDir(),
		issue:      &issueData{Labels: []issueLabel{{Name: "bug"}}},
		cfg:        &config.Config{Project: "proj"},
		creds:      mustLoadCreds(t),
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

// TestRunMergePhase_ConfidenceLow_WritesAutomergeSkipped also asserts that
// gh pr edit --add-label confidence:low is called (P3-1 / AC-005).
func TestRunMergePhase_ConfidenceLow_WritesAutomergeSkipped(t *testing.T) { //nolint:cyclop
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 12)
	writeReviewEventForMerge(t, logPath, "approved", "low")

	var labelCallArgs []string
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "edit" {
				labelCallArgs = args
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:   exec,
		issueNum:   12,
		runID:      "test-run",
		repoRoot:   "/repo",
		homeDir:    t.TempDir(),
		issue:      &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:        &config.Config{Project: "proj"},
		creds:      mustLoadCreds(t),
		branchName: "golemic/issue-12",
		stderr:     &strings.Builder{},
	}

	var written []eventlog.Event
	w := &recordingWriter{events: &written}
	outcome := r.runMergePhase(w, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	// Assert the label was set (P3-1 / AC-005)
	if len(labelCallArgs) == 0 {
		t.Error("gh pr edit --add-label confidence:low was not called")
	} else {
		wantArgs := []string{"pr", "edit", "12", "--add-label", "confidence:low"}
		gotJoined := strings.Join(labelCallArgs, " ")
		wantJoined := strings.Join(wantArgs, " ")
		if gotJoined != wantJoined {
			t.Errorf("gh pr edit args: got %q, want %q", gotJoined, wantJoined)
		}
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
				return "", &preflight.ErrExit{ExitCode: 1} // not an ancestor
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
		executor:   exec,
		issueNum:   20,
		runID:      "test-run",
		repoRoot:   "/repo",
		homeDir:    t.TempDir(),
		issue:      &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:        &config.Config{Project: "proj"},
		creds:      mustLoadCreds(t),
		branchName: "golemic/issue-20",
		stderr:     &strings.Builder{},
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
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil // up to date
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return `[{"name":"verify","bucket":"pass","link":""}]`, nil
			}
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
		executor:               exec,
		issueNum:               30,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                t.TempDir(),
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:medium"}}},
		cfg:                    &config.Config{Project: "proj"},
		creds:                  mustLoadCreds(t),
		branchName:             "golemic/issue-30",
		ciTimeoutOverride:      100 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
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

// ---------------------------------------------------------------------------
// verifyAndPush unit tests — PS-003 branches (AC-001, AC-007, AC-008)
// ---------------------------------------------------------------------------

// makeVerifyRunner builds a minimal Runner for verifyAndPush tests.
func makeVerifyRunner(t *testing.T, exec *fakeExecutor, verifyCmds ...string) *Runner {
	t.Helper()
	homeDir := t.TempDir()
	project := "proj"
	credDir := mkCredDir(t, homeDir, project)
	_ = credDir

	verifyCmd := "echo ok"
	if len(verifyCmds) > 0 {
		verifyCmd = verifyCmds[0]
	}

	creds := mustLoadCredsFromDir(t, homeDir, project)

	r := &Runner{
		executor:               exec,
		issueNum:               50,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                homeDir,
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:                    &config.Config{Project: project, VerifyCommand: verifyCmd},
		creds:                  creds,
		branchName:             "golemic/issue-50",
		ciTimeoutOverride:      100 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
	}
	buf := &strings.Builder{}
	r.stderr = buf
	return r
}

func mkCredDir(t *testing.T, homeDir, project string) string {
	t.Helper()
	d := homeDir + "/.golemic/" + project
	if err := os.MkdirAll(d, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d+"/credentials.json",
		[]byte(`{"dev_token":"ghp_dev","reviewer_token":"ghp_rev"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return d
}

func mustLoadCredsFromDir(t *testing.T, homeDir, project string) *credentials.Credentials {
	t.Helper()
	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	return creds
}

// AC-001: branch has CI checks; after push checks turn green → squash merge succeeds.
func TestVerifyAndPush_GreenCI_MergesSuccessfully_AC001(t *testing.T) { //nolint:cyclop
	checksCall := 0
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "checks":
				checksCall++
				if checksCall == 1 {
					// Initial query: pending
					return `[{"name":"build","bucket":"waiting","link":""}]`, nil
				}
				// Subsequent polls: green
				return `[{"name":"build","bucket":"pass","link":""}]`, nil
			case name == "git" && len(args) >= 1 && args[0] == "push":
				return "", nil
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "merge":
				return "sha-abc", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := makeVerifyRunner(t, exec)
	var written []eventlog.Event
	outcome := r.verifyAndPush(&recordingWriter{events: &written}, 50, t.TempDir())

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	found := false
	for _, ev := range written {
		if ev.Type == eventlog.EventPRMerged {
			found = true
		}
	}
	if !found {
		t.Error("pr_merged event not written")
	}
	if checksCall < 2 {
		t.Errorf("expected at least 2 gh pr checks calls (initial + poll); got %d", checksCall)
	}
}

// AC-007: CI checks fail after the rebase push → automerge_failed, no merge.
func TestVerifyAndPush_RedCI_AfterPush_MergeFailed_AC007(t *testing.T) { //nolint:cyclop
	checksCall := 0
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "checks":
				checksCall++
				if checksCall == 1 {
					return `[{"name":"build","bucket":"waiting","link":""}]`, nil // pending → has CI
				}
				return `[{"name":"build","bucket":"fail","link":""}]`, nil // red after push
			case name == "git" && len(args) >= 1 && args[0] == "push":
				return "", nil
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "comment":
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := makeVerifyRunner(t, exec)
	var written []eventlog.Event
	outcome := r.verifyAndPush(&recordingWriter{events: &written}, 50, t.TempDir())

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
	// Merge must not have been attempted
	for _, ev := range written {
		if ev.Type == eventlog.EventPRMerged {
			t.Error("pr_merged must not be written when CI is red")
		}
	}
}

// AC-008 (success): no CI configured → verify_command passes → push → squash merge.
func TestVerifyAndPush_NoCI_VerifyPasses_MergesSuccessfully_AC008(t *testing.T) { //nolint:cyclop
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			// verify_command runs via sh -c (P1-1)
			if name == "sh" && len(args) == 2 && args[0] == "-c" {
				return "ok", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "checks":
				// no_checks: gh exits 1 with the exact message
				return "", noChecksErr()
			case name == "git" && len(args) >= 1 && args[0] == "push":
				return "", nil
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "merge":
				return "sha-abc", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := makeVerifyRunner(t, exec, "echo ok")
	var written []eventlog.Event
	outcome := r.verifyAndPush(&recordingWriter{events: &written}, 50, t.TempDir())

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	found := false
	for _, ev := range written {
		if ev.Type == eventlog.EventPRMerged {
			found = true
		}
	}
	if !found {
		t.Error("pr_merged event not written")
	}
}

// AC-008 (failure): no CI configured → verify_command fails → no push, automerge_failed.
func TestVerifyAndPush_NoCI_VerifyFails_MergeFailed_AC008(t *testing.T) { //nolint:cyclop
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			// verify_command runs via sh -c (P1-1)
			if name == "sh" && len(args) == 2 && args[0] == "-c" {
				return "", fmt.Errorf("exit status 1")
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "checks":
				return "", noChecksErr()
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "comment":
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := makeVerifyRunner(t, exec, "false")
	var written []eventlog.Event
	outcome := r.verifyAndPush(&recordingWriter{events: &written}, 50, t.TempDir())

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
	// No push should have been issued
	for _, ev := range written {
		if ev.Type == eventlog.EventPRMerged {
			t.Error("pr_merged must not be written when verify_command fails")
		}
	}
}

// noChecksErr returns the exact error that gh emits for a PR with no CI checks.
// queryCIChecks uses errors.As to detect *preflight.ErrExit, so we must return
// the real struct, not a custom interface.
func noChecksErr() error {
	return &preflight.ErrExit{ExitCode: 1, Stderr: "no checks reported on the 'golemic/issue-50' branch"}
}

// ---------------------------------------------------------------------------
// runVerifyCommand unit tests (P1-1)
// ---------------------------------------------------------------------------

// TestRunVerifyCommand_CompoundCommand_ExecutedViaShell verifies that compound
// shell commands (using &&) are passed intact to sh -c, not split by strings.Fields.
func TestRunVerifyCommand_CompoundCommand_ExecutedViaShell(t *testing.T) {
	var gotName string
	var gotArgs []string
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			gotName = name
			gotArgs = args
			return "", nil
		},
	}
	r := &Runner{
		executor: exec,
		cfg:      &config.Config{VerifyCommand: "echo a && echo b"},
	}
	if err := r.runVerifyCommand("/tmp"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotName != "sh" {
		t.Errorf("executor called with %q, want %q", gotName, "sh")
	}
	if len(gotArgs) != 2 || gotArgs[0] != "-c" || gotArgs[1] != "echo a && echo b" {
		t.Errorf("executor args: got %v, want [-c 'echo a && echo b']", gotArgs)
	}
}

// ---------------------------------------------------------------------------
// isBranchUpToDate: non-exit-1 errors propagate (P2-2)
// ---------------------------------------------------------------------------

// TestRunMergePhase_FreshnessCheckNonExit1_AutomergeFailed verifies that a
// git merge-base error with exit code != 1 (e.g. bad revision) is propagated
// as automerge_failed without attempting rebase, push, or merge.
func TestRunMergePhase_FreshnessCheckNonExit1_AutomergeFailed(t *testing.T) { //nolint:cyclop,funlen,gocognit
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 40)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	var rebaseCalled, pushCalled bool
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) >= 1 && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && len(args) >= 1 && args[0] == "merge-base" {
				// exit code 2 = bad revision — must not be treated as "not ancestor"
				return "", &preflight.ErrExit{ExitCode: 2, Stderr: "bad revision 'origin/main'"}
			}
			if name == "git" && len(args) >= 1 && args[0] == "rebase" {
				rebaseCalled = true
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "git" && len(args) >= 1 && args[0] == "push" {
				pushCalled = true
			}
			if name == "gh" && args[0] == "pr" && args[1] == "comment" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:   exec,
		issueNum:   40,
		runID:      "test-run",
		repoRoot:   "/repo",
		homeDir:    t.TempDir(),
		issue:      &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:        &config.Config{Project: "proj"},
		creds:      mustLoadCreds(t),
		branchName: "golemic/issue-40",
		stderr:     &strings.Builder{},
	}

	var written []eventlog.Event
	outcome := r.runMergePhase(&recordingWriter{events: &written}, logPath)

	if outcome != outcomeMergeFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeMergeFailed)
	}
	if rebaseCalled {
		t.Error("rebase must not be called when freshness check fails with non-exit-1 error")
	}
	if pushCalled {
		t.Error("push must not be called when freshness check fails with non-exit-1 error")
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
// Issue #39: deleteRemoteBranch — AC-001 through AC-005
// ---------------------------------------------------------------------------

// AC-001: squashMerge must not pass --delete-branch to gh pr merge.
func TestSquashMerge_OmitsDeleteBranchFlag(t *testing.T) {
	creds := mustLoadCreds(t)

	var capturedArgs []string
	var capturedEnv map[string]string
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			capturedArgs = args
			capturedEnv = env
			return "sha-001", nil
		},
	}

	r := &Runner{
		executor: exec,
		repoRoot: "/repo",
		creds:    creds,
	}

	_, err := r.squashMerge(42)
	if err != nil {
		t.Fatalf("squashMerge returned unexpected error: %v", err)
	}

	for _, a := range capturedArgs {
		if a == "--delete-branch" {
			t.Error("squashMerge must not pass --delete-branch to gh pr merge")
		}
	}

	want := []string{"pr", "merge", "42", "--squash"}
	got := strings.Join(capturedArgs, " ")
	wantJoined := strings.Join(want, " ")
	if got != wantJoined {
		t.Errorf("gh args: got %q, want %q", got, wantJoined)
	}

	if capturedEnv["GH_TOKEN"] != creds.ReviewerToken() {
		t.Errorf("GH_TOKEN: got %q, want ReviewerToken %q", capturedEnv["GH_TOKEN"], creds.ReviewerToken())
	}
}

// AC-002: remote branch exists → deleteRemoteBranch calls ls-remote then push --delete.
func TestRunMergePhase_DeletesRemoteBranchAfterMerge(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 42)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	creds := mustLoadCreds(t)
	var deletePushEnv map[string]string
	var lsRemoteCalled, deletePushCalled bool

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil // up-to-date
			}
			if name == "git" && args[0] == "ls-remote" {
				lsRemoteCalled = true
				return "abc123\trefs/heads/golemic/issue-42\n", nil // branch exists
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return `[{"name":"verify","bucket":"pass","link":""}]`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "sha-002", nil
			}
			if name == "git" && args[0] == "push" && args[1] == "origin" && args[2] == "--delete" {
				deletePushCalled = true
				deletePushEnv = env
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:               exec,
		issueNum:               42,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                t.TempDir(),
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:                    &config.Config{Project: "proj"},
		creds:                  creds,
		branchName:             "golemic/issue-42",
		stderr:                 &strings.Builder{},
		ciTimeoutOverride:      100 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
	}

	var written []eventlog.Event
	outcome := r.runMergePhase(&recordingWriter{events: &written}, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if !lsRemoteCalled {
		t.Error("git ls-remote --heads must be called to check remote branch existence")
	}
	if !deletePushCalled {
		t.Error("git push origin --delete must be called when remote branch exists")
	}
	if deletePushEnv["GH_TOKEN"] != creds.DevToken() {
		t.Errorf("delete push GH_TOKEN: got %q, want DevToken %q", deletePushEnv["GH_TOKEN"], creds.DevToken())
	}

	// Verify pr_merged is present and no automerge_failed
	var prMergedFound bool
	for _, ev := range written {
		if ev.Type == eventlog.EventPRMerged {
			prMergedFound = true
		}
		if ev.Type == eventlog.EventAutomergeFailed {
			t.Error("automerge_failed must not be written on success path")
		}
	}
	if !prMergedFound {
		t.Error("pr_merged event not written")
	}
}

// AC-003: remote branch already gone → no push --delete, no warning.
func TestRunMergePhase_SkipsDeleteWhenRemoteAbsent(t *testing.T) { //nolint:cyclop
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 43)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	stderr := &strings.Builder{}
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil
			}
			if name == "git" && args[0] == "ls-remote" {
				return "", nil // empty → branch absent
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return `[{"name":"verify","bucket":"pass","link":""}]`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "sha-003", nil
			}
			if name == "git" && args[0] == "push" && args[1] == "origin" && args[2] == "--delete" {
				t.Error("git push --delete must not be called when remote branch is absent")
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:               exec,
		issueNum:               43,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                t.TempDir(),
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:                    &config.Config{Project: "proj"},
		creds:                  mustLoadCreds(t),
		branchName:             "golemic/issue-43",
		stderr:                 stderr,
		ciTimeoutOverride:      100 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
	}

	outcome := r.runMergePhase(nopWriter{}, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if strings.Contains(stderr.String(), "remote branch delete failed") {
		t.Errorf("no warning expected when remote branch is absent, got: %q", stderr.String())
	}
}

// AC-004: push --delete fails after successful merge → warning on stderr, outcome still success.
func TestRunMergePhase_WarnsOnRemoteDeleteFailure(t *testing.T) { //nolint:cyclop,funlen,gocognit
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 44)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	stderr := &strings.Builder{}
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil
			}
			if name == "git" && args[0] == "ls-remote" {
				return "abc123\trefs/heads/golemic/issue-44\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return `[{"name":"verify","bucket":"pass","link":""}]`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "sha-004", nil
			}
			if name == "git" && args[0] == "push" && args[1] == "origin" && args[2] == "--delete" {
				return "", fmt.Errorf("unable to delete remote branch: permission denied")
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:               exec,
		issueNum:               44,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                t.TempDir(),
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:                    &config.Config{Project: "proj"},
		creds:                  mustLoadCreds(t),
		branchName:             "golemic/issue-44",
		stderr:                 stderr,
		ciTimeoutOverride:      100 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
	}

	var written []eventlog.Event
	outcome := r.runMergePhase(&recordingWriter{events: &written}, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q — remote delete failure must not degrade outcome", outcome, outcomeSuccess)
	}
	if !strings.Contains(stderr.String(), "Warning: remote branch delete failed") {
		t.Errorf("expected 'Warning: remote branch delete failed' on stderr, got: %q", stderr.String())
	}

	var prMergedFound bool
	for _, ev := range written {
		if ev.Type == eventlog.EventPRMerged {
			prMergedFound = true
		}
		if ev.Type == eventlog.EventAutomergeFailed {
			t.Error("automerge_failed must not be written when only remote delete fails")
		}
	}
	if !prMergedFound {
		t.Error("pr_merged event not written")
	}
}

// AC-005: gate skip (confidence low) → neither squashMerge nor deleteRemoteBranch triggered.
func TestRunMergePhase_GateSkipDoesNotDeleteRemote(t *testing.T) { //nolint:cyclop
	logPath := newLogPath(t)
	writePROpenedEvent(t, logPath, 45)
	writeReviewEventForMerge(t, logPath, "approved", "low") // confidence=low → gate skip

	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "edit" {
				return "", nil // setConfidenceLowLabel
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				t.Error("squashMerge must not be called when gate skips")
			}
			if name == "git" && args[0] == "push" && args[1] == "origin" && args[2] == "--delete" {
				t.Error("deleteRemoteBranch must not be called when gate skips")
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "ls-remote" {
				t.Error("deleteRemoteBranch must not be called when gate skips")
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
	}

	r := &Runner{
		executor:   exec,
		issueNum:   45,
		runID:      "test-run",
		repoRoot:   "/repo",
		homeDir:    t.TempDir(),
		issue:      &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:        &config.Config{Project: "proj"},
		creds:      mustLoadCreds(t),
		branchName: "golemic/issue-45",
		stderr:     &strings.Builder{},
	}

	var written []eventlog.Event
	outcome := r.runMergePhase(&recordingWriter{events: &written}, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}

	var skippedFound bool
	for _, ev := range written {
		if ev.Type == eventlog.EventAutomergeSkipped {
			skippedFound = true
		}
	}
	if !skippedFound {
		t.Error("automerge_skipped event not written")
	}
}

// ---------------------------------------------------------------------------
// Issue #50: fetch + CI gate on up-to-date branch (AC-001 through AC-006)
// ---------------------------------------------------------------------------

// makeMergePhaseRunner builds a minimal Runner for runMergePhase tests with
// short CI timeouts so polling tests complete quickly.
func makeMergePhaseRunner(t *testing.T, exec *fakeExecutor, issueNum int, logPath string) (*Runner, *[]eventlog.Event) {
	t.Helper()
	homeDir := t.TempDir()
	project := "proj"
	mkCredDir(t, homeDir, project)
	creds := mustLoadCredsFromDir(t, homeDir, project)

	var written []eventlog.Event
	r := &Runner{
		executor:               exec,
		issueNum:               issueNum,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                homeDir,
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:medium"}}},
		cfg:                    &config.Config{Project: project},
		creds:                  creds,
		branchName:             fmt.Sprintf("golemic/issue-%d", issueNum),
		stderr:                 &strings.Builder{},
		ciTimeoutOverride:      200 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
	}
	writePROpenedEvent(t, logPath, issueNum)
	writeReviewEventForMerge(t, logPath, "approved", "high")
	return r, &written
}

// AC-001: git fetch origin fails → merge_failed with "git fetch origin failed:" reason;
// isBranchUpToDate and gh pr merge must never be called.
func TestRunMergePhase_FetchFails_MergeFailed_AC001(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)

	var mergeBaseCalled, ghMergeCalled bool
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", fmt.Errorf("network error: unable to reach origin")
			}
			if name == "git" && args[0] == "merge-base" {
				mergeBaseCalled = true
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				ghMergeCalled = true
			}
			if name == "gh" && args[0] == "pr" && args[1] == "comment" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r, eventsPtr := makeMergePhaseRunner(t, exec, 101, logPath)
	outcome := r.runMergePhase(&recordingWriter{events: eventsPtr}, logPath)

	if outcome != outcomeMergeFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeMergeFailed)
	}
	if mergeBaseCalled {
		t.Error("isBranchUpToDate must not be called when fetch fails (AC-001)")
	}
	if ghMergeCalled {
		t.Error("gh pr merge must not be called when fetch fails (AC-001)")
	}

	var found bool
	var reason string
	for _, ev := range *eventsPtr {
		if ev.Type == eventlog.EventAutomergeFailed {
			found = true
			var p map[string]string
			_ = json.Unmarshal(ev.Payload, &p)
			reason = p["reason"]
		}
	}
	if !found {
		t.Fatal("automerge_failed event not written")
	}
	if !strings.HasPrefix(reason, "git fetch origin failed:") {
		t.Errorf("reason: got %q, want prefix 'git fetch origin failed:'", reason)
	}
}

// AC-002: up-to-date branch + green CI → exactly one gh pr merge --squash, pr_merged event, outcomeSuccess;
// rebaseBranch and runVerifyCommand must not be invoked.
func TestRunMergePhase_UpToDate_GreenCI_Merges_AC002(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)

	var rebaseCalled bool
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil // up-to-date
			}
			if name == "git" && args[0] == "rebase" {
				rebaseCalled = true
				return "", fmt.Errorf("must not be called")
			}
			if name == "git" && args[0] == "ls-remote" {
				return "", nil // branch gone; skip delete
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return `[{"name":"verify","bucket":"pass","link":""}]`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "sha-ac002", nil
			}
			if name == "sh" {
				t.Error("runVerifyCommand must not be called on up-to-date CI-green path (AC-002)")
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r, eventsPtr := makeMergePhaseRunner(t, exec, 102, logPath)
	outcome := r.runMergePhase(&recordingWriter{events: eventsPtr}, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if rebaseCalled {
		t.Error("rebaseBranch must not be called on up-to-date path (AC-002)")
	}

	var prMergedFound bool
	for _, ev := range *eventsPtr {
		if ev.Type == eventlog.EventPRMerged {
			prMergedFound = true
		}
		if ev.Type == eventlog.EventAutomergeFailed {
			t.Error("automerge_failed must not be written on success path (AC-002)")
		}
	}
	if !prMergedFound {
		t.Error("pr_merged event not written (AC-002)")
	}
}

// AC-003: up-to-date branch + CI pending then green → pollCIChecks polls at least twice,
// then squash-merges and writes pr_merged.
func TestRunMergePhase_UpToDate_PendingThenGreen_Merges_AC003(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)

	checksCall := 0
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil // up-to-date
			}
			if name == "git" && args[0] == "ls-remote" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				checksCall++
				if checksCall == 1 {
					return `[{"name":"verify","bucket":"waiting","link":""}]`, nil
				}
				return `[{"name":"verify","bucket":"pass","link":""}]`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "sha-ac003", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r, eventsPtr := makeMergePhaseRunner(t, exec, 103, logPath)
	outcome := r.runMergePhase(&recordingWriter{events: eventsPtr}, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeSuccess)
	}
	if checksCall < 2 {
		t.Errorf("pollCIChecks must poll at least twice (pending→green); got %d calls", checksCall)
	}

	var found bool
	for _, ev := range *eventsPtr {
		if ev.Type == eventlog.EventPRMerged {
			found = true
		}
	}
	if !found {
		t.Error("pr_merged event not written (AC-003)")
	}
}

// AC-004: up-to-date branch + red CI → merge_failed with reason containing "CI checks failed" and check name.
func TestRunMergePhase_UpToDate_RedCI_MergeFailed_AC004(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil // up-to-date
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return `[{"name":"verify","bucket":"fail","link":""}]`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				t.Error("gh pr merge must not be called when CI is red (AC-004)")
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "comment" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r, eventsPtr := makeMergePhaseRunner(t, exec, 104, logPath)
	outcome := r.runMergePhase(&recordingWriter{events: eventsPtr}, logPath)

	if outcome != outcomeMergeFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeMergeFailed)
	}

	var found bool
	var reason string
	for _, ev := range *eventsPtr {
		if ev.Type == eventlog.EventAutomergeFailed {
			found = true
			var p map[string]string
			_ = json.Unmarshal(ev.Payload, &p)
			reason = p["reason"]
		}
	}
	if !found {
		t.Fatal("automerge_failed event not written (AC-004)")
	}
	if !strings.Contains(reason, "CI checks failed") {
		t.Errorf("reason: got %q, want it to contain 'CI checks failed' (AC-004)", reason)
	}
	if !strings.Contains(reason, "verify") {
		t.Errorf("reason: got %q, want it to contain check name 'verify' (AC-004)", reason)
	}
}

// AC-005: up-to-date branch + no CI checks → merge_failed with "required check not reported for PR head";
// runVerifyCommand and forcePushBranch must not be called.
func TestRunMergePhase_UpToDate_NoChecks_MergeFailed_AC005(t *testing.T) { //nolint:cyclop,gocognit
	logPath := newLogPath(t)

	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil
			}
			if name == "git" && args[0] == "merge-base" {
				return "", nil // up-to-date
			}
			if name == "sh" {
				t.Error("runVerifyCommand must not be called on up-to-date no_checks path (AC-005)")
				return "", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				return "", noChecksErr()
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				t.Error("gh pr merge must not be called when no checks reported (AC-005)")
				return "", nil
			}
			if name == "git" && args[0] == "push" {
				t.Error("forcePushBranch must not be called on up-to-date no_checks path (AC-005)")
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "comment" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	r, eventsPtr := makeMergePhaseRunner(t, exec, 105, logPath)
	outcome := r.runMergePhase(&recordingWriter{events: eventsPtr}, logPath)

	if outcome != outcomeMergeFailed {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeMergeFailed)
	}

	const wantReason = "required check not reported for PR head"
	var found bool
	var reason string
	for _, ev := range *eventsPtr {
		if ev.Type == eventlog.EventAutomergeFailed {
			found = true
			var p map[string]string
			_ = json.Unmarshal(ev.Payload, &p)
			reason = p["reason"]
		}
	}
	if !found {
		t.Fatal("automerge_failed event not written (AC-005)")
	}
	if reason != wantReason {
		t.Errorf("reason: got %q, want %q (AC-005)", reason, wantReason)
	}
}

// AC-006: behind branch (isBranchUpToDate=false after fetch) → rebaseBranch + verifyAndPush flow,
// and the existing AC-008 no_checks path in verifyAndPush still runs runVerifyCommand (BR-005 regression).
func TestRunMergePhase_BehindBranch_RebasesAndVerifies_AC006(t *testing.T) { //nolint:cyclop,gocognit,funlen
	logPath := newLogPath(t)

	homeDir := t.TempDir()
	project := "proj"
	mkCredDir(t, homeDir, project)
	creds := mustLoadCredsFromDir(t, homeDir, project)

	var rebaseCalled, verifyCommandCalled, forcePushCalled bool
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && args[0] == "fetch" {
				return "", nil // both the new top-level fetch and rebaseBranch's internal fetch
			}
			if name == "git" && args[0] == "merge-base" {
				return "", &preflight.ErrExit{ExitCode: 1} // behind origin/main
			}
			if name == "git" && args[0] == "rebase" && args[1] == "origin/main" {
				rebaseCalled = true
				return "", nil
			}
			if name == "sh" && args[0] == "-c" {
				verifyCommandCalled = true
				return "", nil // verify_command passes
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "checks" {
				// no_checks → verifyAndPush runs local verify_command (AC-008 / BR-005)
				return "", noChecksErr()
			}
			if name == "git" && args[0] == "push" {
				forcePushCalled = true
				return "", nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "merge" {
				return "sha-ac006", nil
			}
			if name == "git" && args[0] == "ls-remote" {
				return "", nil
			}
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		},
	}

	var written []eventlog.Event
	r := &Runner{
		executor:               exec,
		issueNum:               106,
		runID:                  "test-run",
		repoRoot:               "/repo",
		homeDir:                homeDir,
		issue:                  &issueData{Labels: []issueLabel{{Name: "risk:low"}}},
		cfg:                    &config.Config{Project: project, VerifyCommand: "echo ok"},
		creds:                  creds,
		branchName:             "golemic/issue-106",
		stderr:                 &strings.Builder{},
		ciTimeoutOverride:      200 * time.Millisecond,
		ciPollIntervalOverride: 1 * time.Millisecond,
	}
	writePROpenedEvent(t, logPath, 106)
	writeReviewEventForMerge(t, logPath, "approved", "high")

	outcome := r.runMergePhase(&recordingWriter{events: &written}, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q (AC-006)", outcome, outcomeSuccess)
	}
	if !rebaseCalled {
		t.Error("rebaseBranch must be called on behind-branch path (AC-006)")
	}
	if !verifyCommandCalled {
		t.Error("runVerifyCommand must be called in verifyAndPush no_checks path (AC-006 / BR-005)")
	}
	if !forcePushCalled {
		t.Error("forcePushBranch must be called after verify_command passes (AC-006 / BR-005)")
	}

	var prMergedFound bool
	for _, ev := range written {
		if ev.Type == eventlog.EventPRMerged {
			prMergedFound = true
		}
	}
	if !prMergedFound {
		t.Error("pr_merged event not written (AC-006)")
	}
}
