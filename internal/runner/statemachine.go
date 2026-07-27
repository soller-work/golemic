package runner

import "fmt"

// DevLoopState is a named phase in the dev-loop state machine.
type DevLoopState string

const (
	// StateReviewerRequired is the reviewer gate whose exit predicate is
	// reviewerFreshnessMet; a missing fresh gm_review_submit produces a StateError.
	StateReviewerRequired DevLoopState = "REVIEWER_REVIEW_REQUIRED"
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

// reviewerFreshnessMet is the exit predicate for REVIEWER_REVIEW_REQUIRED.
// hasFreshCurrentRound must be true when either the GM broker captured an accepted
// gm_review_submit in this invocation, or a review_submitted event was written to
// the event log during this round.  A stale prior-round event never satisfies it.
func reviewerFreshnessMet(hasFreshCurrentRound bool) bool {
	return hasFreshCurrentRound
}
