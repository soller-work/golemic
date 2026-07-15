---
name: dev-loop
description: Manual dev+review loop for golemic backlog items. Use when the user says "start dev loop", asks to work on a backlog item (I0.x, I1.x, ...), or wants the dev/reviewer subagents driven through implement→review→iterate until approved.
---

# Golemic Dev Loop (manual)

## Purpose

Interim substitute for the Iteration-1 runner. Drives one backlog item from
`docs/backlog.md` through implement → review → iterate until the reviewer
approves. Once golemic self-hosts (post-I1.11), this skill becomes redundant.

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
1. **Backlog ID + one-line goal** (e.g. `I0.4 — golemic preflight: checks + scaffolding`).
2. **Spec pointers**: exact §§ in `docs/architecture.md` and the section in `docs/backlog.md`. Instruct the agent to read them before starting.
3. **What already exists**: previously completed items the new work builds on, with file paths.
4. **Scope**: numbered list, verbatim from the backlog item, augmented with any concrete decisions already agreed with the maintainer (e.g. "use `embed.FS` for templates").
5. **Definition of Done**: `go build ./... && go vet ./... && go test -count=1 ./...` green; external commands only behind injectable interfaces (§2.12); no overwriting of human-edited files; whatever else the backlog item names.
6. **Out of scope**: verbatim from the backlog item.
7. **Report format**: what changed, how verified, remaining risks.

Save the returned `sessionId` — you will need it for round 2.

### Between rounds — brief the reviewer

Task must contain:
1. **Backlog ID + spec pointers** (same as dev brief).
2. **File list**: new and modified files (from `git status`).
3. **Review focus**: acceptance-criteria checklist from the backlog item, plus explicit edge cases and any claims from the dev's report that deserve scrutiny (e.g. "the claimed bugfix in X — verify it is not scope creep").
4. **Verdict contract**: severity-tagged findings (`blocker`/`major`/`minor`/`nit` or `P1`–`P4`) with `file:line` refs and concrete fix suggestions. Final line must be `VERDICT: APPROVED` or `VERDICT: CHANGES_REQUESTED`.

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
- `git status` — check for stray files (manual test runs, `.golemic/` created during preflight testing, etc.). Remove or `.gitignore` them.
- Do NOT commit without maintainer approval, and do NOT push without explicit maintainer approval (per global orchestrator rules — pushes are shared-state actions).

## Report back to the maintainer

- Round count and final verdict.
- One-line summary per round of what shifted.
- Files new/modified/removed.
- Verification command output (grep for `ok` / failures).
- Non-blocking reviewer notes worth remembering for future iterations.
- Ask about commit granularity before committing.
