# golemic Step Machine

This document is the canonical specification for the golemic step machine, implemented across the `internal/loop` and `internal/runner` packages. All later slices (see [#240](https://github.com/soller-work/golemic/issues/240)) must treat this document as the source of truth.

## Overview

The step machine replaces the ad-hoc `outcome*`-string orchestration with an explicit, guarded transition table. A single `RunContext` value flows through every step handler, and guards read it to determine which edge to follow.

**Vocabulary:**

- **StepKey** — names what to do next (a command, e.g. `RUN_DEV`).
- **EventKey** — names a fact produced by a handler (e.g. `DEV_DONE`).
- **Guard** — a predicate `func(*RunContext) bool` attached to a transition edge. `nil` means unconditional. Guards for any `(From, Event)` pair must be disjoint and total over all reachable `RunContext` values.

## Steps

| Step | Description |
|---|---|
| `PREPARE` | Preflight, eligibility check, collision detection, resume hydration. |
| `RUN_DEV` | Run the dev agent (initial prompt or retry-with-findings). Handles gate retries internally via `DevAttempt`. |
| `SYNC_CI` | Wait for CI checks to settle. |
| `RUN_REVIEWER` | Run the reviewer agent. Handles precheck and verdict routing. |
| `MERGE_PR` | Merge the pull request. |
| `TERMINAL_SUCCESS` | Run finished: merged successfully. |
| `TERMINAL_DEV_FAILED` | Run finished: dev agent failed or CI failed after dev. |
| `TERMINAL_REVIEW_FAILED` | Run finished: reviewer explicitly failed the review. |
| `TERMINAL_ESCALATED` | Run finished: reviewer requested changes but round budget exhausted. |
| `TERMINAL_MERGE_FAILED` | Run finished: merge step failed. |
| `TERMINAL_TIMEOUT` | Run finished: agent or CI timed out. |
| `TERMINAL_STALLED` | Run finished: agent stalled (thinking loop). |
| `TERMINAL_ABORTED` | Run finished: agent aborted or internal error. |
| `TERMINAL_SKIPPED` | Run finished: issue not eligible (already merged, closed, etc.). |

## RunContext

`RunContext` is the single shared value threaded through every step handler. Guards read it; handlers may write it.

```go
type DevMode string

const (
    DevModeInitial           DevMode = "initial"
    DevModeRetryWithFindings DevMode = "retry_with_findings"
)

type RunContext struct {
    // DevAttempt is the zero-based gate-retry attempt count within the current round.
    // 0 = first attempt; incremented by the RUN_DEV handler on each gate rejection.
    DevAttempt int

    // Round is the reviewer-round counter (incremented after each RUN_REVIEWER turn).
    Round int

    // MaxRounds is the maximum number of reviewer rounds (from config, default 5).
    MaxRounds int

    // Resume is true when the runner was invoked with --resume.
    Resume bool

    // ResumeVerdict is the last review verdict found on GitHub during resume preparation.
    // Valid values: "" (none / fresh), "approved", "changes_requested".
    ResumeVerdict string

    // DevMode indicates whether RUN_DEV should use an initial or retry-with-findings prompt.
    DevMode DevMode
}
```

## Transition Table

Unconditional edges have no guard. Guarded edges are mutually exclusive within each `(From, Event)` group.

| From | Event | To | Guard |
|---|---|---|---|
| `PREPARE` | `NOT_ELIGIBLE` | `TERMINAL_SKIPPED` | — |
| `PREPARE` | `PREPARE_FAILED` | `TERMINAL_DEV_FAILED` | `WorktreeCreateFailed` |
| `PREPARE` | `PREPARE_FAILED` | `TERMINAL_ABORTED` | `!WorktreeCreateFailed` |
| `PREPARE` | `READY` | `RUN_DEV` | `!Resume \|\| ResumeVerdict != "approved"` |
| `PREPARE` | `READY` | `RUN_REVIEWER` | `Resume && ResumeVerdict == "approved"` |
| `RUN_DEV` | `DEV_DONE` | `SYNC_CI` | — |
| `RUN_DEV` | `DEV_FAILED` | `TERMINAL_DEV_FAILED` | — |
| `RUN_DEV` | `DEV_GATE_REJECTED` | `RUN_DEV` | `DevAttempt < 2` |
| `RUN_DEV` | `DEV_GATE_REJECTED` | `TERMINAL_DEV_FAILED` | `DevAttempt >= 2` |
| `RUN_DEV` | `AGENT_TIMED_OUT` | `TERMINAL_TIMEOUT` | — |
| `RUN_DEV` | `AGENT_STALLED` | `TERMINAL_STALLED` | — |
| `RUN_DEV` | `AGENT_ABORTED` | `TERMINAL_ABORTED` | — |
| `SYNC_CI` | `CI_GREEN` | `RUN_REVIEWER` | — |
| `SYNC_CI` | `CI_FAILED` | `TERMINAL_DEV_FAILED` | — |
| `SYNC_CI` | `AGENT_TIMED_OUT` | `TERMINAL_TIMEOUT` | — |
| `SYNC_CI` | `AGENT_STALLED` | `TERMINAL_STALLED` | — |
| `SYNC_CI` | `AGENT_ABORTED` | `TERMINAL_ABORTED` | — |
| `RUN_REVIEWER` | `REVIEW_APPROVED` | `MERGE_PR` | — |
| `RUN_REVIEWER` | `REVIEW_FAILED` | `TERMINAL_REVIEW_FAILED` | — |
| `RUN_REVIEWER` | `CHANGES_REQUESTED` | `RUN_DEV` | `Round < MaxRounds` |
| `RUN_REVIEWER` | `CHANGES_REQUESTED` | `TERMINAL_ESCALATED` | `Round >= MaxRounds` |
| `RUN_REVIEWER` | `PRECHECK_FAILED` | `RUN_DEV` | `Round < MaxRounds` |
| `RUN_REVIEWER` | `PRECHECK_FAILED` | `TERMINAL_ESCALATED` | `Round >= MaxRounds` |
| `RUN_REVIEWER` | `AGENT_TIMED_OUT` | `TERMINAL_TIMEOUT` | — |
| `RUN_REVIEWER` | `AGENT_STALLED` | `TERMINAL_STALLED` | — |
| `RUN_REVIEWER` | `AGENT_ABORTED` | `TERMINAL_ABORTED` | — |
| `MERGE_PR` | `MERGED` | `TERMINAL_SUCCESS` | — |
| `MERGE_PR` | `MERGE_FAILED` | `TERMINAL_MERGE_FAILED` | — |
| `MERGE_PR` | `AGENT_TIMED_OUT` | `TERMINAL_TIMEOUT` | — |
| `MERGE_PR` | `AGENT_STALLED` | `TERMINAL_STALLED` | — |
| `MERGE_PR` | `AGENT_ABORTED` | `TERMINAL_ABORTED` | — |

## Terminal Mapping

| Terminal Step | Outcome String | Exit Code |
|---|---|---|
| `TERMINAL_SUCCESS` | `success` | 0 |
| `TERMINAL_SKIPPED` | `skipped` | 0 |
| `TERMINAL_DEV_FAILED` | `dev_failed` | 1 |
| `TERMINAL_REVIEW_FAILED` | `review_failed` | 1 |
| `TERMINAL_ESCALATED` | `escalated` | 1 |
| `TERMINAL_MERGE_FAILED` | `merge_failed` | 1 |
| `TERMINAL_TIMEOUT` | `timeout` | 1 |
| `TERMINAL_STALLED` | `stalled` | 1 |
| `TERMINAL_ABORTED` | `aborted` | 1 |

## Boundary: Preflight Before the Event Log

The preflight gate (`r.preflighter.Check()`) runs **before** the event-log writer is created. Because there is nothing to audit without a log, preflight failures are handled imperatively in `Run()` and are outside the machine. Everything from `run_started` onward is machine-driven.

## Guard Invariants

For every `(From, Event)` group with more than one edge:

1. **Disjoint**: no `RunContext` value can satisfy two guards simultaneously.
2. **Total**: every reachable `RunContext` value satisfies exactly one guard.

The test `TestLoopTransitions_AC6_GuardDisjointness` in `internal/runner/loopdef_test.go` verifies this over a fixture grid of boundary values.

## Implementation Notes

- `internal/loop` is dependency-free (stdlib only, no import of `internal/runner`).
- `internal/runner/loopdef.go` defines `RunContext`, `loopTransitions()`, `terminalOutcome()`, and `loopTerminals()`.
- `internal/runner/step_prepare.go` implements `stepPrepare`: skip check → `--clean` cleanup → collision check (skipped in resume mode) → dev worktree creation.
- Fresh runs in `Run()` start the machine at `PREPARE`; the resume fork (Slice 6) still bypasses `PREPARE` via `resumeOrchestrate`.
- `DevAttempt < 3` allows gate retries at attempts 0, 1, and 2 (3 total invocations). At attempt 3 the machine transitions to `TERMINAL_DEV_FAILED`.
- `Round` starts at 1 and is incremented after each `RUN_REVIEWER` turn. The escalation boundary is `Round >= MaxRounds` (default `MaxRounds = 5`).
