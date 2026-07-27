package runner

import (
	"fmt"

	"golemic/internal/loop"
)

// runMachineFrom is a test-only helper that runs the loop machine from an arbitrary start step.
func (r *Runner) runMachineFrom(start loop.StepKey, ctx *RunContext) string {
	m := &loop.Machine[RunContext]{
		Transitions: loopTransitions(),
		Handlers: map[loop.StepKey]func(*RunContext) loop.EventKey{
			loop.StepPrepare:     r.stepPrepare,
			loop.StepRunDev:      r.stepRunDev,
			loop.StepSyncCI:      r.stepSyncCI,
			loop.StepRunReviewer: r.stepRunReviewer,
			loop.StepMergePR:     r.stepMergePR,
		},
		Start:     start,
		Terminals: loopTerminals(),
	}
	final, err := m.Run(ctx)
	if err != nil {
		fmt.Fprintf(r.stderr, "%v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}
	outcome, _ := terminalOutcome(final)
	return outcome
}
