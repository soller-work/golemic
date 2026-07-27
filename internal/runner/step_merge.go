package runner

import (
	"fmt"

	"golemic/internal/loop"
)

// stepMergePR wraps runMergePhase and maps its outcome string to a loop event.
// When a merge-time rebase conflict is resolved, sets ctx.InMergeReReview = true
// and routes to RUN_REVIEWER for a fresh verdict before merging.
func (r *Runner) stepMergePR(ctx *RunContext) loop.EventKey {
	if ctx.PRState == "MERGED" {
		return loop.EventMerged
	}
	outcome := r.runMergePhase(ctx.Writer, ctx.EventLogPath)
	switch outcome {
	case outcomeConflictResolved:
		ctx.InMergeReReview = true
		return loop.EventConflictResolved
	case outcomeConflictUnresolved:
		return loop.EventConflictUnresolved
	}
	return mapMergePROutcome(r, outcome)
}

func mapMergePROutcome(r *Runner, outcome string) loop.EventKey {
	switch outcome {
	case outcomeSuccess:
		return loop.EventMerged
	case outcomeMergeFailed:
		return loop.EventMergeFailed
	default:
		fmt.Fprintf(r.stderr, "merge_failed: unexpected outcome %q from runMergePhase\n", outcome) //nolint:errcheck
		return loop.EventMergeFailed
	}
}
