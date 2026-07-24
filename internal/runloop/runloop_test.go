package runloop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// ---------------------------------------------------------------------------
// Mock executor
// ---------------------------------------------------------------------------

type execCall struct {
	dir  string
	name string
	args []string
	env  map[string]string
}

type mockExecutor struct {
	mu    sync.Mutex
	calls []execCall

	runFunc               func(name string, args ...string) (string, error)
	runWithEnvFunc        func(env map[string]string, name string, args ...string) (string, error)
	runInDirFunc          func(dir, name string, args ...string) (string, error)
	runWithEnvInDirFunc   func(env map[string]string, dir, name string, args ...string) (string, error)
	startWithEnvInDirFunc func(env map[string]string, dir, name string, args ...string) (ProcessHandle, error)
}

func (e *mockExecutor) record(dir, name string, args []string, env map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, execCall{dir: dir, name: name, args: args, env: env})
}

func (e *mockExecutor) getCalls() []execCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make([]execCall, len(e.calls))
	copy(cp, e.calls)
	return cp
}

func (e *mockExecutor) Run(name string, args ...string) (string, error) {
	e.record("", name, args, nil)
	if e.runFunc != nil {
		return e.runFunc(name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (e *mockExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	e.record("", name, args, env)
	if e.runWithEnvFunc != nil {
		return e.runWithEnvFunc(env, name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (e *mockExecutor) RunInDir(dir, name string, args ...string) (string, error) {
	e.record(dir, name, args, nil)
	if e.runInDirFunc != nil {
		return e.runInDirFunc(dir, name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (e *mockExecutor) RunWithEnvInDir(env map[string]string, dir, name string, args ...string) (string, error) {
	e.record(dir, name, args, env)
	if e.runWithEnvInDirFunc != nil {
		return e.runWithEnvInDirFunc(env, dir, name, args...)
	}
	return "", fmt.Errorf("not mocked: %s %v", name, args)
}

func (e *mockExecutor) StartWithEnvInDir(env map[string]string, dir, name string, args ...string) (ProcessHandle, error) {
	e.record(dir, name, args, env)
	if e.startWithEnvInDirFunc != nil {
		return e.startWithEnvInDirFunc(env, dir, name, args...)
	}
	return nil, fmt.Errorf("not mocked: %s %v", name, args)
}

// ---------------------------------------------------------------------------
// Mock process handle
// ---------------------------------------------------------------------------

type mockHandle struct {
	waitFn   func() error
	signalFn func(os.Signal) error
}

func (h *mockHandle) Wait() error              { return h.waitFn() }
func (h *mockHandle) Signal(s os.Signal) error { return h.signalFn(s) }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const fakeGolemicBin = "/fake/golemic"

func newTestLoop(t *testing.T, exec *mockExecutor) (*Loop, string) {
	t.Helper()
	homeDir := t.TempDir()
	l := New(exec, fakeGolemicBin, homeDir, "/fake/repo", "testproject", new(bytes.Buffer))
	l.interval = time.Hour // prevent automatic re-ticks in tests
	l.newRunID = func() string { return "test-run-id" }
	l.resolveCredentials = func() (string, string, error) { return "golemic-dev", "dev-token", nil }
	return l, homeDir
}

func issueJSON(num int) string {
	return fmt.Sprintf(`{"number":%d,"title":"Test issue","url":"https://github.com/o/r/issues/%d","labels":[]}`, num, num)
}

func issueViewJSON(labels []string, assignees []string) string {
	labelObjs := make([]map[string]string, 0, len(labels))
	for _, label := range labels {
		labelObjs = append(labelObjs, map[string]string{"name": label})
	}
	assigneeObjs := make([]map[string]string, 0, len(assignees))
	for _, login := range assignees {
		assigneeObjs = append(assigneeObjs, map[string]string{"login": login})
	}
	payload, _ := json.Marshal(map[string]any{"labels": labelObjs, "assignees": assigneeObjs})
	return string(payload)
}

func writeRunFinishedEvent(t *testing.T, eventLogPath, outcome string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(eventLogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"outcome": outcome})
	ev := eventlog.Event{
		Type:    eventlog.EventRunFinished,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "test-run-id",
		TurnID:  0,
		Payload: payload,
	}
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() //nolint:errcheck
	if err := w.Write(ev); err != nil {
		t.Fatal(err)
	}
}

// expectedEventLogPath returns the event log path for the test-run-id under homeDir.
func expectedEventLogPath(homeDir string) string {
	return filepath.Join(homeDir, ".golemic", "testproject", "runs", "test-run-id", "events.jsonl")
}

// ---------------------------------------------------------------------------
// AC-001: Happy path tick
// ---------------------------------------------------------------------------

func TestHappyPathTick(t *testing.T) { //nolint:cyclop,gocognit // exhaustive AC-001 assertions; linear flow, splitting would obscure coverage
	exec := &mockExecutor{}
	l, homeDir := newTestLoop(t, exec)
	eventLogPath := expectedEventLogPath(homeDir)

	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "/fake/repo\n", nil
		}
		if name == fakeGolemicBin && len(args) == 1 && args[0] == "next-issue" {
			return issueJSON(42), nil
		}
		return "", fmt.Errorf("unexpected RunInDir: %s %v", name, args)
	}

	issueViewCalls := 0
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name != "gh" {
			return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "view" {
			issueViewCalls++
			switch issueViewCalls {
			case 1:
				return issueViewJSON([]string{"ready-for-agent"}, nil), nil
			case 2:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			case 3:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			default:
				return "", fmt.Errorf("unexpected gh issue view call %d", issueViewCalls)
			}
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "edit" {
			return "", nil
		}
		return "", fmt.Errorf("unexpected RunWithEnv: %s %v", name, args)
	}

	exec.startWithEnvInDirFunc = func(env map[string]string, dir, name string, args ...string) (ProcessHandle, error) {
		// Write success event so release reason is "done"
		writeRunFinishedEvent(t, eventLogPath, "success")
		return &mockHandle{
			waitFn:   func() error { return nil },
			signalFn: func(os.Signal) error { return nil },
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.tick(ctx)

	calls := exec.getCalls()
	// Expect: next-issue, claim gh calls, golemic run, release gh calls.
	if len(calls) != 7 {
		t.Fatalf("expected 7 executor calls, got %d: %v", len(calls), calls)
	}
	if calls[0].name != fakeGolemicBin || calls[0].args[0] != "next-issue" {
		t.Errorf("call[0]: want golemic next-issue, got %s %v", calls[0].name, calls[0].args)
	}
	if calls[1].name != "gh" || calls[1].args[0] != "issue" || calls[1].args[1] != "view" {
		t.Errorf("call[1]: want gh issue view, got %s %v", calls[1].name, calls[1].args)
	}
	if calls[2].name != "gh" || calls[2].args[0] != "issue" || calls[2].args[1] != "edit" {
		t.Errorf("call[2]: want gh issue edit, got %s %v", calls[2].name, calls[2].args)
	}
	if calls[3].name != "gh" || calls[3].args[0] != "issue" || calls[3].args[1] != "view" {
		t.Errorf("call[3]: want gh issue view, got %s %v", calls[3].name, calls[3].args)
	}
	if calls[4].name != fakeGolemicBin || calls[4].args[0] != "run" || calls[4].args[1] != "--issue" || calls[4].args[2] != "42" {
		t.Errorf("call[4]: want golemic run --issue 42, got %s %v", calls[4].name, calls[4].args)
	}
	if calls[5].name != "gh" || calls[5].args[0] != "issue" || calls[5].args[1] != "view" {
		t.Errorf("call[5]: want gh issue view, got %s %v", calls[5].name, calls[5].args)
	}
	if calls[6].name != "gh" || calls[6].args[0] != "issue" || calls[6].args[1] != "edit" {
		t.Errorf("call[6]: want gh issue edit, got %s %v", calls[6].name, calls[6].args)
	}
	for _, c := range calls {
		if c.name == fakeGolemicBin && len(c.args) > 0 && (c.args[0] == "claim-issue" || c.args[0] == "release-issue") {
			t.Errorf("unexpected golemic leaf subprocess call: %v", c.args)
		}
	}
	if captured := calls[4].env; captured["GOLEMIC_RUN_ID"] == "" || captured["GOLEMIC_EVENT_LOG"] == "" || captured["GOLEMIC_TURN_ID"] != "0" {
		t.Errorf("run env invalid: %v", captured)
	}
}

// ---------------------------------------------------------------------------
// AC-002: Nothing takeable
// ---------------------------------------------------------------------------

func TestNothingTakeable(t *testing.T) {
	exec := &mockExecutor{}
	l, _ := newTestLoop(t, exec)

	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "/fake/repo\n", nil
		}
		if name == fakeGolemicBin && len(args) == 1 && args[0] == "next-issue" {
			return "", &preflight.ErrExit{ExitCode: 2, Stderr: "no takeable issue"}
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}

	var buf bytes.Buffer
	l.stderr = &buf

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.tick(ctx)

	calls := exec.getCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 call (next-issue), got %d", len(calls))
	}
	if calls[0].args[0] != "next-issue" {
		t.Errorf("expected next-issue call, got %v", calls[0].args)
	}
	if !strings.Contains(buf.String(), "no takeable issue") {
		t.Errorf("stderr should mention no takeable issue, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// AC-003: Race lost at claim
// ---------------------------------------------------------------------------

func TestRaceLostAtClaim(t *testing.T) {
	exec := &mockExecutor{}
	l, _ := newTestLoop(t, exec)

	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "/fake/repo\n", nil
		}
		if name == fakeGolemicBin && args[0] == "next-issue" {
			return issueJSON(42), nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}
	views := 0
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name != "gh" {
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "view" {
			views++
			if views == 1 {
				return issueViewJSON([]string{"ready-for-agent"}, nil), nil
			}
			return issueViewJSON([]string{"in-progress"}, []string{"someone-else"}), nil
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "edit" {
			return "", nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}

	var buf bytes.Buffer
	l.stderr = &buf

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.tick(ctx)

	calls := exec.getCalls()
	if len(calls) != 5 {
		t.Errorf("expected 5 calls (next-issue + claim view/edit/view + rollback edit), got %d", len(calls))
	}
	for _, c := range calls {
		if c.name == fakeGolemicBin && len(c.args) > 0 && (c.args[0] == "run" || c.args[0] == "release-issue") {
			t.Errorf("unexpected golemic subprocess call after race loss: %v", c.args)
		}
	}
	if !strings.Contains(buf.String(), "race lost") {
		t.Errorf("stderr should mention race lost, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// AC-004: Non-success outcome -> failed
// ---------------------------------------------------------------------------

func TestNonSuccessOutcomeFailed(t *testing.T) { //nolint:cyclop // linear sequence of setup and assertions
	exec := &mockExecutor{}
	l, homeDir := newTestLoop(t, exec)
	eventLogPath := expectedEventLogPath(homeDir)

	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "/fake/repo\n", nil
		}
		if name == fakeGolemicBin && args[0] == "next-issue" {
			return issueJSON(42), nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}
	views := 0
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name != "gh" {
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "view" {
			views++
			switch views {
			case 1:
				return issueViewJSON([]string{"ready-for-agent"}, nil), nil
			case 2, 3:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			default:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			}
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "edit" {
			return "", nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}
	exec.startWithEnvInDirFunc = func(env map[string]string, dir, name string, args ...string) (ProcessHandle, error) {
		writeRunFinishedEvent(t, eventLogPath, "escalated")
		return &mockHandle{
			waitFn:   func() error { return nil },
			signalFn: func(os.Signal) error { return nil },
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.tick(ctx)

	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var releaseReason string
	for _, ev := range events {
		if ev.Type == eventlog.EventIssueReleased {
			var payload struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			releaseReason = payload.Reason
		}
	}
	if releaseReason != "failed" {
		t.Errorf("release reason: want failed, got %q", releaseReason)
	}
}

// ---------------------------------------------------------------------------
// AC-005: No run_finished event -> abandoned
// ---------------------------------------------------------------------------

func TestNoRunFinishedAbandoned(t *testing.T) { //nolint:cyclop // linear sequence of setup and assertions
	exec := &mockExecutor{}
	l, homeDir := newTestLoop(t, exec)
	eventLogPath := expectedEventLogPath(homeDir)

	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "/fake/repo\n", nil
		}
		if name == fakeGolemicBin && args[0] == "next-issue" {
			return issueJSON(42), nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}
	views := 0
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name != "gh" {
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "view" {
			views++
			switch views {
			case 1:
				return issueViewJSON([]string{"ready-for-agent"}, nil), nil
			case 2, 3:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			default:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			}
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "edit" {
			return "", nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}
	exec.startWithEnvInDirFunc = func(env map[string]string, dir, name string, args ...string) (ProcessHandle, error) {
		// Runner exits without writing any events
		return &mockHandle{
			waitFn:   func() error { return nil },
			signalFn: func(os.Signal) error { return nil },
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.tick(ctx)

	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var releaseReason string
	for _, ev := range events {
		if ev.Type == eventlog.EventIssueReleased {
			var payload struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			releaseReason = payload.Reason
		}
	}
	if releaseReason != "abandoned" {
		t.Errorf("release reason: want abandoned, got %q", releaseReason)
	}
}

// ---------------------------------------------------------------------------
// AC-006: next-issue exit 1 -> loop tolerates and continues
// ---------------------------------------------------------------------------

func TestNextIssueExit1Tolerated(t *testing.T) {
	exec := &mockExecutor{}
	l, _ := newTestLoop(t, exec)

	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "/fake/repo\n", nil
		}
		if name == fakeGolemicBin && args[0] == "next-issue" {
			return "", &preflight.ErrExit{ExitCode: 1, Stderr: "gh error"}
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}

	var buf bytes.Buffer
	l.stderr = &buf

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.tick(ctx)

	calls := exec.getCalls()
	if len(calls) != 1 {
		t.Errorf("expected only 1 call (next-issue), got %d", len(calls))
	}
	if !strings.Contains(buf.String(), "next-issue failed") {
		t.Errorf("stderr should mention next-issue failed, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// AC-007: SIGTERM during idle wait exits within 1s
// ---------------------------------------------------------------------------

func TestSIGTERMDuringIdleExitsQuickly(t *testing.T) {
	exec := &mockExecutor{}
	l, _ := newTestLoop(t, exec)
	l.interval = time.Hour // never fires naturally

	// next-issue returns no issue so the first tick completes quickly
	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		return "", &preflight.ErrExit{ExitCode: 2, Stderr: "no issue"}
	}

	var buf bytes.Buffer
	l.stderr = &buf

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()

	// Give the first tick time to complete
	time.Sleep(20 * time.Millisecond)

	// Simulate SIGTERM
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("loop did not exit within 1 second after SIGTERM")
	}

	if !strings.Contains(buf.String(), "run-loop terminated") {
		t.Errorf("stderr should contain 'run-loop terminated', got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// AC-008: SIGTERM while runner in flight -> forward, wait, release
// ---------------------------------------------------------------------------

func TestSIGTERMWhileRunnerInFlight(t *testing.T) { //nolint:cyclop,gocognit,funlen // AC-008 signal handling scenario; assertions require coordinated channel setup
	exec := &mockExecutor{}
	l, homeDir := newTestLoop(t, exec)
	l.interval = time.Hour

	eventLogPath := expectedEventLogPath(homeDir)

	// next-issue succeeds
	exec.runInDirFunc = func(dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
			return "/fake/repo\n", nil
		}
		if name == fakeGolemicBin && args[0] == "next-issue" {
			return issueJSON(42), nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}
	views := 0
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name != "gh" {
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "view" {
			views++
			switch views {
			case 1:
				return issueViewJSON([]string{"ready-for-agent"}, nil), nil
			case 2, 3:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			default:
				return issueViewJSON([]string{"in-progress"}, []string{"golemic-dev"}), nil
			}
		}
		if len(args) >= 2 && args[0] == "issue" && args[1] == "edit" {
			return "", nil
		}
		return "", fmt.Errorf("unexpected: %s %v", name, args)
	}

	runnerStarted := make(chan struct{})
	signalReceived := make(chan os.Signal, 1)
	runnerDone := make(chan struct{})

	exec.startWithEnvInDirFunc = func(env map[string]string, dir, name string, args ...string) (ProcessHandle, error) {
		return &mockHandle{
			waitFn: func() error {
				close(runnerStarted)
				// Block until signalled
				<-runnerDone
				return nil
			},
			signalFn: func(sig os.Signal) error {
				signalReceived <- sig
				// Write an escalated event so the reason becomes "failed"
				writeRunFinishedEvent(t, eventLogPath, "escalated")
				close(runnerDone)
				return nil
			},
		}, nil
	}

	var buf bytes.Buffer
	l.stderr = &buf

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()

	// Wait for runner to start
	select {
	case <-runnerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}

	// Simulate SIGTERM
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not exit after SIGTERM + runner finish")
	}

	// Verify SIGTERM was forwarded
	select {
	case sig := <-signalReceived:
		if sig != syscall.SIGTERM {
			t.Errorf("expected SIGTERM, got %v", sig)
		}
	default:
		t.Error("SIGTERM was not forwarded to runner")
	}

	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var releaseReason string
	for _, ev := range events {
		if ev.Type == eventlog.EventIssueReleased {
			var payload struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			releaseReason = payload.Reason
		}
	}
	if releaseReason != "failed" {
		t.Errorf("release reason: want failed, got %q", releaseReason)
	}

	if !strings.Contains(buf.String(), "run-loop terminated") {
		t.Errorf("stderr should contain 'run-loop terminated', got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Interval override via GOLEMIC_RUN_LOOP_INTERVAL_MS
// ---------------------------------------------------------------------------

func TestIntervalOverrideEnvVar(t *testing.T) {
	t.Setenv("GOLEMIC_RUN_LOOP_INTERVAL_MS", "250")

	exec := &mockExecutor{}
	l := New(exec, fakeGolemicBin, t.TempDir(), "/fake/repo", "proj", new(bytes.Buffer))

	if l.interval != 250*time.Millisecond {
		t.Errorf("interval: want 250ms, got %v", l.interval)
	}
}

// ---------------------------------------------------------------------------
// Reason derivation uses LAST run_finished event
// ---------------------------------------------------------------------------

func TestDeriveReasonUsesLastEvent(t *testing.T) {
	homeDir := t.TempDir()
	eventLogPath := filepath.Join(homeDir, "events.jsonl")

	// Write two run_finished events: first success, then escalated
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeRunFinishedEvent(t, eventLogPath, "success")
	writeRunFinishedEvent(t, eventLogPath, "escalated")

	exec := &mockExecutor{}
	l := New(exec, fakeGolemicBin, t.TempDir(), "/fake/repo", "proj", new(bytes.Buffer))
	reason := l.deriveReason(eventLogPath)
	if reason != "failed" {
		t.Errorf("reason: want failed (last event was escalated), got %q", reason)
	}
}
