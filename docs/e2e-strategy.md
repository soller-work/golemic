# E2E Strategy

Status: agreed 2026-07-30. This document is the authoritative contract for golemic's
end-to-end testing. Implementation is tracked in dedicated issues; this document
describes the target state and the reasoning behind it.

## Goals

1. **Prove the full workflow on a foreign repo.** The sandbox repository
   `soller-work/golemic_e2e` is a separate project whose only purpose is to prove,
   on GitHub and for posterity, that golemic drives a complete Issue → PR → Review →
   Merge loop against a repository that is not golemic itself.
2. **Keep golemic's counters clean.** All side effects (issues, PRs, reviews,
   merges) happen in `golemic_e2e`, never in `golemic`. The only artifacts the E2E
   system creates in `golemic` are bug tickets for real failures.
3. **Determinism over realism.** E2E runs use scripted, deterministic agents — no
   LLM calls. They are fast, cheap, and never flaky for model reasons. What is
   under test is golemic's orchestration machinery, not agent intelligence.

## Decisions (fixed)

| Topic | Decision |
|---|---|
| Test code location | `test/e2e/` in the **golemic** repo (build tag `e2e`) |
| Execution | **Nightly** GitHub Actions workflow in the **golemic_e2e** repo, running against golemic `main` |
| Approval gate | **None.** E2E does not block reviewer approvals or merges in golemic |
| Agents | **Deterministic scripts** (a fake `pi` executable), no LLMs |
| Failures | Each failed scenario produces **exactly one deduplicated bug ticket** in golemic, label `e2e-failure`, **without** `ready-for-agent` (human triage promotes it) |
| Existing E2E tests | May only be modified/deleted with **explicit user permission**, enforced by prompt rules **and** a hard verify check |
| New feature issues | grill-me must formulate E2E scenarios for `change_type=feature` slices; waivable only by explicit user decision, documented in the issue |
| Legacy suite | The pre-2026-07 `test/e2e` suite (LLM-based) is fully replaced; nothing of it is load-bearing |

## Architecture

```
golemic repo                          golemic_e2e repo
─────────────                         ────────────────
test/e2e/                             .github/workflows/e2e-nightly.yml
  scenarios/   (scenario definitions)     - cron: nightly
  detagent/    (deterministic pi)         - checkout golemic@main
  harness/     (spawn golemic binary)     - go build ./cmd/golemic
  report/      (failure → bug ticket)     - run suite (issues/PRs land here)
                                          - on failure: file e2e-failure
                                            tickets in golemic (deduped)
```

The workflow file in `golemic_e2e` is intentionally **thin**: checkout golemic,
build, run `go test -tags e2e ./test/e2e/...`, invoke the reporter. All logic,
scenarios, and the reporter live in the golemic repo so they evolve with the code
under test. The workflow is delivered as a template in
`docs/e2e-workflow-template.yml` and installed into `golemic_e2e` manually by the
user (golemic's runner cannot write to other repos; secrets must be configured by
a human anyway).

### Required secrets in golemic_e2e (manual, one-time)

- `GOLEMIC_DEV_TOKEN` / `GOLEMIC_REVIEWER_TOKEN` — two distinct GitHub tokens with
  write access to `golemic_e2e` (issues, PRs, reviews, merges).
- `GOLEMIC_TICKET_TOKEN` — token with issue-write access to `soller-work/golemic`,
  used only by the failure reporter.

No LLM API keys are needed — agents are deterministic.

## The deterministic agent

Golemic invokes its agent by spawning `pi` from `PATH`
(`internal/agent/agent.go`, `newPiCmd`). The deterministic agent is a program
named `pi` placed **first on `PATH`** for the duration of the E2E run. It must
honor the real contract:

- **Invocation:** `pi -p --mode json --session-id <id> --append-system-prompt @<file>
  --tools <csv> --model <model> <userPrompt>` (see `buildPiArgs`,
  `internal/agent/agent.go:428`).
- **Environment:** `GOLEMIC_GM_SOCK` (unix socket of the gm broker),
  `GOLEMIC_RUN_ID`, `GOLEMIC_INVOCATION_ID`, `GOLEMIC_ROLE` (`dev` or
  `reviewer`), cwd = the role's worktree.
- **Broker protocol:** one JSON object per connection to the unix socket:
  `{"callId": "...", "runId": $GOLEMIC_RUN_ID, "invocationId":
  $GOLEMIC_INVOCATION_ID, "tool": "gm_...", "params": {...}}`; the broker answers
  one JSON line `{"callId": ..., "result": ...}` (see
  `internal/gmbroker/broker.go`, `handleConn`).
