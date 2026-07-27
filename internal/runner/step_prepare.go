package runner

import (
	"fmt"

	"golemic/internal/loop"
	"golemic/internal/telemetry"
	"golemic/internal/worktree"
)

// stepPrepare runs, in order: skip check, --clean cleanup, collision check
// (skipped when ctx.Resume is true), resume hydration, and dev-worktree
// creation for fresh runs.
//
// Event mapping:
//
//	issue not eligible (skip)                           → loop.EventNotEligible
//	clean failure / collision detected / check error    → loop.EventPrepareFailed
//	resume hydration failure                            → loop.EventPrepareFailed
//	worktree-create failure                             → loop.EventPrepareFailed
//	                                                      (ctx.WorktreeCreateFailed = true)
//	all checks passed, worktree ready                   → loop.EventReady
//
// All stderr messages and event-log side effects are byte-identical to
// the pre-machine imperative code they replace.
func (r *Runner) stepPrepare(ctx *RunContext) loop.EventKey {
	// Skip check: issue must be OPEN
	if r.issue.State != "OPEN" {
		fmt.Fprintf(r.stderr, "skipped: issue #%d has state=%q (expected OPEN)\n", r.issueNum, r.issue.State)
		for _, lbl := range r.issue.Labels {
			if lbl.Name == "ready-for-agent" {
				fmt.Fprintf(r.stderr, "warning: issue #%d is not OPEN but still carries label \"ready-for-agent\" — please remove it manually\n", r.issueNum)
				break
			}
		}
		return loop.EventNotEligible
	}

	// --clean cleanup (before collision check, only when flag is set)
	if r.clean {
		if err := r.cleanArtifacts(); err != nil {
			fmt.Fprintln(r.stderr, err.Error())
			return loop.EventPrepareFailed
		}
	}

	// Collision check (skipped in resume mode)
	if !ctx.Resume {
		collision, err := r.checkAllCollisions()
		if err != nil {
			fmt.Fprintln(r.stderr, err.Error())
			return loop.EventPrepareFailed
		}
		if collision != nil {
			fmt.Fprintln(r.stderr, collision.Message)
			return loop.EventPrepareFailed
		}
		// Create dev worktree (turn 1: initial dev).
		// The worktree.create span is a child of the run span so callers must set
		// r.traceID and r.sink before entering the machine.
		r.turnCounter++
		_, endCreateDevWT := telemetry.StartSpan(r.sink, r.traceID, ctx.ParentSpanID, telemetry.SpanWorktreeCreate,
			map[string]any{"run_id": r.runID, "issue": r.issueNum, "worktree": "dev"})
		if err := worktree.Create(r.repoRoot, ctx.GolemicDir, r.runID, r.issueNum, "golemic-dev", r.executor, ctx.Writer, r.turnCounter); err != nil {
			endCreateDevWT(telemetry.StatusError, nil)
			fmt.Fprintf(r.stderr, "Failed to create dev worktree: %v\n", err)
			ctx.WorktreeCreateFailed = true
			return loop.EventPrepareFailed
		}
		endCreateDevWT(telemetry.StatusOK, nil)
		return loop.EventReady
	}

	if err := r.hydrateResume(ctx); err != nil {
		return loop.EventPrepareFailed
	}
	return loop.EventReady
}
