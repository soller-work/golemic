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
- Run `scripts/validate_slice.py` before presenting the final artifact.

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
3. actors, authorization, and ownership;
4. preconditions and relevant existing state;
5. happy-path behavior;
6. alternate, error, retry, and cancellation behavior;
7. business rules and decision tables;
8. **risk classification**: propose `low`, `medium`, or `high` with rationale using the DT-001 guidance below; record the confirmed value in the decision log with source `user` or `confirmed_recommendation`;
9. inputs, outputs, validation, and error contracts;
10. type-specific contract: state mutation, read model, ordered process, or external integration;
11. state changes, events, and external side effects where relevant;
12. idempotency, concurrency, freshness, consistency, retry, timeout, compensation, and failure handling where relevant;
13. codebase integration points and change boundaries;
14. acceptance scenarios, required tests, and quality commands.

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

Create exactly one JSON document as a local temporary file (e.g. `slice.json`). Follow `schema.json` exactly. Use JSON values, not Markdown, Gherkin text blocks, comments, or prose outside defined fields. There is no `slice_id` field and no local backlog directory — the GitHub issue number assigned at creation time is the only identifier.

Requirements:

- Keep identifiers stable and unique.
- `depends_on` is informational prose only (e.g. `["Requires issue #5 to be closed first"]`). Hard dependencies are expressed as `--blocked-by N` arguments to `create_issue.py` (step 8) and become native GitHub blocked_by relations — not a field in the JSON.
- Use `BR-*` for business rules, `DT-*` for decision tables, `IF-*` for interfaces, `RM-*` for read models, `PS-*` for process steps, `IC-*` for integration contracts, `SC-*` for state changes, `SE-*` for side effects, `AC-*` for acceptance scenarios, `EV-*` for codebase evidence, and `D-*` for decisions.
- Trace acceptance scenarios to every relevant rule, interface, read model, process step, integration contract, state change, and side effect.
- Use empty arrays instead of omitting required collections.
- Do not use placeholders such as `TBD`, `TODO`, `unknown`, `later`, or equivalent unresolved language in a `ready` artifact.
- Do not include implementation choices that conflict with verified architecture constraints.

### 6. Validate and repair

Run from the skill directory or use equivalent absolute paths:

```bash
python scripts/validate_slice.py schema.json <slice.json>
```

If validation fails, repair the JSON and rerun the validator. Do not present an invalid artifact as complete.

The validator performs:

- JSON Schema Draft 2020-12 validation;
- identifier uniqueness checks;
- trace-reference integrity checks;
- readiness invariants;
- unresolved-placeholder detection;
- codebase evidence checks;
- type-specific command, query, process, and integration invariants.

### 7. Autonomous Agent Guardrails: Mandatory Tools and Workflow

**For autonomous agents (including AI): You must follow this workflow to avoid repeated mistakes and wasted tokens.**

#### Phase 1: Schema Understanding (BEFORE writing JSON)

Do not guess field names, types, or enum values. Query the schema:

```bash
# Understand enum values for any field
python3 scripts/schema-query.py schema.json "interfaces[].kind"
python3 scripts/schema-query.py schema.json "slice_type"

# Understand complex types
python3 scripts/schema-query.py schema.json "business_rules[]"
python3 scripts/schema-query.py schema.json "decision_tables[].rows[]"
```

Read `references/slice-types.md` completely to understand your slice type's specific requirements.

#### Phase 2: Generate Skeleton (Mandatory)

Never write JSON from scratch. Use the scaffold tool:

```bash
python3 scripts/schema-scaffold.py schema.json --slice-type command --output slice.json
```

This creates valid-but-incomplete JSON with all required fields, correct types, and placeholder values (FILL_IN). This prevents structure errors before they happen.

#### Phase 3: Fill In & Validate Incrementally

Do not write 600 lines of JSON and then validate. Instead, fill one section, then validate:

```bash
# Fill stakeholder_intent section, then validate it
python3 scripts/validate-slice-partial.py slice.json --stage stakeholder_intent

# Fill scope + preconditions, then validate
python3 scripts/validate-slice-partial.py slice.json --stage preconditions

# Fill business_rules, then validate
python3 scripts/validate-slice-partial.py slice.json --stage business_rules

# Fill all critical sections (~50% done), then validate
python3 scripts/validate-slice-partial.py slice.json --stage 50_percent

# Fill remaining sections
python3 scripts/validate-slice-partial.py slice.json --stage full_draft
```

Sequence for all stages (use in order):
1. `stakeholder_intent` — Are intent fields filled?
2. `scope` — Intent + scope?
3. `preconditions` — Intent + scope + preconditions?
4. `business_rules` — Intent + scope + business rules?
5. `50_percent` — Critical fields only (faster checkpoint)
6. `full_draft` — All major sections have content?
7. `100_percent` — Full schema validation (strictest)

#### Phase 4: Final Validation

Only when partial stages pass, run full validation:

```bash
python3 scripts/validate-slice-partial.py slice.json --stage 100_percent
```

Must pass with zero errors.

#### Why This Matters

Agents that skip these steps waste tokens:
- Guessing field names → 30+ validation errors
- Writing JSON from scratch → structural errors
- Validating at the end → huge iteration loops

Using the tools enforces correctness **before** expensive JSON manipulation. Token cost: ~40% reduction. Quality: 100% validation pass rate on first full validation.

### 8. Create the GitHub issue

Once validation passes, create the issue from the host repository root:

```bash
python3 .pi/skills/grill-me/scripts/create_issue.py slice.json [--blocked-by N[,N...]]
```

- `--blocked-by` accepts a comma-separated list of existing GitHub issue numbers that this issue is blocked by. These become native GitHub blocked_by relations. Omit the flag when there are no hard dependencies.
- The script re-validates the slice, deterministically renders the Markdown body, creates the issue, sets blocked_by relations, and attaches the `ready-for-agent` label as the final write step (so a partially created issue is never visible as takeable).
- On success it prints the new issue number and URL. The local `slice.json` is now a throwaway file; the GitHub issue is the sole artifact.
- Use `--dry-run` to preview the rendered body and planned `gh` commands without executing any write.

Do not edit production code. Do not silently convert the result into loose issues.
