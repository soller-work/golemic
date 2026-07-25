# Dev-Loop Observability Findings and Optimization Backlog

This document captures observations from watching golemic run `issue-186-20260724T100527Z` end-to-end. The goal is not to optimize for that specific issue type. Golemic must handle arbitrary software tickets. The findings below therefore focus on general dev-loop mechanics, observability, reviewer/dev state handling, and Pareto-level optimization opportunities.

## Observed Run Summary

Run path:

```text
/Users/sergej/.golemic/golemic/runs/issue-186-20260724T100527Z
```

Final outcome:

```text
dev_failed
```

High-level timeline:

```text
12:05  run started
12:05  dev round 1 started
12:24  dev round 1 completed
12:24  PR #206 opened
12:26  CI wait finished green
12:26  reviewer worktree created
12:34  reviewer precheck green
12:35  reviewer submitted changes_requested
12:36  dev round 2 started
12:57  dev round 2 completed
12:57  reviewer round 2 worktree recreated
12:58  reviewer precheck green
12:58  reviewer round 2 completed without a fresh substantive review
12:58  dev/gate retry started
13:00  dev/gate retry completed
13:00  another dev/gate retry started
13:01  run finished dev_failed
```

Notable measurements from the observed artifacts:

- First dev round reached roughly 214k total tokens and about 98 turns before completion.
- First reviewer round reached roughly 138k total tokens.
- Second dev round reached roughly 238k total tokens.
- Later gate retry attempts still carried very large prompt/context costs for little work.
- `gm_project_check` took roughly 55-60 seconds due largely to slower test packages such as `internal/agent`.
- Activity logs were overwritten across rounds, which destroyed useful forensic data from earlier rounds.

## Main Findings

### 1. Reviewer state can become stale across rounds

After the first reviewer submitted `changes_requested`, the dev agent addressed the finding and verification passed. Reviewer round 2 then appeared to report that a review had already been submitted instead of performing and submitting a fresh review for the updated PR state.

This is a major loop-level correctness risk. A stale review state can cause the runner to continue as if the previous `changes_requested` verdict is still current, even after the dev has addressed the finding.

Generalized risk:

- Any issue type can be affected.
- A correct fix can be blocked by stale reviewer session or stale review-submission state.
- The runner can enter unnecessary dev/gate retries despite a green tree.

Recommended changes:

- Give each reviewer round a fresh session identity, or otherwise isolate reviewer memory by PR head SHA and review round.
- Require exactly one fresh `gm_review_submit` per reviewer invocation for the current PR head SHA.
- Have the runner enforce this requirement structurally instead of relying only on the prompt.
- Treat “review already submitted” as invalid for a new reviewer round unless it references the current round and current head SHA.

Potential issue title:

```text
runner: require fresh reviewer verdict per PR head and isolate reviewer round state
```

### 2. Activity logs are overwritten across rounds

The run initially produced a large `dev.activity.jsonl`, but later dev rounds truncated or replaced the same file. This made it difficult to inspect the original dev behavior after the run advanced.

Generalized risk:

- Loss of observability for failed or expensive runs.
- Harder root-cause analysis for token waste, stale state, retries, and tool-use failures.
- Harder comparison between attempts or rounds.

Recommended changes:

- Write activity logs with round and attempt in the filename:

```text
dev-r1-a0.activity.jsonl
reviewer-r1-a0.activity.jsonl
dev-r2-a0.activity.jsonl
dev-r3-a1.activity.jsonl
```

- Optionally keep `dev.latest.activity.jsonl` / `reviewer.latest.activity.jsonl` as convenience symlinks or copies.
- Add event-log references to transcript/activity file paths for each agent turn.

Potential issue title:

```text
runner: preserve per-round agent activity logs instead of overwriting role logs
```

### 3. Gate retry uses LLM work for machine-checkable states

A gate retry was started even when the important remaining action was to run verification and continue if green. The agent called `gm_project_check`; verification passed, but the run still ended as `dev_failed` after further confusing retry behavior.

Generalized risk:

- LLM calls are used for deterministic control-flow states.
- Green verification can still lead to failed loop outcomes if the required final tool call or state transition is not handled cleanly.
- Cost and latency increase without adding reasoning value.

Recommended changes:

- Model gate retries as runner-owned deterministic states.
- If the only question is “does verification pass?”, the runner should call the verify command itself, not start a full dev agent.
- Start a dev agent only when verification fails and there are actionable failures to fix.
- If verification passes after a gate retry, proceed to the next state without requiring a new LLM finalization step.

Potential issue title:

```text
runner: make green gate retries deterministic and avoid unnecessary dev-agent turns
```

### 4. The loop needs stronger state-machine semantics

The observed behavior suggests some states are inferred from agent behavior and prior tool calls rather than enforced by the runner. This is fragile for a software-encoded dev loop.

Recommended state model:

```text
DEV_IMPLEMENT
→ DEV_DONE_RECEIVED
→ RUNNER_VERIFY
→ PR_OPENED
→ CI_WAIT
→ REVIEWER_PRECHECK
→ REVIEWER_REVIEW_REQUIRED
→ REVIEW_SUBMITTED
→ if changes_requested: DEV_RETRY_WITH_FINDINGS
→ if approved: MERGE
```

Each state should have explicit machine-checkable acceptance criteria:

- Dev implementation state ends only after accepted `gm_dev_done`.
- Reviewer state ends only after a fresh accepted `gm_review_submit` for the current PR head SHA.
- Verification state is runner-owned and does not require LLM participation when no code change is needed.
- A stale tool result from a previous round must not satisfy the current round.
- A missing required tool call should be a structured state failure, not an implicit retry loop.

