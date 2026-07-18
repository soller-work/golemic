// Package runner orchestrates a golemic run: host-repo resolution, config/credentials
// loading, runId generation, event log creation, issue loading via gh, collision checks,
// dev/reviewer worktrees, agent execution, event reading, outcome determination,
// run_finished writing, and cleanup.
//
// Process steps (PS-001–PS-006 per spec):
//   1. Resolve host repo (git root; if under tools/golemic, find enclosing repo)
//   2. Load config and credentials (fail-closed before any GitHub access)
//   3. Generate runId, create event log, write run_started
//   4. Load issue from GitHub via gh issue view
//   5. Collision check (worktree, local/remote branch, open PR)
//   6. Full orchestration: dev worktree → dev agent → pr_opened → reviewer worktree → reviewer agent → dirty check → review_submitted → outcome determination → run_finished → cleanup
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golemic/internal/agent"
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/telemetry"
	"golemic/internal/worktree"
)

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Preflighter runs preflight checks in read-only mode. Implementations must be
// safe to call multiple times.
type Preflighter interface {
	Check() preflight.Results
}

// Runner orchestrates a golemic run.
type Runner struct {
	executor    preflight.Executor
	homeDir     string
	cwd         string
	stdout      io.Writer
	stderr      io.Writer
	issueNum    int
	preflighter Preflighter // nil = create from executor+homeDir+repoRoot in Run()
	lookupEnv   func(string) (string, bool)
	runAgentFn  func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error)

	clean bool
	quiet bool

	ciPollIntervalOverride time.Duration
	ciTimeoutOverride      time.Duration

	// Resolved during Run
	repoRoot   string
	project    string
	runID      string
	branchName string
	cfg        *config.Config
	creds      *credentials.Credentials
	issue      *issueData

	// Telemetry
	sink    telemetry.Sink
	traceID string
}

// New creates a new Runner. executor is used for all gh/git commands, homeDir is
// the user's home directory (~/.golemic is resolved relative to it), cwd is the
// current working directory, issueNum is the GitHub issue number.
func New(executor preflight.Executor, homeDir, cwd string, issueNum int) *Runner {
	return &Runner{
		executor: executor,
		homeDir:  homeDir,
		cwd:      cwd,
		stdout:   io.Discard,
		stderr:   io.Discard,
		issueNum: issueNum,
		sink:     telemetry.NoopSink{},
	}
}

// SetStdout sets the writer for normal output (e.g. runId on success).
func (r *Runner) SetStdout(w io.Writer) { r.stdout = w }

// SetStderr sets the writer for error output.
func (r *Runner) SetStderr(w io.Writer) { r.stderr = w }

// SetPreflighter injects a custom Preflighter, replacing the default preflight
// implementation. Used by tests to inject a passing or failing stub.
func (r *Runner) SetPreflighter(p Preflighter) { r.preflighter = p }

// SetLookupEnv injects a custom env lookup for credentials loading.
// nil means os.LookupEnv (production default).
func (r *Runner) SetLookupEnv(fn func(string) (string, bool)) { r.lookupEnv = fn }

// SetRunAgentFn injects a fake agent runner for unit tests.
// nil means agent.RunRole (production default).
func (r *Runner) SetRunAgentFn(fn func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error)) {
	r.runAgentFn = fn
}

// SetClean enables pre-run artifact cleanup for the target issue.
func (r *Runner) SetClean(clean bool) { r.clean = clean }

// SetCIPollInterval overrides the CI check poll interval (for tests only).
func (r *Runner) SetCIPollInterval(d time.Duration) { r.ciPollIntervalOverride = d }

// SetCITimeout overrides the CI check timeout (for tests only).
func (r *Runner) SetCITimeout(d time.Duration) { r.ciTimeoutOverride = d }

// SetQuiet suppresses the run-setup header when set to true.
func (r *Runner) SetQuiet(quiet bool) { r.quiet = quiet }

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------

