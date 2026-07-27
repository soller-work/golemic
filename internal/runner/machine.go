package runner

import "golemic/internal/loop"

// buildMachine wires the loop machine. Both Run() and tests use it so the
// handler/transition/terminal wiring lives in exactly one place. onTransition
// may be nil (no audit observer).
func (r *Runner) buildMachine(start loop.StepKey, onTransition loop.TransitionObserver[RunContext]) *loop.Machine[RunContext] {
	return &loop.Machine[RunContext]{
		Transitions: loopTransitions(),
		Handlers: map[loop.StepKey]func(*RunContext) loop.EventKey{
			loop.StepPrepare:     r.stepPrepare,
			loop.StepRunDev:      r.stepRunDev,
			loop.StepSyncCI:      r.stepSyncCI,
			loop.StepRunReviewer: r.stepRunReviewer,
			loop.StepMergePR:     r.stepMergePR,
		},
		Start:        start,
		Terminals:    loopTerminals(),
		OnTransition: onTransition,
	}
}
