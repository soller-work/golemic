// Package runloop implements the autonomous 60-second polling loop for golemic.
package runloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golemic/internal/claim"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// ProcessHandle represents an in-flight subprocess that can receive signals.
type ProcessHandle interface {
	Wait() error
	Signal(os.Signal) error
}

// Executor extends preflight.Executor with subprocess lifecycle support for the runner.
type Executor interface {
	preflight.Executor
	StartWithEnvInDir(env map[string]string, dir, name string, args ...string) (ProcessHandle, error)
}

const defaultIntervalMS = 60_000

// Loop is the autonomous run-loop that polls for takeable issues.
type Loop struct {
	executor           Executor
	golemicBin         string
	homeDir            string
	repoRoot           string
	project            string
	interval           time.Duration
	stderr             io.Writer
	newRunID           func() string
	resolveCredentials func() (devLogin, devToken string, err error)
}

// New creates a Loop. The tick interval defaults to 60 s; set
// GOLEMIC_RUN_LOOP_INTERVAL_MS to a positive integer to override (test-only).
func New(executor Executor, golemicBin, homeDir, repoRoot, project string, stderr io.Writer) *Loop {
	ms := defaultIntervalMS
	if v := os.Getenv("GOLEMIC_RUN_LOOP_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ms = n
		}
	}
	l := &Loop{
		executor:   executor,
		golemicBin: golemicBin,
		homeDir:    homeDir,
		repoRoot:   repoRoot,
		project:    project,
		interval:   time.Duration(ms) * time.Millisecond,
		stderr:     stderr,
		newRunID: func() string {
			return fmt.Sprintf("loop-%s", time.Now().UTC().Format("20060102T150405.000Z"))
		},
	}
	l.resolveCredentials = func() (string, string, error) {
		return claim.ResolveCredentials(executor)
	}
	return l
}

// Run starts the tick loop and blocks until ctx is cancelled (SIGINT/SIGTERM).
func (l *Loop) Run(ctx context.Context) {
	fmt.Fprintln(l.stderr, "run-loop started") //nolint:errcheck

	// First tick fires immediately per PS-001 -> PS-002.
	l.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(l.stderr, "run-loop terminated") //nolint:errcheck
			return
		case <-time.After(l.interval):
			l.tick(ctx)
		}
	}
}

// tick performs one complete tick cycle.
func (l *Loop) tick(ctx context.Context) {
	issueNum, ok := l.selectIssue()
	if !ok {
		return
	}

	runID := l.newRunID()
	runsDir := filepath.Join(l.homeDir, ".golemic", l.project, "runs", runID)
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		fmt.Fprintf(l.stderr, "run-loop: failed to create run dir: %v\n", err) //nolint:errcheck
		return
	}
	eventLogPath := filepath.Join(runsDir, "events.jsonl")

	tickEnv := map[string]string{
		"GOLEMIC_RUN_ID":    runID,
		"GOLEMIC_EVENT_LOG": eventLogPath,
		"GOLEMIC_TURN_ID":   "0",
	}

	if !l.claim(issueNum, tickEnv) {
		return
	}

	l.runAndRelease(ctx, issueNum, eventLogPath, tickEnv)
}

// selectIssue calls golemic next-issue and returns the issue number.
func (l *Loop) selectIssue() (int, bool) {
	out, err := l.executor.RunInDir(l.repoRoot, l.golemicBin, "next-issue")
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) && ee.ExitCode == 2 {
			fmt.Fprintln(l.stderr, "run-loop: no takeable issue") //nolint:errcheck
		} else {
			fmt.Fprintf(l.stderr, "run-loop: next-issue failed: %v\n", err) //nolint:errcheck
		}
		return 0, false
	}

	var issue struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal([]byte(out), &issue); err != nil || issue.Number <= 0 {
		fmt.Fprintln(l.stderr, "run-loop: next-issue: invalid response") //nolint:errcheck
		return 0, false
	}
	return issue.Number, true
}

// claim acquires the issue lock in-process and writes the issue_claimed event.
func (l *Loop) claim(issueNum int, env map[string]string) bool {
	devLogin, devToken, err := l.resolveCredentials()
	if err != nil {
		fmt.Fprintf(l.stderr, "run-loop: claim #%d failed: %v\n", issueNum, err) //nolint:errcheck
		return false
	}

	result, claimErr := claim.Claim(l.executor, issueNum, devLogin, devToken)
	switch result {
	case claim.ResultOK:
		return l.writeClaimEvent(env, issueNum)
	case claim.ResultIdempotent:
		return true
	case claim.ResultNotTakeable:
		fmt.Fprintf(l.stderr, "run-loop: issue #%d not takeable\n", issueNum) //nolint:errcheck
	case claim.ResultRaceLost:
		fmt.Fprintf(l.stderr, "run-loop: race lost on issue #%d: %v\n", issueNum, claimErr) //nolint:errcheck
	case claim.ResultError:
		fmt.Fprintf(l.stderr, "run-loop: claim #%d failed: %v\n", issueNum, claimErr) //nolint:errcheck
	}
	return false
}

