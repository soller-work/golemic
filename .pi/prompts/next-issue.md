---
description: Implement the next claimable Golemic issue via the local planner/dev/reviewer workflow
argument-hint: "[extra instructions]"
---

Implement the next claimable issue using the project workflow in `docs/implementation/subagent-loop.md`.

Extra instructions, if any:
$ARGUMENTS

Requirements:

- Read `docs/implementation/subagent-loop.md` first and follow it exactly.
- Use `claim_next_issue` as the only allowed mechanism to select, validate, claim, push, and prepare the issue worktree.
- `claim_next_issue` and `complete_claimed_issue` serialize shared main-checkout Git operations with a repo workflow lock; if either tool reports the lock is held, stop and report the blocker.
- If `claim_next_issue` is unavailable, returns `blocked`, or does not return an issue ID, branch name, and worktree path, stop and report the tool blocker. Do not fall back to manual shell-based selection, claiming, pushing, or worktree creation.
- After a successful claim, read the returned issue document completely.
- Run all post-claim planning, reviewer planning, clarification handling, implementation, review, checks, completion updates, and issue commit from the returned issue worktree; do not implement directly in the main checkout.
- Create and use issue-worktree-relative `.tmp/` for temporary workflow artifacts.
- Maintain issue-worktree-relative `.tmp/session-registry.json` and enforce hard per-issue subagent session reuse.
- Invoke planner, reviewer, dev, PO, and architect subagents after worktree creation with the returned worktree path as `cwd`.
- If the local orchestrator tooling cannot return, store, or reuse required subagent session IDs, stop with an infrastructure blocker.
- Use `planner` as the active planner.
- Use `reviewer` as the active reviewer.
- From the issue worktree, start planner and reviewer planning in parallel:
  - planner creates `.tmp/implementation-plan.md`;
  - reviewer creates an independent `.tmp/review-plan.md`.
- Reinvoke the planner in the same planner session to integrate the review plan and any PO/Architect answers.
- Route planner product/scope/acceptance questions to `po` and save answers to `po-answers.md` when needed.
- Route planner technical/design/boundary questions to `architect` and save answers to `architect-answers.md` when needed.
- Do not start dev unless `implementation-plan.md` has `Status: ready-for-dev`.
- If `implementation-plan.md` has `Status: blocked`, stop and report the blockers.
- After `ready-for-dev`, the planner is done; do not invoke the planner again during normal dev/reviewer ping-pong.
- Use `dev` as the developer.
- The dev must treat `implementation-plan.md` as the primary implementation input and document any material deviation.
- The reviewer who created `review-plan.md` must review the implementation in the same reviewer session and explicitly consider that review plan.
- Only one planner, one developer, and one reviewer are active at a time after the parallel planning phase.
- Loop dev → reviewer until the active reviewer approves or a real blocker requires human decision.
- After approval, call `complete_claimed_issue` from the main checkout with the claimed issue ID to run checks, complete backlog/docs, commit the issue branch, update/rebase onto `main`, rerun checks, fast-forward merge, push, and cleanup.
- If `complete_claimed_issue` is unavailable or returns `blocked`, stop and report the tool blocker; do not fall back to ad-hoc manual cleanup unless explicitly asked.
- If resolving a completion blocker requires material code changes after reviewer approval, return to the same reviewer session for re-approval before rerunning `complete_claimed_issue`.
- Do not ask for clarification unless there is a real blocker.
