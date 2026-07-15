---
name: dev-loop
description: Manual dev+review loop for golemic backlog issues. Use when the user says "start dev loop", asks to work on a backlog issue (docs/backlog/*.json, slice IDs like I1.x), or wants the dev/reviewer subagents driven through implement→review→iterate until approved.
---

# Golemic Dev Loop (manual)

## Purpose

Interim substitute for the Iteration-1 runner. Drives one backlog issue from
`docs/backlog/` through implement → review → iterate until the reviewer
approves. Once golemic self-hosts (post-I1.11), this skill becomes redundant.

## Selecting the issue

Open issues are implementation-slice JSON files (grill-me schema) at
`docs/backlog/NNN_<slug>.json`. The numeric prefix is the processing order.

Unless the user names a specific issue, pick the next one:

```bash
python3 .pi/skills/dev-loop/scripts/next_issue.py
```

It prints the file path, `slice_id`, `title`, and `readiness`. If `readiness`
is not `ready`, stop and escalate to the maintainer instead of starting the
loop. Read the full JSON before briefing the dev — it is the authoritative
spec (scope, business rules, interfaces, acceptance scenarios, quality gates).

## Language

All artifacts produced during the loop MUST be English — code, tests, docs,
commit messages, task briefings, review findings. See `docs/conventions.md`.
This applies even when the maintainer converses in German.

## Agents

Both project-local under `.pi/agents/`:
- `dev` — implements the item.
- `reviewer` — reviews the diff, returns a verdict.

Always invoke with `agentScope: "both"` and `confirmProjectAgents: false`.

## Loop protocol

Hard cap: **3 review rounds**. If not approved by round 3, stop and escalate to
the maintainer with the reviewer's outstanding findings.

### Round 1 — brief the dev

Task must contain, in this order:
1. **Slice ID + one-line goal** (e.g. `I1.1 — event log: JSONL writer/reader + env-var contract`), taken from `slice_id` and `title`.
2. **Spec pointers**: the issue file path (`docs/backlog/NNN_<slug>.json`) and the architecture §§ it references (see `implementation_context` / `decision_log`). Instruct the agent to read the full JSON and those §§ before starting.
3. **What already exists**: previously completed items the new work builds on, with file paths (cross-check `codebase_evidence` in the JSON).
4. **Scope**: from the JSON — `scope`, `business_rules`, `interfaces`/`process_steps`/`integration_contracts`, `state_changes`, augmented with any concrete decisions already agreed with the maintainer.
5. **AC→test mapping**: every `AC-*` in `acceptance_scenarios` must be covered by at least one named automated test. The dev's report must include the mapping (AC ID → test function).
6. **Definition of Done**: everything in `quality.definition_of_done` and `quality.quality_commands`; at minimum `go build ./... && go vet ./... && go test -count=1 ./...` green; external commands only behind injectable interfaces (§2.12); no overwriting of human-edited files.
7. **Out of scope**: verbatim from `scope.out_of_scope`.
8. **Report format**: what changed, how verified, AC→test mapping, remaining risks.

Save the returned `sessionId` — you will need it for round 2.

### Between rounds — brief the reviewer

Task must contain:
1. **Slice ID + spec pointers** (same as dev brief, including the issue JSON path).
2. **File list**: new and modified files (from `git status`).
3. **Review focus**: the `acceptance_scenarios` from the issue JSON as a checklist, plus explicit edge cases and any claims from the dev's report that deserve scrutiny (e.g. "the claimed bugfix in X — verify it is not scope creep").
4. **AC coverage check (blocker-level)**: verify the dev's AC→test mapping — every `AC-*` must trace to a real, meaningful automated test. A missing or hollow test for any AC is a P1/P2 finding.
5. **Verdict contract**: severity-tagged findings (`blocker`/`major`/`minor`/`nit` or `P1`–`P4`) with `file:line` refs and concrete fix suggestions. Final line must be `VERDICT: APPROVED` or `VERDICT: CHANGES_REQUESTED`.

Reviewer sessions can be reused across rounds — save the `sessionId`.

### Round 2+ — brief the dev again (same session)

Reuse the dev's `sessionId`. Task must:
1. State the verdict up front (`changes_requested`).
2. List **each blocker (P1/P2)** with the reviewer's finding, file:line, and the agreed fix approach. If the reviewer proposed multiple fixes, pick one and state which.
3. List **each P3** to fix, same structure.
4. Explicitly enumerate **P4 items to ignore** (wording, style, scope-creep suggestions) so the dev doesn't rathole.
5. Restate the DoD.

Then re-run the reviewer in the same session with a short delta brief:
"Dev has addressed P2-1 by X, P2-2 by Y, all P3 as promised. Verify."

## Handling reviewer findings

- **P1/P2 (blocker/major)**: always fix.
- **P3 (minor)**: fix unless it introduces scope creep; call that out explicitly to the dev if declining.
- **P4 (nit)**: default ignore. If a P4 hints at real risk (e.g. leftover artifacts in the repo, hardcoded paths in tests that could collide), promote it to P3 in the dev brief.
- **Reviewer disagreement with maintainer decisions**: the maintainer's decision wins. Note it in the dev brief so the reviewer doesn't re-raise next round.

## Handling artifacts

After approval, before reporting done:
- **Delete the issue file** (`docs/backlog/NNN_<slug>.json`). The deletion goes
  into the **same commit** as the implementation — issue implemented = issue
  gone, no in-between state.
- `git status` — check for stray files (manual test runs, `.golemic/` created during preflight testing, etc.). Remove or `.gitignore` them.
- Do NOT commit without maintainer approval, and do NOT push without explicit maintainer approval (per global orchestrator rules — pushes are shared-state actions).

## Report back to the maintainer

- Round count and final verdict.
- One-line summary per round of what shifted.
- Files new/modified/removed.
- Verification command output (grep for `ok` / failures).
- Non-blocking reviewer notes worth remembering for future iterations.
- Ask about commit granularity before committing.