// Run executes the full run process and returns the process exit code.
//
// Process flow (per spec §Process Steps):
//
//	PS-001: Resolve host repo
//	PS-002: Load config and credentials (fail-closed)
//	PS-003: Generate runId, create event log, write run_started
//	PS-004: Load issue from GitHub
//	PS-005: Collision check
//	PS-006: Full orchestration
func (r *Runner) Run() int {
	// ---- PS-001: Resolve host repo ----
	repoRoot, err := resolveHostRepo(r.executor, r.cwd)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to resolve host repo: %v\n", err)
		return 1
	}
	r.repoRoot = repoRoot
	r.project = filepath.Base(repoRoot)

	// ---- Preflight gate (read-only, before any GitHub/event-log access) ----
	pfl := r.preflighter
	if pfl == nil {
		// Production path: create a real check-mode preflight with stdout discarded;
		// the runner prints failures to stderr directly.
		pfl = preflight.New(r.executor, r.homeDir, r.repoRoot)
	}
	gateResults := pfl.Check()
	if !gateResults.AllOK() {
		for _, res := range gateResults {
			if !res.Ok {
				fmt.Fprintf(r.stderr, "FAILED: %s - %s\n", res.Name, res.Details)
			}
		}
		fmt.Fprintln(r.stderr, "failed")
		return 1
	}

	// ---- PS-002: Load config and credentials (BR-002: fail-closed) ----
	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to load config: %v\n", err)
		return 1
	}
	r.cfg = cfg
	r.project = cfg.Project

	loader := credentials.NewLoader(r.homeDir)
	loader.LookupEnv = r.lookupEnv
	creds, err := loader.Load(r.project)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to load credentials: %v\n", err)
		return 1
	}
	r.creds = creds

	// ---- PS-003: Generate runId and create event log (BR-003, BR-007) ----
	r.runID = fmt.Sprintf("issue-%d-%s", r.issueNum, time.Now().UTC().Format("20060102T150405Z"))
	r.branchName = fmt.Sprintf("%s%d", branchPrefix, r.issueNum)

	eventLogPath := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID, "events.jsonl")

	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to create event log: %v\n", err)
		return 1
	}
	defer writer.Close()

	// Write run_started (BR-007: must be written before any GitHub access)
	startPayload, _ := json.Marshal(runStartedPayload{
		Issue: r.issueNum,
		RunID: r.runID,
	})
	if err := writer.Write(eventlog.Event{
		Type:    eventlog.EventRunStarted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: startPayload,
	}); err != nil {
		fmt.Fprintf(r.stderr, "Failed to write run_started event: %v\n", err)
		return 1
	}

	// ---- PS-004: Load issue from GitHub ----
	issue, err := r.loadIssue()
	if err != nil {
		fmt.Fprintf(r.stderr, "Failed to load issue %d: %v\n", r.issueNum, err)
		return 1
	}
	r.issue = issue
	if !r.quiet {
		r.writeRunHeader(r.stderr)
	}

	// ---- Pre-collision cleanup (--clean) ----
	if r.clean {
		if err := r.cleanArtifacts(); err != nil {
			fmt.Fprintln(r.stderr, err.Error()) //nolint:errcheck
			finishedPayload, _ := json.Marshal(runFinishedPayload{Outcome: outcomeAborted})
			_ = writer.Write(eventlog.Event{
				Type:    eventlog.EventRunFinished,
				Ts:      time.Now().Format(time.RFC3339),
				RunID:   r.runID,
				Payload: finishedPayload,
			})
			fmt.Fprintf(r.stdout, "runs/%s\n", r.runID) //nolint:errcheck
			return 1
		}
	}

	// ---- PS-005: Collision check ----
	collision, err := r.checkAllCollisions()
	if err != nil {
		fmt.Fprintln(r.stderr, err.Error())
		// Write run_finished with outcome aborted
		finishedPayload, _ := json.Marshal(runFinishedPayload{Outcome: outcomeAborted})
		_ = writer.Write(eventlog.Event{
			Type:    eventlog.EventRunFinished,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   r.runID,
			Payload: finishedPayload,
		})
		fmt.Fprintf(r.stdout, "runs/%s\n", r.runID)
		return 1
	}
	if collision != nil {
		fmt.Fprintln(r.stderr, collision.Message)
		// Write run_finished with outcome aborted
		finishedPayload, _ := json.Marshal(runFinishedPayload{Outcome: outcomeAborted})
		_ = writer.Write(eventlog.Event{
			Type:    eventlog.EventRunFinished,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   r.runID,
			Payload: finishedPayload,
		})
		fmt.Fprintf(r.stdout, "runs/%s\n", r.runID)
		return 1
	}

	// ---- Telemetry sink setup ----
	r.traceID = telemetry.TraceID(r.runID)
	runDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID)
	if r.cfg.Telemetry.Enabled {
		r.sink = telemetry.NewFileSink(filepath.Join(runDir, "telemetry.jsonl"))
	} else {
		r.sink = telemetry.NoopSink{}
	}

	// ---- PS-006: Full orchestration ----
	runSpanID, endRunSpan := telemetry.StartSpan(r.sink, r.traceID, "", telemetry.SpanRun, map[string]any{
		"service.name": "golemic",
		"run_id":       r.runID,
		"issue":        r.issueNum,
		"project":      r.project,
		"pid":          os.Getpid(),
	})

	finalOutcome := r.orchestrate(writer, eventLogPath, runSpanID)

	// Worktree cleanup spans (children of run span, only on success)
	golemicDir2 := filepath.Join(r.homeDir, ".golemic", r.project)
	if finalOutcome == outcomeSuccess {
		_, endCleanDev := telemetry.StartSpan(r.sink, r.traceID, runSpanID, telemetry.SpanWorktreeCleanup,
			map[string]any{"run_id": r.runID, "issue": r.issueNum, "worktree": "dev"})
		cleanDevErr := worktree.Cleanup(r.repoRoot, golemicDir2, r.issueNum, r.executor)
		if cleanDevErr != nil {
			fmt.Fprintf(r.stderr, "Warning: dev worktree cleanup failed: %v\n", cleanDevErr) //nolint:errcheck
			endCleanDev(telemetry.StatusError, nil)
		} else {
			endCleanDev(telemetry.StatusOK, nil)
		}

		_, endCleanRev := telemetry.StartSpan(r.sink, r.traceID, runSpanID, telemetry.SpanWorktreeCleanup,
			map[string]any{"run_id": r.runID, "issue": r.issueNum, "worktree": "reviewer"})
		cleanRevErr := worktree.CleanupReviewer(r.repoRoot, golemicDir2, r.issueNum, r.executor)
		if cleanRevErr != nil {
			fmt.Fprintf(r.stderr, "Warning: reviewer worktree cleanup failed: %v\n", cleanRevErr) //nolint:errcheck
			endCleanRev(telemetry.StatusError, nil)
		} else {
			endCleanRev(telemetry.StatusOK, nil)
		}
	}

	// Close run span
	runStatus := telemetry.StatusOK
	if finalOutcome != outcomeSuccess {
		runStatus = telemetry.StatusError
	}
	endRunSpan(runStatus, map[string]any{"outcome": finalOutcome})

	// Write run_finished with final outcome (BR-001: always the last event)
	finishedPayload, _ := json.Marshal(runFinishedPayload{Outcome: finalOutcome})
	_ = writer.Write(eventlog.Event{
		Type:    eventlog.EventRunFinished,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: finishedPayload,
	})

	// BR-007: Exit 0 only for success; exit != 0 otherwise
	if finalOutcome == outcomeSuccess {
		fmt.Fprintln(r.stdout, r.runID)
		return 0
	}

	fmt.Fprintf(r.stdout, "runs/%s\n", r.runID)
	return 1
}