// runAndRelease starts the runner, waits for it (forwarding SIGTERM on ctx cancel),
// derives the release reason from the event log, and releases the issue.
func (l *Loop) runAndRelease(ctx context.Context, issueNum int, eventLogPath string, env map[string]string) {
	handle, err := l.executor.StartWithEnvInDir(env, l.repoRoot, l.golemicBin, "run", "--issue", strconv.Itoa(issueNum))
	if err != nil {
		fmt.Fprintf(l.stderr, "run-loop: runner start failed for #%d: %v\n", issueNum, err) //nolint:errcheck
		l.release(issueNum, "abandoned", env)
		return
	}

	runDone := make(chan error, 1)
	go func() { runDone <- handle.Wait() }()

	select {
	case runErr := <-runDone:
		if runErr != nil {
			fmt.Fprintf(l.stderr, "run-loop: runner for #%d exited non-zero: %v\n", issueNum, runErr) //nolint:errcheck
		}
	case <-ctx.Done():
		fmt.Fprintf(l.stderr, "run-loop: forwarding SIGTERM to runner for #%d\n", issueNum) //nolint:errcheck
		_ = handle.Signal(syscall.SIGTERM)
		<-runDone
	}

	reason := l.deriveReason(eventLogPath)
	l.release(issueNum, reason, env)
}

// deriveReason reads the event log and maps the last run_finished outcome to a release reason.
// Falls back to "abandoned" on any read or parse error, and when no run_finished event exists.
func (l *Loop) deriveReason(eventLogPath string) string {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		fmt.Fprintf(l.stderr, "run-loop: could not read event log: %v\n", err) //nolint:errcheck
		return "abandoned"
	}
	ev, err := eventlog.LastEventOfType(events, eventlog.EventRunFinished)
	if err != nil {
		return "abandoned"
	}
	var payload struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return "abandoned"
	}
	if payload.Outcome == "success" {
		return "done"
	}
	return "failed"
}

func (l *Loop) writeClaimEvent(env map[string]string, issueNum int) bool {
	payload, err := eventlog.MarshalIssueClaimedPayload(issueNum, "ok")
	if err != nil {
		fmt.Fprintf(l.stderr, "run-loop: failed to write %s event: %v\n", eventlog.EventIssueClaimed, err) //nolint:errcheck
		return false
	}
	return l.writeEvent(env, eventlog.EventIssueClaimed, payload)
}

func (l *Loop) writeReleaseEvent(env map[string]string, issueNum int, reason string) bool {
	payload, err := eventlog.MarshalIssueReleasedPayload(issueNum, reason)
	if err != nil {
		fmt.Fprintf(l.stderr, "run-loop: failed to write %s event: %v\n", eventlog.EventIssueReleased, err) //nolint:errcheck
		return false
	}
	return l.writeEvent(env, eventlog.EventIssueReleased, payload)
}

func (l *Loop) writeEvent(env map[string]string, eventType string, payload json.RawMessage) bool {
	eventLogPath := env["GOLEMIC_EVENT_LOG"]
	runID := env["GOLEMIC_RUN_ID"]
	turnID, err := strconv.Atoi(env["GOLEMIC_TURN_ID"])
	if eventLogPath == "" || runID == "" || err != nil || turnID < 0 {
		fmt.Fprintf(l.stderr, "run-loop: failed to write %s event: invalid env\n", eventType) //nolint:errcheck
		return false
	}

	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(l.stderr, "run-loop: failed to write %s event: %v\n", eventType, err) //nolint:errcheck
		return false
	}
	defer writer.Close() //nolint:errcheck

	event := eventlog.Event{Type: eventType, Ts: time.Now().Format(time.RFC3339), RunID: runID, TurnID: turnID, Payload: payload}
	if err := writer.Write(event); err != nil {
		fmt.Fprintf(l.stderr, "run-loop: failed to write %s event: %v\n", eventType, err) //nolint:errcheck
		return false
	}
	return true
}

// release releases the issue lock in-process and writes the issue_released event.
func (l *Loop) release(issueNum int, reason string, env map[string]string) {
	devLogin, devToken, err := l.resolveCredentials()
	if err != nil {
		fmt.Fprintf(l.stderr, "run-loop: release #%d failed: %v\n", issueNum, err) //nolint:errcheck
		return
	}

	result, releaseErr := claim.Release(l.executor, issueNum, devLogin, devToken, reason)
	switch result {
	case claim.ReleaseResultOK:
		if !l.writeReleaseEvent(env, issueNum, reason) {
			fmt.Fprintf(l.stderr, "run-loop: release #%d failed: could not write event\n", issueNum) //nolint:errcheck
		}
	case claim.ReleaseResultIdempotent:
		return
	case claim.ReleaseResultForeignClaim:
		fmt.Fprintf(l.stderr, "run-loop: release #%d failed: %v\n", issueNum, releaseErr) //nolint:errcheck
	case claim.ReleaseResultError:
		fmt.Fprintf(l.stderr, "run-loop: release #%d failed: %v\n", issueNum, releaseErr) //nolint:errcheck
	}
}
