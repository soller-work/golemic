# E2E Test Suite

Deterministic end-to-end tests for golemic. The suite drives five fixed scenarios
against the `golemic_e2e` sandbox repository using a scripted fake `pi` agent
(the **detagent**). No LLM calls are made.

## Quick start

```bash
# Prerequisites: gh authenticated, sandbox checked out, tokens in env.
export GOLEMIC_E2E_PATH=~/golemic_e2e
export GOLEMIC_DEV_TOKEN=<dev-github-token>
export GOLEMIC_REVIEWER_TOKEN=<reviewer-github-token>

# Optional (defaults to <gh-user>/golemic_e2e):
# export GOLEMIC_E2E_REPO=owner/golemic_e2e

# Run all five scenarios:
go test -tags e2e ./test/e2e/... -v

# Run a single scenario:
go test -tags e2e ./test/e2e/... -run HappyPath -v
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `GOLEMIC_E2E_PATH` | yes | Local path to a checked-out `golemic_e2e` git repo |
| `GOLEMIC_DEV_TOKEN` | yes | GitHub token with write access to the sandbox (dev role) |
| `GOLEMIC_REVIEWER_TOKEN` | yes | GitHub token with write access to the sandbox (reviewer role) |
| `GOLEMIC_E2E_REPO` | no | `owner/repo` slug (default: `<gh-user>/golemic_e2e`) |
| `GOLEMIC_BINARY` | no | Path to a pre-built golemic binary (default: built from source) |

## Scenarios

| Test | Scenario | What it proves |
|---|---|---|
| `TestE2EHappyPath` | `happy_path` | Full loop: issue → PR → approved review → merge |
| `TestE2EReviewerRejectsOnce` | `reviewer_rejects_once` | Round-trip: changes_requested then approved |
| `TestE2EDevVerifyFails` | `dev_verify_fails` | Dev failure when verify command fails |
| `TestE2ECollision` | `collision` | Collision detection with pre-existing PR |
| `TestE2ETimeout` | `timeout` | Agent kill when detagent stalls without output |

## Architecture

The **harness** (`test/e2e/harness/`) builds both the golemic binary and the detagent
binary (named `pi`) at test startup, creates issues in the sandbox with an
`E2E-SCENARIO: <name>` marker in the body, prepends the fake `pi` to PATH before
spawning golemic, and handles idempotent cleanup via `t.Cleanup`.

The **detagent** (`test/e2e/detagent/`) reads `GOLEMIC_ROLE` and `E2E-SCENARIO`
from the issue spec (via `gm_slice_get` over the broker socket), then follows a
fixed script: write files, stage them, call `gm_project_check`, call `gm_dev_done`
(dev), or call `gm_pr_view` then `gm_review_submit` (reviewer).

**Scenario names** (`test/e2e/scenarios/scenarios.go`) are the single source of
truth shared between the harness and detagent.

## Sandbox requirements

For `dev_verify_fails` to prove that a failing verify is caught (rather than
detagent simply exiting 1), the sandbox's `.golemic/config.json` should set:

```json
{ "verify_command": "bash ./verify.sh" }
```

and `verify.sh` in the sandbox repo should fail when `VERIFY_BREAK` exists:

```bash
#!/usr/bin/env bash
set -e
if [ -f VERIFY_BREAK ]; then
  echo "VERIFY_BREAK sentinel found — failing verify" >&2
  exit 1
fi
echo "verify ok"
```

Without this, the test still passes (detagent exits 1 explicitly) but the verify
failure path is not exercised.

## Missing prerequisites

Any scenario whose prerequisites are not met (missing sandbox, missing auth,
missing tokens) calls `t.Skip` with an explanatory message instead of failing.
A crashed or partial previous run does not break the next run because cleanup is
idempotent via `t.Cleanup`.