// writeAgentCompleted appends an agent_completed event to the event log.
// Errors are silently dropped; a log write failure must not change the run outcome.
func (r *Runner) writeAgentCompleted(eventLogPath, role string, exitCode int) {
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		return
	}
	defer w.Close() //nolint:errcheck

	payload, err := eventlog.MarshalAgentCompletedPayload(role, exitCode)
	if err != nil {
		return
	}
	_ = w.Write(eventlog.Event{
		Type:    eventlog.EventAgentCompleted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		Payload: payload,
	})
}

// cleanupReviewerWorktree emits a worktree.cleanup span and cleans up the reviewer worktree.
func (r *Runner) cleanupReviewerWorktree(golemicDir, parentSpanID string) {
	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanWorktreeCleanup,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "worktree": "reviewer"})
	if err := worktree.CleanupReviewer(r.repoRoot, golemicDir, r.issueNum, r.executor); err != nil {
		fmt.Fprintf(r.stderr, "Warning: reviewer worktree cleanup failed: %v\n", err) //nolint:errcheck
		endSpan(telemetry.StatusError, nil)
	} else {
		endSpan(telemetry.StatusOK, nil)
	}
}

// postEscalationCommentWithSpan emits an escalation.comment span and posts the comment.
func (r *Runner) postEscalationCommentWithSpan(eventLogPath, parentSpanID string, roundCount int) {
	_, endSpan := telemetry.StartSpan(r.sink, r.traceID, parentSpanID, telemetry.SpanEscalationComment,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "round": roundCount})
	prNumber, prErr := r.getPRNumber(eventLogPath)
	if prErr == nil {
		r.postEscalationComment(prNumber, roundCount)
		endSpan(telemetry.StatusOK, nil)
	} else {
		fmt.Fprintf(r.stderr, "Warning: failed to get PR number for escalation comment: %v\n", prErr) //nolint:errcheck
		endSpan(telemetry.StatusError, nil)
	}
}

