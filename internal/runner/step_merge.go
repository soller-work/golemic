package runner

import (
	"fmt"

	"golemic/internal/loop"
)

// stepMergePR wraps runMergePhase and maps its outcome string to a loop event.
// runMergePhase only returns outcomeSuccess or outcomeMergeFailed today;
// any unexpected string is routed to EventMergeFailed with a stderr note.
func (r *Runner) stepMergePR(ctx *RunContext) loop.EventKey {
	outcome := r.runMergePhase(ctx.Writer, ctx.EventLogPath)
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
