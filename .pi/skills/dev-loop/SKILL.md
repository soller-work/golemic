---
name: dev-loop
description: Manual local backup for the golemic runner. Use when golemic is down and one GitHub issue must be implemented by hand into a merge-ready PR. The user names the issue by URL or number. Drives the dev/reviewer subagents through implement → review → iterate until approved, then opens the PR.
---

# Golemic Dev Loop (manual, local backup)

## Purpose

Backup for when the golemic runner is not running and one issue must be
implemented by hand. Reproduces golemic's dev → reviewer ping-pong locally and
hands back a **merge-ready PR**. One issue, one session — not built for parallel
runs, and it does not merge.

## Input

The user names the issue by URL or number. Extract the issue number `N`
(from a URL like `https://github.com/<owner>/<repo>/issues/N`, take the trailing
integer). If no issue is given, ask for it.

## The spec is authoritative

Fetch the task specification with:

```bash
golemic slice --issue N
```

Its output (structured JSON slice or raw issue body) is the source of truth —
scope, business rules, interfaces, acceptance scenarios, quality gates. Read it
in full before briefing the dev. **Do not** rely on any summary in the issue's
web UI. If `golemic slice` fails, fall back to `gh issue view N`.

## Language

All artifacts produced during the loop MUST be English — code, tests, docs,
commit messages, PR title/body, task briefings, review findings. See
`docs/conventions.md`. This applies even when the maintainer converses in German.

## Agents

The **single source of truth** for each agent's persona body and model chain is
`.golemic/agents/{role}.md` (e.g. `.golemic/agents/dev.md`,
`.golemic/agents/reviewer.md`). The `.pi/agents/` entries are symlinks into that
directory — do not edit them directly, and do not treat them as authoritative.
The model used for each subagent comes from the `model:` field in the agent's
frontmatter in `.golemic/agents/{role}.md`; there is no separate model config.

Invoke with `agentScope: "both"` and `confirmProjectAgents: false`.

Roles:
- `dev` — implements the change.
- `reviewer` — reviews the diff, returns a verdict.

## Isolated worktree (mandatory)

Other local agents may be editing files in the main checkout at the same time —
never run the loop there, or your work and theirs will clobber each other. Before
briefing the dev, create a dedicated worktree on golemic's branch convention
(`golemic/issue-N`, so the PR stays consistent with the runner) off an
up-to-date main:

```bash
git -C <main-repo> fetch -q origin main
git -C <main-repo> worktree add /tmp/golemic-issue-N -b golemic/issue-N origin/main
```

Every step below — dev, reviewer, `git`, verification, PR — runs **inside**
`/tmp/golemic-issue-N`. Pass that path as the `cwd` for the dev and reviewer
subagents so they read and edit only the isolated tree.

## Loop protocol

Hard cap: **3 review rounds**. If not approved by round 3, stop and escalate to
the maintainer with the reviewer's outstanding findings — do not open a PR.

### Round 1 — brief the dev

Task must contain, in this order:
1. **Issue number + one-line goal**, taken from the slice `title`.
2. **Spec pointer**: instruct the dev to run `golemic slice --issue N` and treat
   that output as the source of truth, plus the architecture §§ it references.
3. **Project guidelines**: include the full contents of `.golemic/guidelines/dev.md`
   verbatim under a "Project Guidelines" heading.
4. **What already exists**: relevant existing files the work builds on.
5. **Scope**: from the slice — scope, business rules, interfaces/process steps,
   state changes.
6. **AC→test mapping**: every acceptance scenario must be covered by at least one
   named automated test. The dev's report must include the mapping (AC → test
   function).
7. **Definition of Done**: the slice's quality gates; at minimum
   `go build ./... && go vet ./... && go test -count=1 ./...` green; external
   commands (`gh`, `git`, `pi`) only behind injectable interfaces (§2.12).
8. **Out of scope**: verbatim from the slice.
9. **Commit, don't push yet**: commit on `golemic/issue-N` with a meaningful
   message following Conventional Commits with the slice number
   (e.g. `fix(runner): … (117)`). Do **not** push or open a PR — that happens
   after the reviewer approves.
10. **Report format**: what changed, how verified, AC→test mapping, remaining
    risks.

Save the returned `sessionId` — you need it for round 2.

### Between rounds — brief the reviewer

Task must contain:
1. **Issue number + spec pointer** (reviewer also runs `golemic slice --issue N`).
2. **Project guidelines**: include the full contents of `.golemic/guidelines/reviewer.md`
   verbatim under a "Project Guidelines" heading.
3. **File list**: new and modified files (from `git status` / `git diff`).
4. **Review focus**: the slice's acceptance scenarios as a checklist, plus edge
   cases and any claims from the dev's report that deserve scrutiny.
5. **AC coverage check (blocker-level)**: verify the dev's AC→test mapping —
   every acceptance scenario must trace to a real, meaningful automated test. A
   missing or hollow test is a P1/P2 finding.
6. **Verdict contract**: severity-tagged findings (`P1`–`P4`) with `file:line`
   refs and concrete fixes. The **final line** of the report must be exactly one
   of:
   ```
   VERDICT: APPROVED
   ```
   or
   ```
   VERDICT: CHANGES_REQUESTED
   ```
   This is how the orchestrator reads the verdict in the manual flow. Do **not**
   use `golemic submit-review` — that is the runner path and does not apply here.

Reviewer sessions can be reused across rounds — save the `sessionId`.

### Round 2+ — brief the dev again (same session)

Reuse the dev's `sessionId`. Task must:
1. State the verdict up front (`changes_requested`).
2. List **each P1/P2** with the reviewer's finding, file:line, and the fix
   approach. If the reviewer proposed multiple fixes, pick one and say which.
3. List **each P3** to fix.
4. Enumerate **P4 items to ignore** so the dev doesn't rathole.
5. Restate the DoD; still no push.

Then re-run the reviewer in the same session with a short delta brief:
"Dev addressed P2-1 by X, P2-2 by Y, all P3 as promised. Verify."

## Handling reviewer findings

- **P1/P2**: always fix.
- **P3**: fix unless it introduces scope creep; call that out if declining.
- **P4**: default ignore. Promote to P3 only if it hints at real risk (leftover
  artifacts, hardcoded paths that could collide).
- **Reviewer vs maintainer decisions**: the maintainer's decision wins. Note it
  in the dev brief so the reviewer doesn't re-raise it.

## After approval — open the PR

All commands run inside the worktree (`/tmp/golemic-issue-N`). Only once the
reviewer returns `VERDICT: APPROVED`:
1. `git status` — check for stray files (manual test runs, `.golemic/` created
   during testing). Remove or `.gitignore` them.
2. Confirm with the maintainer before pushing (push is a shared-state action).
3. Push and open the PR:

```bash
git push -u origin golemic/issue-N
gh pr create --title "..." --body "..."
```

The PR body **must** include a closing keyword so the merge auto-closes the
issue, e.g. `Closes #N`. Do **not** merge — the maintainer merges.

## Tear down the worktree

After the PR is open (or on abandonment/escalation), remove the worktree so it
doesn't accumulate. The branch and its pushed commits survive:

```bash
git -C <main-repo> worktree remove /tmp/golemic-issue-N
```

## Report back to the maintainer

Keep the final report short — 3–5 lines:
- Round count and final verdict.
- One-line summary of what was built.
- Files changed (new/modified/removed), one line.
- The PR URL.
- Non-blocking P3/P4 notes, if any (one line each).