Potential issue title:

```text
runner: formalize dev-loop phases as explicit state machine with required tool-call gates
```

### 5. Prompt and context size grow too much across retries

Later rounds carried very large token contexts even when the task was narrow, such as verifying or addressing one reviewer finding. Some turns had large input with little or no cache reuse.

Generalized risk:

- High cost on all issue types.
- Slower loops.
- Higher chance of model distraction from stale context.
- Retry prompts may include too much historical material instead of the current actionable delta.

Recommended changes:

- Create compact retry prompts that include only:
  - issue number/title;
  - current PR/head SHA;
  - current reviewer findings;
  - current verification failure, if any;
  - changed files summary;
  - required next tool call.
- Avoid embedding long prior transcripts or duplicated issue specs in retries.
- Prefer stable prompt prefixes to improve provider cache reuse.
- Track and report cache-read/cache-write effectiveness by round.

Potential issue title:

```text
runner: shrink retry prompts and improve cache-friendly prompt structure
```

### 6. Full verification is expensive and repeated

`gm_project_check` runs the full configured verify command. This is required before merge, but expensive when repeatedly used during implementation and retry loops.

Observed pattern:

- Full verify failed on a single test.
- Full verify failed on lint unused code.
- Full verify failed on unused imports.
- Full verify eventually passed.
- Full verify was run again in a gate retry.

Recommended changes:

- Keep full verification at hard gates: before PR update/submit and before merge.
- Encourage or support targeted local checks while editing:

```text
go test ./internal/agent ./internal/runner ./cmd/golemic
go test ./internal/prompt
go test ./...
```

- Let `gm_project_check` return structured failure metadata plus suggested targeted commands.
- Consider a `gm_project_check_fast` or `gm_targeted_check` helper that can run affected-package checks based on changed files.

Potential issue title:

```text
runner: add targeted verification path before full project gate checks
```

### 7. Reviewer precheck latency and purpose should be examined

Reviewer precheck was green and useful, but it added a significant gap before the actual reviewer agent started. The precheck itself is deterministic and valuable, but should be clearly separated from reviewer reasoning.

Recommended changes:

- Report precheck duration as a first-class event/span.
- If precheck fails, do not start reviewer; route directly to deterministic gate/dev retry.
- If precheck passes, provide the reviewer a compact precheck summary rather than large logs.

Potential issue title:

```text
runner: expose reviewer precheck duration and route precheck failures without reviewer LLM
```

## Pareto Priority

The largest general-purpose improvements appear to be:

1. **Fresh reviewer verdict enforcement**  
   Prevents stale `changes_requested` state and unnecessary follow-up loops.

2. **Per-round activity log preservation**  
   Makes every failed or expensive run diagnosable.

3. **Deterministic gate retry handling**  
   Avoids LLM calls for machine-checkable verification states.

4. **Explicit state-machine gates**  
   Turns the dev loop into enforceable software behavior rather than prompt-dependent convention.

5. **Retry prompt compaction and cache discipline**  
   Reduces cost and stale-context confusion across arbitrary issue types.

6. **Targeted verification support**  
   Reduces edit-test latency while preserving full gate confidence.

## Suggested Backlog Slices

### Slice A: Preserve agent activity logs by round and attempt

Acceptance criteria:

- Agent activity filenames include role, round, and attempt.
- Existing convenience paths may remain as latest aliases.
- Event log records activity/transcript paths for each agent turn.
- Tests prove a second dev/reviewer round does not overwrite the first round's activity log.

### Slice B: Enforce fresh reviewer submission per round

Acceptance criteria:

- Runner records current PR head SHA before reviewer invocation.
- `gm_review_submit` records PR number, head SHA, round, verdict, and review ID.
- Reviewer round succeeds only if a fresh submission exists for that round/head SHA.
- A stale “already submitted” state cannot satisfy a new reviewer round.

### Slice C: Deterministic gate retry

Acceptance criteria:

- If gate verification is green, runner advances without launching a dev agent.
- If gate verification is red, dev agent receives only the failing output and relevant context.
- A green gate cannot end as `dev_failed` only because no LLM finalization call occurred.

### Slice D: Formal dev-loop state machine

Acceptance criteria:

- Runner states and transitions are explicit in code.
- Each state has machine-checkable entry and exit criteria.
- Missing required tool calls produce structured state errors.
- Tests cover stale reviewer verdict, missing `gm_dev_done`, missing `gm_review_submit`, green gate retry, and red gate retry.

### Slice E: Retry prompt compaction

Acceptance criteria:

- Retry prompts include only actionable current-round context.
- Prompt sizes are measured in telemetry by role/round.
- Large prior transcripts are not embedded in retries.
- Cache-read/cache-write metrics are included in run summaries where available.

### Slice F: Targeted verification helper

Acceptance criteria:

- Runner can suggest or run changed-package checks before full verification.
- Full verification remains mandatory at merge/PR gates.
- `gm_project_check` failure output includes structured package/lint failure hints where possible.

## Open Questions

- Should reviewer sessions always be fresh, or should they be resumable but scoped by PR head SHA and review round?
- Should `gm_review_submit` reject submissions for stale PR head SHAs?
- Should gate retry be fully non-LLM, or should there be an escape hatch for ambiguous failures?
- Should activity logs be retained indefinitely, size-capped, or compressed after run completion?
- Should Golemic expose a run-level summary with per-round token/cost/tool counts for easier Pareto analysis?
