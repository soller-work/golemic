# E2E Nightly Workflow — Setup Guide

This document describes how to install the nightly E2E workflow in the
`soller-work/golemic_e2e` sandbox repository. Follow these steps once; the
workflow runs unattended every night after that.

## Prerequisites

- Write access to `soller-work/golemic_e2e`.
- A GitHub account (or bot account) for the **dev** role and another for the
  **reviewer** role — the sandbox must allow separate author and reviewer
  identities.
- Access to create fine-grained personal access tokens (PATs) on each account.

## Step 1 — Create the three tokens

### GOLEMIC_DEV_TOKEN

Fine-grained PAT on the **dev** account scoped to `soller-work/golemic_e2e`:

| Permission | Level |
|---|---|
| Contents | Read and write |
| Issues | Read and write |
| Pull requests | Read and write |
| Metadata | Read (required) |

### GOLEMIC_REVIEWER_TOKEN

Fine-grained PAT on the **reviewer** account scoped to `soller-work/golemic_e2e`:

| Permission | Level |
|---|---|
| Contents | Read and write |
| Issues | Read and write |
| Pull requests | Read and write |
| Metadata | Read (required) |

### GOLEMIC_TICKET_TOKEN

Fine-grained PAT on any account with access to `soller-work/golemic`:

| Permission | Level |
|---|---|
| Issues | Read and write |
| Metadata | Read (required) |

This token is the only credential that touches the golemic repository; it never
reaches the golemic_e2e code or sandbox.

## Step 2 — Add repository secrets

In `soller-work/golemic_e2e`, go to **Settings → Secrets and variables →
Actions → New repository secret** and add all three secrets:

| Secret name | Value |
|---|---|
| `GOLEMIC_DEV_TOKEN` | PAT from Step 1 (dev account) |
| `GOLEMIC_REVIEWER_TOKEN` | PAT from Step 1 (reviewer account) |
| `GOLEMIC_TICKET_TOKEN` | PAT from Step 1 (ticket account) |

## Step 3 — Create the `e2e-failure` label in golemic

In `soller-work/golemic`, go to **Issues → Labels → New label** and create:

| Field | Value |
|---|---|
| Name | `e2e-failure` |
| Colour | Any (e.g. `#e11d48`) |

The reporter will error if the label does not exist when it tries to file a
ticket.

## Step 4 — Install the workflow file

Copy `docs/e2e-workflow-template.yml` from the golemic repository to:

```
soller-work/golemic_e2e/.github/workflows/e2e-nightly.yml
```

Commit and push to the default branch of `golemic_e2e`. GitHub Actions will
pick it up automatically.

## Step 5 — Validate with a manual run

1. In `soller-work/golemic_e2e`, go to **Actions → E2E Nightly**.
2. Click **Run workflow → Run workflow** (uses the default branch).
3. Watch the run. A green run with all scenarios passing produces no new issues
   in golemic. A red run files exactly one deduplicated ticket per failed
   scenario.

## Workflow behaviour summary

| Situation | Outcome |
|---|---|
| All scenarios pass | Run green; no issues filed |
| One or more scenarios fail | Run red; one issue per new fingerprint filed in golemic with label `e2e-failure` |
| Same failure on a subsequent night | Run red; no new issue (existing fingerprint suppressed) |
| Reporter cannot reach GitHub API after retries | Run red; reporter step exits non-zero with error message |

## Updating the workflow

The workflow file in `golemic_e2e` is intentionally thin. All logic — scenarios,
the reporter, the harness — lives in the golemic repository and is checked out
fresh on every run. You should rarely need to update the workflow file itself.

The only reasons to update `e2e-nightly.yml` are:
- Changing the cron schedule.
- Pinning a new `actions/checkout` or `actions/setup-go` version.
- Adding or removing secrets.

## Troubleshooting

**Reporter exits with "GOLEMIC_TICKET_TOKEN is required"**
: The secret is missing or misspelled. Recheck Step 2.

**Reporter exits with "HTTP 422 Unprocessable Entity"**
: The `e2e-failure` label does not exist in `soller-work/golemic`. Create it
  following Step 3.

**Reporter exits with "no test events parsed"**
: The E2E suite crashed before producing any output. Check the suite step logs.

**Duplicate tickets appear**
: The reporter fingerprint is stable per `scenario|failure_class`. Duplicates
  mean the label `e2e-failure` was removed from an existing open issue, or the
  issue was closed before the repeat run. Re-open the issue and restore the
  label to suppress future duplicates.
