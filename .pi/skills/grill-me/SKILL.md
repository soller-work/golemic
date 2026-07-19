---
name: grill-me
description: Stress-test a software plan, feature, workflow, or design through a one-question-at-a-time interview and turn the resolved decisions into one validated, autonomously implementable vertical slice. Support command, query, process, and integration slices. Use when the user asks to be grilled, wants a plan challenged before implementation, wants stakeholder requirements converted into an implementation-ready use case, or needs a standardized implementation-slice.json that validates against schema.json. Inspect an available codebase instead of asking questions that the code can answer.
---

# Grill Me

## Goal

Interrogate one software plan or feature until its product and implementation decisions are complete enough for an autonomous coding agent. Classify the slice as `command`, `query`, `process`, or `integration`. Produce exactly one machine-readable `implementation-slice.json` and validate it against the bundled `schema.json` plus semantic checks in `scripts/validate_slice.py`.

Do not implement production code. The validated JSON file is the handoff artifact.

## Required resources

- Use `schema.json` as the canonical output contract.
- Read `references/slice-types.md` when classifying the primary slice type or resolving type-specific requirements.
- Use `references/example-slice.json` only as a command-slice structural example. Never copy its domain content.
- Use `references/example-query-slice.json`, `references/example-process-slice.json`, and `references/example-integration-slice.json` as structural examples for those respective slice types. Never copy their domain content.
- Run `scripts/validate_slice.py` before presenting the final artifact (via `slice.py finalize`).

## Workflow

### 1. Establish the subject

Identify the single stakeholder-visible capability being modeled. Classify it by its primary stakeholder-visible outcome using `references/slice-types.md`: state mutation (`command`), information retrieval (`query`), ordered progression (`process`), or external boundary behavior (`integration`). If the request contains multiple independently deployable outcomes, choose the smallest coherent vertical slice and put the others in `scope.out_of_scope`.

Inspect an available repository before interviewing. Discover and record:

- relevant modules, files, symbols, routes, schemas, and tests;
- existing architectural and infrastructure conventions;
- reusable interfaces and forbidden dependency directions;
- test, lint, build, migration, and validation commands.

Do not ask the user for facts that can be verified in the codebase. Do not invent file paths, symbols, commands, or conventions.

### 2. Start the interview

State a rough adaptive question range based on the apparent scope, for example:

`Estimated interview size: about 8-12 questions. This may change as dependencies or risks appear.`

Ask exactly one question per turn. Use this header:

`Question <current> of about <low>-<high> - <decision topic>`

For every question include:

1. the decision that must be made;
2. a concise explanation of why it affects implementation;
3. one recommended answer;
4. the main consequence of accepting that recommendation.

Wait for the user's answer before continuing. Never bundle independent decisions into one question.

If the estimate changes, state the new range and one concrete reason before the next question.

### 3. Resolve the decision tree

Walk dependencies in this order unless the feature requires a different dependency order:

1. stakeholder, goal, value, and trigger;
2. issue dependencies: which existing GitHub issues (by number) must be resolved before this one is takeable — these become `--blocked-by` arguments to `create_issue.py` and are recorded as native GitHub blocked_by relations;
3. in-scope outcome and explicit non-goals;
4. actors, authorization, and ownership;
5. preconditions and relevant existing state;
6. happy-path behavior;
7. alternate, error, retry, and cancellation behavior;
8. business rules and decision tables;
9. **risk classification**: propose `low`, `medium`, or `high` with rationale using the DT-001 guidance below; record the confirmed value in the decision log with source `user` or `confirmed_recommendation`;
10. inputs, outputs, validation, and error contracts;
11. type-specific contract: state mutation, read model, ordered process, or external integration;
12. state changes, events, and external side effects where relevant;
13. idempotency, concurrency, freshness, consistency, retry, timeout, compensation, and failure handling where relevant;
14. codebase integration points and change boundaries;
15. acceptance scenarios, required tests, and quality commands.

#### DT-001 — Risk value guidance

