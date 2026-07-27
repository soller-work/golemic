package runner

import (
	"fmt"

	"golemic/internal/loop"
)

// runMachineFrom is a test-only helper that runs the loop machine from an arbitrary start step.
func (r *Runner) runMachineFrom(start loop.StepKey, ctx *RunContext) string {
	m := r.buildMachine(start, nil)
	final, err := m.Run(ctx)
	if err != nil {
		fmt.Fprintf(r.stderr, "%v\n", err) //nolint:errcheck
		return outcomeDevFailed
	}
	outcome, _ := terminalOutcome(final)
	return outcome
}
