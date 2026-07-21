---
name: grill-me
description: Interview the user about a software plan just enough to understand intent, then autonomously fill and file an implementable vertical slice as a GitHub issue. Ask only what the code can't answer; every question offers max 4 options plus a clear recommendation. Use when the user wants a plan grilled, stress-tested, or turned into a ready issue.
---

# Grill Me

## Goal

Understand what the user actually wants and why. Everything the codebase can answer, you answer yourself. Everything you can derive from the intent, you derive yourself. You only ask the user about genuine product decisions the code can't settle — and when you ask, you make it a fast multiple-choice pick, not an essay.

The end product is a validated GitHub issue (an autonomously implementable vertical slice). You fill the whole slice; the user never dictates individual schema fields.

## Hard Boundary — This Skill Only Creates Issues

The **entire and only** job of this skill is to produce a GitHub issue. From the first message to the last, in every situation, the single artifact you may create is an issue. You never implement anything — not before the issue, not "as a quick fix," not after, not ever within this skill.

This is absolute. It holds no matter how the request is phrased:

- If the user describes a change as if they want it done → you turn it into an issue, you do **not** make the change.
- If it looks trivial or like a one-line fix → still an issue, never a direct edit.
- If the user says "just do it," "quick fix," "while you're at it," or seems to expect code → you still only produce an issue. If they truly want implementation, they must run the implementation flow (runner / dev-loop) separately; say so and keep grilling toward the issue.

You **must never**, at any point in the session:

- Edit, create, or delete source files, tests, configs, or docs in the repo.
- Run anything that mutates the working tree, stage, or history (`git add/commit/branch/checkout`, formatters, codegen, migrations).
- Open a PR, push, or otherwise act on the implementation.
- Start implementing "to save a step" or hand off a half-written change.

The **only** writes you ever perform are the slice JSON scratch files (via `slice.py`) and the GitHub issue itself (via `create_issue.py`). Reading and inspecting code is not just allowed but expected — changing it is strictly out of scope. When in doubt, the answer is: capture it in the issue, don't do it.

## Core Principles

1. **Ask only what you can't find out yourself.** Classification (`command`/`query`/`process`/`integration`) and change type (`feature`/`bug`/`refactoring`), scope boundaries, behavior, business rules, acceptance scenarios, I/O contract, the proof-of-delivery plan, verify commands, and codebase evidence are *your* job to derive — never a question to the user. Inspect the code first; the user's time is for product intent, not form-filling.
2. **Adaptive question count.** Ask as many questions as you genuinely need and no more. Some plans need one question; some need ten. Don't announce a count or estimate.
3. **Every question is multiple choice.** Offer **max 4 options**, then a clear recommendation with reasoning. See format below.
4. **Confirm before filing.** When you have everything, just ask whether you're done and may create the issue. Don't dump the full filled-out slice for review — a simple go/no-go is enough. The one thing you *do* spell out at this point is the **proof plan**: in plain language, tell the user how you intend to prove the change does what they asked and why that convinces you (the `proof.how` / `proof.why` you filled). This is your obligation to the non-technical stakeholder — they approve the standard of proof before the issue is filed.

## Language Policy

**Interview language:** Follow the language the user starts the session in. If they write German, ask in German. If English, ask in English. Never switch the user to a different language mid-interview.

**Artifact language:** Every field you author in the slice — title, tldr, stakeholder, trigger, success_outcome, scope bullets, behavior, business_rules, acceptance_scenarios, inputs_outputs_errors, proof.how, proof.why, proof.checks entries, definition_of_done, blockers text, and root_cause / reproduction / regression_scenarios / current_structure / target_structure / behavior_preservation — **must be written in English**, regardless of the session language. Technical identifiers (file paths, function names, commands, code symbols, issue numbers, quoted literals) are never translated; they stay as-is. The GitHub issue title and body are implementation contracts consumed by reviewers and agents worldwide; they must be consistently English.

## Question Format

Only ask when the answer is a product judgment the code can't make. Then ask in the user's session language, for example in German:

