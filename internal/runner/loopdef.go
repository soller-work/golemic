package runner

import (
	"time"

	"golemic/internal/loop"
	"golemic/internal/worktree"
)

// DevMode distinguishes the first dev run from a reviewer-directed retry.
type DevMode string

const (
	DevModeInitial           DevMode = "initial"
	DevModeRetryWithFindings DevMode = "retry_with_findings"
)

// RunContext is the shared mutable context threaded through every step handler.
// Guards in the transition table read (never write) this struct.
type RunContext struct {
	// DevAttempt is the zero-based gate-retry attempt count within the current round.
	// 0 = first attempt; incremented by the RUN_DEV handler on each gate rejection.
	DevAttempt int

	// Round is the reviewer-round counter (incremented after each RUN_REVIEWER turn).
	Round int

	// MaxRounds is the maximum number of reviewer rounds (from config).
	MaxRounds int

	// Resume is true when the runner was invoked with --resume.
	Resume bool

	// ResumeVerdict is the last review verdict found on GitHub during resume preparation.
	// Valid values: "" (none / fresh), "approved", "changes_requested".
	ResumeVerdict string

	// DevMode indicates whether the RUN_DEV step should use an initial or retry prompt.
	DevMode DevMode

	// Operational fields: set once when the RunContext is created in Run().
	GolemicDir   string
	EventLogPath string
	Timeout      time.Duration
	ParentSpanID string

	// WorktreeCreateFailed is set by stepPrepare when dev worktree creation
	// fails, routing PREPARE_FAILED to TERMINAL_DEV_FAILED instead of TERMINAL_ABORTED.
	WorktreeCreateFailed bool

	// PrepareFailKind disambiguates non-worktree PREPARE_FAILED outcomes.
	// Valid values: "" (abort), "dev_failed", "review_failed", "escalated".
	PrepareFailKind string

	// PRState carries the resume PR state needed by MERGE_PR fast paths.
	PRState string

	// GateReason is the rejection reason from the last §10 gate rejection.
	GateReason string

	// Findings and FindingsJSON carry reviewer feedback into the dev-retry prompt.
	Findings     string
	FindingsJSON string

	// Writer is the event writer used for worktree-creation events.
	Writer worktree.EventWriter

	// ReviewerWorktreeExists tracks whether a reviewer worktree was already
	// created in this session so subsequent rounds clean it up first.
	ReviewerWorktreeExists bool

	// InMergeReReview is true when the machine is in a post-merge-conflict
	// re-review cycle (MERGE_PR resolved a conflict and routed to RUN_REVIEWER).
	InMergeReReview bool

	// MergeReReviewRound counts reviewer turns taken during the merge re-review
	// phase, independent of Round/MaxRounds.
	MergeReReviewRound int

	// MaxMergeReReviewRounds caps the merge re-review budget.
	MaxMergeReReviewRounds int
}

// loopTerminals returns the set of terminal steps.
func loopTerminals() map[loop.StepKey]bool {
	return map[loop.StepKey]bool{
		loop.StepTerminalSuccess:      true,
		loop.StepTerminalDevFailed:    true,
		loop.StepTerminalReviewFailed: true,
		loop.StepTerminalEscalated:    true,
		loop.StepTerminalMergeFailed:  true,
		loop.StepTerminalTimeout:      true,
		loop.StepTerminalStalled:      true,
		loop.StepTerminalAborted:      true,
		loop.StepTerminalSkipped:      true,
	}
}

type terminalResult struct {
	outcome  string
	exitCode int
}

var terminalOutcomeTable = map[loop.StepKey]terminalResult{
	loop.StepTerminalSuccess:      {outcomeSuccess, 0},
	loop.StepTerminalSkipped:      {outcomeSkipped, 0},
	loop.StepTerminalDevFailed:    {outcomeDevFailed, 1},
	loop.StepTerminalReviewFailed: {outcomeReviewFailed, 1},
	loop.StepTerminalEscalated:    {outcomeEscalated, 1},
	loop.StepTerminalMergeFailed:  {outcomeMergeFailed, 1},
	loop.StepTerminalTimeout:      {outcomeTimeout, 1},
	loop.StepTerminalStalled:      {outcomeStalled, 1},
	loop.StepTerminalAborted:      {outcomeAborted, 1},
}

