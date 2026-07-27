package runner

import (
	"fmt"

	"golemic/internal/loop"
)

// stepSyncCI checks for a valid pr_opened event, resolves the PR number, then
// calls runPreReviewSyncGate and maps the outcome to a loop event.
//
// It owns the pr_opened precondition that was previously checked in orchestrate.
func (r *Runner) stepSyncCI(ctx *RunContext) loop.EventKey {
	if !r.hasPROpenedEvent(ctx.EventLogPath) {
		fmt.Fprintf(r.stderr, "dev_failed: pr_opened event missing or invalid\n") //nolint:errcheck
		return loop.EventCIFailed
	}
	prNumber, err := r.getPRNumber(ctx.EventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: pre-review sync: get PR number: %v\n", err) //nolint:errcheck
		return loop.EventCIFailed
	}
	outcome := r.runPreReviewSyncGate(ctx.Writer, prNumber, ctx.EventLogPath, ctx.Timeout)
	return mapSyncCIOutcome(outcome)
}

// mapSyncCIOutcome converts a runPreReviewSyncGate outcome string to a loop event.
// Possible outcomes from runPreReviewSyncGate: outcomeSuccess, outcomeDevFailed,
// outcomeStalled, outcomeAborted. outcomeTimeout is never returned but mapped
// defensively to EventAgentTimedOut.
func mapSyncCIOutcome(outcome string) loop.EventKey {
	switch outcome {
	case outcomeSuccess:
		return loop.EventCIGreen
	case outcomeTimeout:
		return loop.EventAgentTimedOut
	case outcomeStalled:
		return loop.EventAgentStalled
	case outcomeAborted:
		return loop.EventAgentAborted
	default:
		return loop.EventCIFailed
	}
}