| Change characteristics | Recommended risk | Downstream effect |
|---|---|---|
| Small, local, well-covered by tests, no critical paths (auth, migrations, CI config, release tooling) | `low` | Eligible for auto-merge |
| Moderate scope or touches shared components, still well-testable | `medium` | Eligible for auto-merge |
| Architectural change, critical path, migration, security-relevant, or hard to verify | `high` | Always requires human merge |

When uncertain, recommend the higher value. The user confirms or overrides; their confirmed value is what lands in the slice.

Normalize every resolved answer into the draft decision log. Use only these decision sources:

- `user`: explicitly decided by the user;
- `codebase`: verified from repository evidence;
- `confirmed_recommendation`: recommended by the interviewer and explicitly accepted by the user.

A recommendation is not a decision until the user accepts it. Record contradictions and resolve them before continuing.

### 4. Apply the readiness gate

Set `readiness` to `ready` only when all of the following are true:

- the artifact describes exactly one vertical slice;
- stakeholder intent, trigger, success outcome, and scope boundaries are explicit;
- the `risk` field is set to `low`, `medium`, or `high` with a decision log entry recording the rationale;
- all material behavior branches and business rules are resolved;
- inputs, outputs, errors, permissions, and applicable state transitions are specified;
- the selected `slice_type` satisfies its type-specific contract: commands define mutations, queries define complete read models without domain state changes, processes define ordered steps and terminal behavior, and integrations define reliability and compatibility contracts;
- side effects and failure behavior are specified where relevant;
- implementation locations and architecture constraints are verified in the codebase;
- acceptance scenarios cover success, authorization, validation, and material failure paths;
- test levels, quality commands, and definition of done are explicit;
- `open_questions`, `assumptions_requiring_confirmation`, and `blockers` are empty;
- every codebase evidence entry has `verified: true`;
- structural and semantic validation passes.

Do not use `ready` merely because the user wants to stop. If required information is unavailable or the environment cannot be inspected, set `readiness` to `blocked` and describe the missing evidence precisely.

### 5. Produce the artifact

Run from the skill directory or use equivalent absolute paths:

```bash
slice.py init <type>                                          # writes skeleton to slice.json
slice.py plan slice.json                                      # read once; shows fill order and sub-schemas
slice.py set slice.json <section> '<json_fragment>'           # repeat per section
slice.py finalize slice.json                                  # normalizes and delegates to validate_slice.py
```

The driver assigns IDs (`BR-001`, `IF-001`, etc.) deterministically — do not include `id` fields in fragments. It validates each fragment locally against the section's sub-schema and checks cross-references (`traces_to`, `rule_ids`, `evidence_ids`) before writing. `finalize` enforces readiness conditionals and rejects incomplete or inconsistent slices.

Agent responsibilities: provide semantic content per section. No ID bookkeeping, no full-document rewrites.

Other conventions:
- `depends_on` is informational prose only (e.g. `["Requires issue #5 to be closed first"]`). Hard dependencies are expressed as `--blocked-by N` arguments to `create_issue.py` and become native GitHub blocked_by relations.
- Use empty arrays instead of omitting required collections.
- Do not use placeholders such as `TBD`, `TODO`, `unknown`, `later`, or equivalent unresolved language in a `ready` artifact.

### 6. Create the GitHub issue

Once validation passes, create the issue from the host repository root:

```bash
python3 .pi/skills/grill-me/scripts/create_issue.py slice.json [--blocked-by N[,N...]]
```

- `--blocked-by` accepts a comma-separated list of existing GitHub issue numbers that this issue is blocked by. These become native GitHub blocked_by relations. Omit the flag when there are no hard dependencies.
- The script re-validates the slice, deterministically renders the Markdown body, creates the issue, sets blocked_by relations, and attaches the `ready-for-agent` label as the final write step.
- On success it prints the new issue number and URL. The local `slice.json` is now a throwaway file; the GitHub issue is the sole artifact.
- Use `--dry-run` to preview the rendered body and planned `gh` commands without executing any write.

Do not edit production code. Do not silently convert the result into loose issues.
