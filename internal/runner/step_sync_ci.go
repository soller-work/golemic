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

func mapSyncCIOutcome(outcome string) loop.EventKey {
	if ev, ok := agentFailureEvent(outcome); ok {
		return ev
	}
	if outcome == outcomeSuccess {
		return loop.EventCIGreen
	}
	return loop.EventCIFailed
}