- **Terminal detection:** the runner watches agent stdout for pi-style
  `tool_execution_end` events of `gm_dev_done` / accepted `gm_review_submit`
  (`internal/agent/pifilter.go:93`, `terminalDoneFromLine`). The deterministic
  agent must emit these JSON lines on stdout after its terminal tool call.
- **Dev role script:** read the spec (`gm_slice_get`), apply the change dictated
  by the scenario marker (see below) to the worktree, commit, run
  `gm_project_check`, then `gm_dev_done`.
- **Reviewer role script:** `gm_pr_view`, `gm_project_check`, then
  `gm_review_submit` with the verdict dictated by the scenario marker.

### Scenario markers

Each E2E scenario creates a sandbox issue whose body contains a machine-readable
marker line:

```
E2E-SCENARIO: <name>
```

The deterministic agent parses the marker from the issue spec and behaves
accordingly (e.g. `happy_path` → clean implementation + approval;
`reviewer_rejects_once` → one `changes_requested` round, then approval;
`dev_verify_fails` → commit that breaks the verify command). One agent binary,
table-driven behavior. Scenario names are the single source of truth shared
between test code and agent script.

### Initial scenario suite

| Scenario | Proves |
|---|---|
| `happy_path` | full loop: issue → PR → approved review → merge, exit 0 |
| `reviewer_rejects_once` | changes_requested round-trip, dev fixes, second review approves |
| `dev_verify_fails` | failing verify is caught, run ends in the documented failure outcome |
| `collision` | pre-existing open PR for the issue is handled per collision rules |
| `timeout` | a stalling agent is killed and the run fails cleanly |

The suite grows over time: every `change_type=feature` slice ships its own
scenarios (see grill-me obligations).

## Failure → bug ticket pipeline

After the nightly run, a reporter (Go program in `test/e2e/report/`, invoked by
the workflow) examines results:

- Per failed scenario it computes a **fingerprint** (scenario name + failure
  class). It searches open `e2e-failure` issues in golemic for the fingerprint
  (embedded as `E2E-FINGERPRINT: <hash>` in the issue body).
- **No open ticket with that fingerprint** → create one: title
  `e2e: <scenario> failed (<failure class>)`, label `e2e-failure`, body with
  fingerprint, run link, trimmed logs, events excerpt.
- **Open ticket exists** → add nothing or at most update a "last seen" line;
  never a duplicate ticket.
- Tickets are **not** labeled `ready-for-agent`. A human triages each morning and
  promotes real bugs by adding the label manually.

## Protection of existing E2E tests

Existing E2E test files encode proven promises; silently weakening them would
silently weaken the proof. Two enforcement layers:

1. **Prompt rule** (first line of defense): dev and reviewer agent guidelines
   state that files under `test/e2e/` may not be modified or deleted unless the
   issue explicitly grants permission. Adding new files is always allowed.
2. **Hard verify check** (guarantee): a script wired into golemic's verify
   command fails the build when the working tree modifies or deletes an existing
   tracked file under `test/e2e/` **unless** the issue body contains the marker
   line `E2E-MODIFY-APPROVED`. grill-me writes this marker only after the user
   explicitly approved the modification during the interview.

## grill-me obligations

For `change_type=feature` slices, grill-me must:

- Formulate concrete E2E scenarios (marker name + expected outcome) as part of
  the slice; the dev agent implements them in `test/e2e/` alongside the feature.
- Treat this as mandatory **with user waiver**: if the user explicitly decides
  the feature does not need an E2E scenario (e.g. no orchestration-loop
  involvement), the waiver and its reasoning are recorded in the issue body.
- Ask the user before setting `E2E-MODIFY-APPROVED` whenever a slice requires
  changing existing E2E tests.

## Rollout

Implemented as four issues (2 blocked by 1):

1. **Deterministic E2E framework** — remove the legacy LLM-based suite, build the
   deterministic agent, harness, and initial scenario suite.
2. **Nightly workflow + failure reporter** — workflow template, reporter with
   fingerprint dedup, setup documentation.
3. **E2E protection check** — verify-integrated guard + prompt rules +
   `E2E-MODIFY-APPROVED` marker convention.
4. **grill-me E2E obligations** — schema section for E2E scenarios,
   mandatory-with-waiver validation for features, marker mechanics.