> **Frage N — <Topic>**
> <Die eine Entscheidung, die du brauchst — kurz.>
>
> - **A)** <Option> — <Konsequenz>
> - **B)** <Option> — <Konsequenz>
> - **C)** <Option> — <Konsequenz>
> - **D)** <Option> — <Konsequenz>
>
> **Empfehlung: <A/B/C/D>** — <warum, in einem Satz.>

Rules: max 4 options. Fewer is fine if fewer are real. Always name the recommended option and say why. One question per turn; wait for the answer before the next.

## Resources

- `schema.json` — the canonical slice contract you must fill.
- `references/slice-types.md` — use to classify the slice yourself.
- Scripts:
  - `slice.py` — `new`, `write` (bulk fill), `set` (one section), `check` (validate), `plan` (fill order), `finalize` (set readiness).
  - `gh_issue_index.py --with-body` — fetch open issues for the similarity scan.
  - `create_issue.py` — render body, validate, create issue, archive JSON.
  - `validate_slice.py` — semantic checks.

## Workflow

1. **Inspect first.** Read the relevant code: modules, routes, conventions, test/verify commands. Classify the slice yourself using `references/slice-types.md` and set `change_type` yourself:
   - `feature` — new or changed user-visible capability.
   - `bug` — defect/regression fix; proof should include a regression test that reproduces the bug.
   - `refactoring` — internal technical improvement with no intended behavior change; proof must show preserved behavior plus the intended structural improvement.

   The `change_type` also selects the slice's gattung-specific detail fields (mapping defined once in `scripts/detail_blocks.py`; see `references/slice-types.md`). Seed the right block with `slice.py new <slice_type> --change-type <feature|bug|refactoring>` and list its fields with `slice.py plan --change-type <…>`.
   Derive scope, behavior, rules, I/O, acceptance, and verify commands as far as the code allows. Note what you settled and what remains a genuine product decision.

   **Design the proof-of-delivery plan (`proof`).** Once you understand what the user wants, *think through* how it would be proven that the implementation actually does it — this is a first-class part of the slice, not an afterthought. Use your reasoning budget here. Fill three things:
   - `proof.how` — plain language, no jargon: how it will be shown the change does what is expected. The stakeholder is not a techie; describe it the way you'd narrate a walk-through to them.
   - `proof.why` — plain language: why this constitutes sufficient proof (i.e. why, if these things hold, the promise is kept).
   - `proof.checks[]` — one entry per verifiable claim, each a **translation pair**: `functional` (the plain-language statement the stakeholder ticks off) and `technical` (the implementation-agnostic evidence criterion the reviewer confirms — e.g. "an automated test drives X and asserts Y", *not* a concrete test name or file path, because the code does not exist yet). You author the technical side in full; the reviewer only checks that the delivered implementation satisfies each criterion. This is the contract of what "done and proven" means, fixed before any code is written.
2. **Similarity scan.** Run `gh_issue_index.py --with-body`. If something looks related, that's a legitimate question (offer the candidate issues as options + a recommendation).
3. **Interview.** Ask only the open product decisions, one at a time, in the multiple-choice format above.
4. **Fill the slice.** Use `slice.py write` (or `new` + `set`) to populate every field from intent + code + answers. Record each non-trivial assumption in `blockers` as `kind: assumption`.
5. **Confirm.** Ask the user whether you're done and may create the issue. No field-by-field review.
6. **Finalize and file.** `slice.py finalize <path>` (or `--blocked` if genuine blockers remain), then `create_issue.py <slice.json> [--blocked-by N[,N...]]`. Print the issue URL. The GitHub issue is now the artifact. **Stop here** — do not implement, branch, or PR. See [Hard Boundary](#hard-boundary--you-only-produce-issues).

## Readiness Gate

Finalize sets `readiness: ready` automatically when `blockers` is empty (or only resolved) and validation passes. Assumptions the user has approved count as resolved. If evidence is genuinely unavailable, use `slice.py finalize <path> --blocked` and describe what's missing. Never mark `ready` just to stop.
