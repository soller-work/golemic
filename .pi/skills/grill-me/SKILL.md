---
name: grill-me
description: Stress-test a software plan through a one-question-at-a-time 7-step interview to produce a validated, autonomously implementable vertical slice. Support command, query, process, and integration slices. Use when the user asks to be grilled, wants a plan challenged, or needs an implementation-ready use case converted into a GitHub issue. Inspect the codebase instead of asking for facts.
---

# Grill Me

## Goal

Interview one software plan until product and implementation decisions are complete. Classify it as `command`, `query`, `process`, or `integration`. Validate the result against `schema.json` and create a GitHub issue with a fixed Markdown body layout. The issue body is the single source of truth for all downstream consumers.

## Resources

- `schema.json` — the canonical output contract (slim, no cross-refs, no session state).
- `references/slice-types.md` — use when classifying or resolving type-specific requirements.
- Scripts: `slice.py` (new, write, check, finalize), `create_issue.py` (renders body, archives JSON), `validate_slice.py` (semantic checks), `gh_issue_index.py` (fetches open issues for similarity scan).

## 7-Step Workflow

### 1. Subject & Classification
Identify the single stakeholder-visible outcome. Classify using `references/slice-types.md`: state mutation (command), retrieval (query), progression (process), or external contract (integration). If multi-slice: propose smallest vertical slice, put others in scope.out. Inspect the codebase for modules, routes, conventions, and test commands. Do not ask the user for facts the code can verify.

### 2. Similarity Check
Fetch open issues via `gh_issue_index.py --with-body`. If a close match appears, ask the user: "Related to issue #N?" Let them decide. Continue or pivot based on their answer.

### 3. Scope Cut
Agree on what goes in, what stays out, and what depends on what. Multi-slice: propose a per-slice split with shared discovery and auto-blocked-by from conversation context.

### 4. Intent
Resolve stakeholder, trigger, success_outcome, and tldr (max 140 chars). One turn per question. Format:

> **Question N — <Topic>**
> <Decision required>
> **Recommendation:** <one proposed answer + consequence>
> **Your answer?**

### 5. Behavior & Rules
Define type-specific behavior (state mutations, read model, ordered steps, or external contract as Markdown). Add business_rules, failure paths, I/O contract, codebase evidence (path:line), and verify commands in the same turn or next.

### 6. Acceptance & Done
Resolve acceptance_scenarios (free-form Given/When/Then strings), definition_of_done (checklist items), and security (only if security_relevant=true). Ask once; get everything in one turn if possible.

### 7. Create Issue
Run: `python3 .pi/skills/grill-me/scripts/create_issue.py <slice.json> [--blocked-by N[,N...]]`

The script renders Markdown (TL;DR header, fixed section order, conditional Security), validates atomically, creates the issue, and archives the JSON to `.pi/skills/grill-me/.tmp/archive/<timestamp>_<issue-nr>.json`. Print the issue URL. The GitHub issue is now the artifact; delete or ignore the local slice.json.

## Readiness Gate

Finalize sets `readiness: ready` automatically if:
- `blockers` array is empty (or contains only resolved items).
- Validation passes.
- Use `slice.py finalize <path> --blocked` to force blocked status.

Never use `ready` just because the user wants to stop. If evidence is unavailable, set `blocked` and describe what is missing.