// postEscalationComment posts a deterministic escalation comment on the PR using
// the reviewer token. Errors are logged but do not change the escalated outcome (BR-008).
func (r *Runner) postEscalationComment(prNumber, roundCount int) {
	body := fmt.Sprintf(
		"golemic has completed %d review round(s) for issue #%d (PR #%d). "+
			"The reviewer requested changes in every round. "+
			"No merge has happened. Human review is required.",
		roundCount, r.issueNum, prNumber,
	)
	_, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "pr", "comment", fmt.Sprintf("%d", prNumber), "--body", body,
	)
	if err != nil {
		fmt.Fprintf(r.stderr, "Warning: failed to post escalation comment: %v\n", err) //nolint:errcheck
	}
}

// orchestrate implements the bounded dev→reviewer ping-pong loop after collision check passes.
// runSpanID is the parent telemetry span ID for all phases within orchestration.
// Returns final outcome.
func (r *Runner) orchestrate(writer worktree.EventWriter, eventLogPath string, runSpanID string) string {
	const maxRounds = 3

	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	var timeoutDuration time.Duration
	if r.cfg.TimeoutSeconds > 0 {
		timeoutDuration = time.Duration(r.cfg.TimeoutSeconds) * time.Second
	} else {
		timeoutDuration = time.Duration(r.cfg.TimeoutMinutes) * time.Minute
	}

	// Create dev worktree
	_, endCreateDevWT := telemetry.StartSpan(r.sink, r.traceID, runSpanID, telemetry.SpanWorktreeCreate,
		map[string]any{"run_id": r.runID, "issue": r.issueNum, "worktree": "dev"})
	if err := worktree.Create(r.repoRoot, golemicDir, r.runID, r.issueNum, "golemic-dev", r.executor, writer); err != nil {
		endCreateDevWT(telemetry.StatusError, nil)
		fmt.Fprintf(r.stderr, "Failed to create dev worktree: %v\n", err)
		return outcomeDevFailed
	}
	endCreateDevWT(telemetry.StatusOK, nil)

	// Round 1 dev
	devOutcome := r.runDevAgent(golemicDir, eventLogPath, timeoutDuration, runSpanID, 1)
	if devOutcome != outcomeSuccess {
		return devOutcome
	}

	if !r.hasPROpenedEvent(eventLogPath) {
		fmt.Fprintf(r.stderr, "dev_failed: pr_opened event missing or invalid\n")
		return outcomeDevFailed
	}

	// CI gate: wait for PR checks to pass before allowing the reviewer to start
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: failed to get PR number for CI gate: %v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}
	ciGateOutcome := r.runCIGate(prNumber, eventLogPath, timeoutDuration)
	if ciGateOutcome != outcomeSuccess {
		return ciGateOutcome
	}

	return r.pingPongLoop(golemicDir, eventLogPath, writer, timeoutDuration, runSpanID)
}