// terminalOutcome maps a terminal step to the outcome string and exit code.
// Panics on unknown step — totality is enforced by the step table above.
func terminalOutcome(s loop.StepKey) (string, int) {
	r, ok := terminalOutcomeTable[s]
	if !ok {
		panic("terminalOutcome: unknown terminal step: " + string(s))
	}
	return r.outcome, r.exitCode
}

// loopTransitions returns the canonical golemic transition table.
// Guards for a given (From, Event) pair are disjoint and total over RunContext.
func loopTransitions() []loop.Transition[RunContext] {
	return []loop.Transition[RunContext]{
		// PREPARE: route based on eligibility and resume state.
		{From: loop.StepPrepare, Event: loop.EventNotEligible, To: loop.StepTerminalSkipped},
		{From: loop.StepPrepare, Event: loop.EventPrepareFailed, To: loop.StepTerminalDevFailed,
			Guard: func(rc *RunContext) bool {
				return rc.PrepareFailKind == "dev_failed" || (rc.WorktreeCreateFailed && rc.PrepareFailKind == "")
			}},
		{From: loop.StepPrepare, Event: loop.EventPrepareFailed, To: loop.StepTerminalReviewFailed,
			Guard: func(rc *RunContext) bool { return rc.PrepareFailKind == "review_failed" }},
		{From: loop.StepPrepare, Event: loop.EventPrepareFailed, To: loop.StepTerminalEscalated,
			Guard: func(rc *RunContext) bool { return rc.PrepareFailKind == "escalated" }},
		{From: loop.StepPrepare, Event: loop.EventPrepareFailed, To: loop.StepTerminalAborted,
			Guard: func(rc *RunContext) bool { return !rc.WorktreeCreateFailed && rc.PrepareFailKind == "" }},
		// EventReady routes based on resume hydration.
		{From: loop.StepPrepare, Event: loop.EventReady, To: loop.StepRunDev,
			Guard: func(rc *RunContext) bool { return !rc.Resume || rc.ResumeVerdict == "changes_requested" }},
		{From: loop.StepPrepare, Event: loop.EventReady, To: loop.StepRunReviewer,
			Guard: func(rc *RunContext) bool { return rc.Resume && rc.ResumeVerdict == "" }},
		{From: loop.StepPrepare, Event: loop.EventReady, To: loop.StepMergePR,
			Guard: func(rc *RunContext) bool { return rc.Resume && rc.ResumeVerdict == "approved" }},

		// RUN_DEV: success → wait for CI; gate retry while attempts remain.
		{From: loop.StepRunDev, Event: loop.EventDevDone, To: loop.StepSyncCI},
		{From: loop.StepRunDev, Event: loop.EventDevFailed, To: loop.StepTerminalDevFailed},
		{From: loop.StepRunDev, Event: loop.EventDevGateRejected, To: loop.StepRunDev,
			Guard: func(rc *RunContext) bool { return rc.DevAttempt < 3 }},
		{From: loop.StepRunDev, Event: loop.EventDevGateRejected, To: loop.StepTerminalDevFailed,
			Guard: func(rc *RunContext) bool { return rc.DevAttempt >= 3 }},
		{From: loop.StepRunDev, Event: loop.EventAgentTimedOut, To: loop.StepTerminalTimeout},
		{From: loop.StepRunDev, Event: loop.EventAgentStalled, To: loop.StepTerminalStalled},
		{From: loop.StepRunDev, Event: loop.EventAgentAborted, To: loop.StepTerminalAborted},

		// SYNC_CI: green → reviewer; conflict unresolved or other failure → dev failed.
		{From: loop.StepSyncCI, Event: loop.EventCIGreen, To: loop.StepRunReviewer},
		{From: loop.StepSyncCI, Event: loop.EventCIFailed, To: loop.StepTerminalDevFailed},
		{From: loop.StepSyncCI, Event: loop.EventConflictUnresolved, To: loop.StepTerminalDevFailed},
		{From: loop.StepSyncCI, Event: loop.EventAgentTimedOut, To: loop.StepTerminalTimeout},
		{From: loop.StepSyncCI, Event: loop.EventAgentStalled, To: loop.StepTerminalStalled},
		{From: loop.StepSyncCI, Event: loop.EventAgentAborted, To: loop.StepTerminalAborted},

		// RUN_REVIEWER: approved → merge; changes/precheck → retry dev or escalate.
		// Pre-approval path uses Round/MaxRounds; merge re-review uses MergeReReviewRound/MaxMergeReReviewRounds.
		{From: loop.StepRunReviewer, Event: loop.EventReviewApproved, To: loop.StepMergePR},
		{From: loop.StepRunReviewer, Event: loop.EventReviewFailed, To: loop.StepTerminalReviewFailed},
		{From: loop.StepRunReviewer, Event: loop.EventChangesRequested, To: loop.StepRunDev,
			Guard: func(rc *RunContext) bool { return !rc.InMergeReReview && rc.Round < rc.MaxRounds }},
		{From: loop.StepRunReviewer, Event: loop.EventChangesRequested, To: loop.StepTerminalEscalated,
			Guard: func(rc *RunContext) bool { return !rc.InMergeReReview && rc.Round >= rc.MaxRounds }},
		{From: loop.StepRunReviewer, Event: loop.EventChangesRequested, To: loop.StepRunDev,
			Guard: func(rc *RunContext) bool {
				return rc.InMergeReReview && rc.MergeReReviewRound < rc.MaxMergeReReviewRounds
			}},
		{From: loop.StepRunReviewer, Event: loop.EventChangesRequested, To: loop.StepTerminalEscalated,
			Guard: func(rc *RunContext) bool {
				return rc.InMergeReReview && rc.MergeReReviewRound >= rc.MaxMergeReReviewRounds
			}},
		{From: loop.StepRunReviewer, Event: loop.EventPrecheckFailed, To: loop.StepRunDev,
			Guard: func(rc *RunContext) bool { return !rc.InMergeReReview && rc.Round < rc.MaxRounds }},
		{From: loop.StepRunReviewer, Event: loop.EventPrecheckFailed, To: loop.StepTerminalEscalated,
			Guard: func(rc *RunContext) bool { return !rc.InMergeReReview && rc.Round >= rc.MaxRounds }},
		{From: loop.StepRunReviewer, Event: loop.EventPrecheckFailed, To: loop.StepRunDev,
			Guard: func(rc *RunContext) bool {
				return rc.InMergeReReview && rc.MergeReReviewRound < rc.MaxMergeReReviewRounds
			}},
		{From: loop.StepRunReviewer, Event: loop.EventPrecheckFailed, To: loop.StepTerminalEscalated,
			Guard: func(rc *RunContext) bool {
				return rc.InMergeReReview && rc.MergeReReviewRound >= rc.MaxMergeReReviewRounds
			}},
		{From: loop.StepRunReviewer, Event: loop.EventAgentTimedOut, To: loop.StepTerminalTimeout},
		{From: loop.StepRunReviewer, Event: loop.EventAgentStalled, To: loop.StepTerminalStalled},
		{From: loop.StepRunReviewer, Event: loop.EventAgentAborted, To: loop.StepTerminalAborted},

		// MERGE_PR: merged → success; conflict resolved → re-review; conflict unresolved → dev failed.
		{From: loop.StepMergePR, Event: loop.EventMerged, To: loop.StepTerminalSuccess},
		{From: loop.StepMergePR, Event: loop.EventMergeFailed, To: loop.StepTerminalMergeFailed},
		{From: loop.StepMergePR, Event: loop.EventConflictResolved, To: loop.StepRunReviewer},
		{From: loop.StepMergePR, Event: loop.EventConflictUnresolved, To: loop.StepTerminalDevFailed},
		{From: loop.StepMergePR, Event: loop.EventAgentTimedOut, To: loop.StepTerminalTimeout},
		{From: loop.StepMergePR, Event: loop.EventAgentStalled, To: loop.StepTerminalStalled},
		{From: loop.StepMergePR, Event: loop.EventAgentAborted, To: loop.StepTerminalAborted},
	}
}
