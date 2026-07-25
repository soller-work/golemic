package runner

import "fmt"

// DevLoopState is a named phase in the dev-loop state machine.
type DevLoopState string

const (
	// Ordered progression states.
	StateDevImplement         DevLoopState = "DEV_IMPLEMENT"
	StateDevDoneReceived      DevLoopState = "DEV_DONE_RECEIVED"
	StateRunnerVerify         DevLoopState = "RUNNER_VERIFY"
	StatePROpened             DevLoopState = "PR_OPENED"
	StateCIWait               DevLoopState = "CI_WAIT"
	StateReviewerPrecheck     DevLoopState = "REVIEWER_PRECHECK"
	StateReviewerRequired     DevLoopState = "REVIEWER_REVIEW_REQUIRED"
	StateReviewSubmitted      DevLoopState = "REVIEW_SUBMITTED"
	StateDevRetryWithFindings DevLoopState = "DEV_RETRY_WITH_FINDINGS"
	StateMerge                DevLoopState = "MERGE"

	// Terminal failure states.
	StateDevFailed    DevLoopState = "DEV_FAILED"
	StateReviewFailed DevLoopState = "REVIEW_FAILED"
	StateEscalated    DevLoopState = "ESCALATED"
	StateMergeFailed  DevLoopState = "MERGE_FAILED"

	// Gate-rejected sub-states, classified by the §10 tree-green criterion.
	// Both currently route to the bounded LLM gate-retry; #213 redirects
	// StateGateRejectedGreen to deterministic finalize.
	StateGateRejectedGreen DevLoopState = "GATE_REJECTED_GREEN"
	StateGateRejectedRed   DevLoopState = "GATE_REJECTED_RED"
)

// StateError is produced when a required exit predicate is unmet at a state boundary.
// A missing required terminal tool call (gm_dev_done, gm_review_submit) produces a
// StateError rather than an implicit retry or silent nil return.
type StateError struct {
	State     DevLoopState
	Predicate string
	Message   string
}

func (e *StateError) Error() string {
	return fmt.Sprintf("state %s: predicate %q unmet: %s", e.State, e.Predicate, e.Message)
}

// classifyDevGate maps the §10 tree-green criterion to the gate-rejected transition.
// isTreeGreen must equal gmbroker.Broker.IsTreeGreen(): last gm_project_check OK and
// current working-tree fingerprint matches.
//
// Returns StateGateRejectedGreen when the tree is green (slot for #213 to redirect to
// deterministic finalize) or StateGateRejectedRed otherwise.
func classifyDevGate(isTreeGreen bool) DevLoopState {
	if isTreeGreen {
		return StateGateRejectedGreen
	}
	return StateGateRejectedRed
}

// reviewerFreshnessMet is the exit predicate for REVIEWER_REVIEW_REQUIRED.
// hasFreshCurrentRound must be true when either the GM broker captured an accepted
// gm_review_submit in this invocation, or a review_submitted event was written to
// the event log during this round.  A stale prior-round event never satisfies it.
func reviewerFreshnessMet(hasFreshCurrentRound bool) bool {
	return hasFreshCurrentRound
}

// devDonePredicateMet is the exit predicate for DEV_IMPLEMENT and
// DEV_RETRY_WITH_FINDINGS.  It returns true when the broker captured an accepted
// gm_dev_done in the current invocation.
func devDonePredicateMet(hasDevDone bool) bool {
	return hasDevDone
}

// terminalOutcome maps a terminal DevLoopState to the caller-visible outcome string,
// preserving all existing external outcome constants.
func terminalOutcome(s DevLoopState) string {
	switch s {
	case StateMerge:
		return outcomeSuccess
	case StateDevFailed:
		return outcomeDevFailed
	case StateReviewFailed:
		return outcomeReviewFailed
	case StateEscalated:
		return outcomeEscalated
	case StateMergeFailed:
		return outcomeMergeFailed
	default:
		return outcomeDevFailed
	}
}