// pingPongLoop runs the bounded reviewer ping-pong loop (up to maxRounds).
func (r *Runner) pingPongLoop(golemicDir, eventLogPath string, writer worktree.EventWriter, timeout time.Duration, runSpanID string) string {
	const maxRounds = 3

	firstReviewerRound := true
	round := 1
	for {
		if !firstReviewerRound {
			r.cleanupReviewerWorktree(golemicDir, runSpanID)
		}
		firstReviewerRound = false

		_, endCreateRevWT := telemetry.StartSpan(r.sink, r.traceID, runSpanID, telemetry.SpanWorktreeCreate,
			map[string]any{"run_id": r.runID, "issue": r.issueNum, "worktree": "reviewer"})
		if err := worktree.CreateForReviewer(r.repoRoot, golemicDir, r.runID, r.issueNum, r.branchName, "golemic-reviewer", r.executor, writer); err != nil {
			endCreateRevWT(telemetry.StatusError, nil)
			fmt.Fprintf(r.stderr, "Failed to create reviewer worktree: %v\n", err) //nolint:errcheck
			return outcomeReviewFailed
		}
		endCreateRevWT(telemetry.StatusOK, nil)

		if outcome := r.runReviewerAgent(golemicDir, eventLogPath, timeout, runSpanID, round); outcome != outcomeSuccess {
			return outcome
		}

		reviewerWorktreePath := filepath.Join(golemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
		isDirty, err := worktree.IsDirty(reviewerWorktreePath, r.executor)
		if err != nil {
			fmt.Fprintf(r.stderr, "review_failed: failed to check dirty status: %v\n", err) //nolint:errcheck
			return outcomeReviewFailed
		}
		if isDirty {
			fmt.Fprintf(r.stderr, "review_failed: reviewer worktree has uncommitted changes\n") //nolint:errcheck
			return outcomeReviewFailed
		}

		next, outcome := r.handleVerdict(eventLogPath, golemicDir, runSpanID, timeout, maxRounds, &round)
		if !next {
			return outcome
		}
	}
}

// handleVerdict processes the latest review verdict and returns (continueLoop, outcome).
// When continueLoop is true, outcome is empty and the caller should loop again.
func (r *Runner) handleVerdict(eventLogPath, golemicDir, runSpanID string, timeout time.Duration, maxRounds int, round *int) (continueLoop bool, outcome string) {
	verdict, err := r.latestReviewVerdict(eventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: review_submitted event missing or invalid\n") //nolint:errcheck
		return false, outcomeReviewFailed
	}

	roundCount := r.countReviewSubmittedEvents(eventLogPath)

	switch verdict {
	case "approved":
		return false, outcomeSuccess
	case "changes_requested":
		if roundCount >= maxRounds {
			r.postEscalationCommentWithSpan(eventLogPath, runSpanID, roundCount)
			return false, outcomeEscalated
		}
		findings, bodyErr := r.latestReviewBody(eventLogPath)
		if bodyErr != nil || findings == "" {
			fmt.Fprintf(r.stderr, "review_failed: EMPTY_FINDINGS: changes_requested review has an empty body\n") //nolint:errcheck
			return false, outcomeReviewFailed
		}
		*round++
		if o := r.runDevRetryAgent(golemicDir, eventLogPath, timeout, findings, runSpanID, *round); o != outcomeSuccess {
			return false, o
		}
		return true, ""
	default:
		fmt.Fprintf(r.stderr, "review_failed: unknown verdict %q\n", verdict) //nolint:errcheck
		return false, outcomeReviewFailed
	}
}
