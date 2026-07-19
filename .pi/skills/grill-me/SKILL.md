---
name: grill-me
description: Interview the user about a software plan just enough to understand intent, then autonomously fill and file an implementable vertical slice as a GitHub issue. Ask only what the code can't answer; every question offers max 4 options plus a clear recommendation. Use when the user wants a plan grilled, stress-tested, or turned into a ready issue.
---

# Grill Me

## Goal

Understand what the user actually wants and why. Everything the codebase can answer, you answer yourself. Everything you can derive from the intent, you derive yourself. You only ask the user about genuine product decisions the code can't settle — and when you ask, you make it a fast multiple-choice pick, not an essay.

The end product is a validated GitHub issue (an autonomously implementable vertical slice). You fill the whole slice; the user never dictates individual schema fields.

## Core Principles

1. **Ask only what you can't find out yourself.** Classification (`command`/`query`/`process`/`integration`), scope boundaries, behavior, business rules, acceptance scenarios, I/O contract, verify commands, and codebase evidence are *your* job to derive — never a question to the user. Inspect the code first; the user's time is for product intent, not form-filling.
2. **Adaptive question count.** Ask as many questions as you genuinely need and no more. Some plans need one question; some need ten. Don't announce a count or estimate.
3. **Every question is multiple choice.** Offer **max 4 options**, then a clear recommendation with reasoning. See format below.
4. **Confirm before filing.** When you have everything, just ask whether you're done and may create the issue. Don't dump the full filled-out slice for review — a simple go/no-go is enough.

## Question Format

Only ask when the answer is a product judgment the code can't make. Then:

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

1. **Inspect first.** Read the relevant code: modules, routes, conventions, test/verify commands. Classify the slice yourself using `references/slice-types.md`. Derive scope, behavior, rules, I/O, acceptance, and verify commands as far as the code allows. Note what you settled and what remains a genuine product decision.
2. **Similarity scan.** Run `gh_issue_index.py --with-body`. If something looks related, that's a legitimate question (offer the candidate issues as options + a recommendation).
3. **Interview.** Ask only the open product decisions, one at a time, in the multiple-choice format above.
4. **Fill the slice.** Use `slice.py write` (or `new` + `set`) to populate every field from intent + code + answers. Record each non-trivial assumption in `blockers` as `kind: assumption`.
5. **Confirm.** Ask the user whether you're done and may create the issue. No field-by-field review.
6. **Finalize and file.** `slice.py finalize <path>` (or `--blocked` if genuine blockers remain), then `create_issue.py <slice.json> [--blocked-by N[,N...]]`. Print the issue URL. The GitHub issue is now the artifact.

## Readiness Gate

Finalize sets `readiness: ready` automatically when `blockers` is empty (or only resolved) and validation passes. Assumptions the user has approved count as resolved. If evidence is genuinely unavailable, use `slice.py finalize <path> --blocked` and describe what's missing. Never mark `ready` just to stop.
