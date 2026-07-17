# E2E Smoke Test

This document is a step-by-step manual procedure to verify that the golemic system works end-to-end against a real GitHub repository. A human operator can follow this procedure without additional knowledge to execute a complete golemic loop (preflight → dev → PR → review) and validate that both the dev bot and reviewer bot execute their roles correctly.

## Prerequisites

Before starting, ensure you have:

- A local clone of the `golemic_e2e` sandbox repository
- Write access to the `golemic_e2e` repository on GitHub
- Two GitHub bot tokens configured (dev bot and reviewer bot) with write access to the repo
- The `golemic` binary built and available in your PATH (from `go build -o tools/golemic ./cmd/golemic`)
- The `gh` CLI installed and authenticated
- The `pi` CLI installed and launchable (`pi --version` succeeds)

The sandbox repository structure required:

```
golemic_e2e/
├── .git/
├── .golemic/
│   ├── config.json
│   └── guidelines/
│       ├── dev.md
│       └── reviewer.md
├── README.md
└── ... (any other project files)
```

## Part 1: Prepare the Sandbox Repository

### 1.1 Clone or Navigate to golemic_e2e

If you do not yet have a local clone, clone the sandbox repository:

```bash
git clone https://github.com/<your-org>/golemic_e2e.git
cd golemic_e2e
```

If you already have it cloned, navigate into it:

```bash
cd /path/to/golemic_e2e
```

### 1.2 Initialize .golemic/ Configuration

If `.golemic/` does not exist, golemic will scaffold it during preflight. Run preflight first to create the default structure:

```bash
golemic preflight
```

You will see output like:

```
OK: gh installiert
OK: pi installiert
OK: git
FEHLT: .golemic/ Scaffolding — wurde angelegt — bitte config.json und Guidelines ausfüllen
OK: config.json valide
OK: Credentials
```

The scaffolding step creates `.golemic/config.json` with defaults and `.golemic/guidelines/{dev,reviewer}.md` with skeleton templates.

The `FEHLT` status for Scaffolding on first run is expected — it means the templates were created and need to be filled in. Subsequent preflight runs will show `OK` for this check.

### 1.3 Update .golemic/config.json

Edit `.golemic/config.json` to match your sandbox repository. At minimum, ensure the file is valid JSON and contains:

```json
{
  "project": "golemic_e2e",
  "verify_command": "echo 'Verification passed'",
  "label": "ready-for-agent",
  "models": {
    "dev": "z-ai/glm-4.6",
    "reviewer": "deepseek/deepseek-v4-pro"
  },
  "timeout_minutes": 30
}
```

Key fields:
- `project`: Must match the repository name.
- `verify_command`: A shell command that exits 0 on success. For the smoke test, a simple echo is sufficient; for a real project, this would be your build/test command.
- `label`: GitHub issue label to mark issues as ready for the agent (unused in this manual smoke test, but must be valid).
- `models`: Model identifiers for dev and reviewer roles.
- `timeout_minutes`: Maximum runtime per role in minutes.

### 1.4 Update .golemic/guidelines/dev.md

Edit `.golemic/guidelines/dev.md` with basic guidelines for the dev bot. Replace the TODO placeholders:

```markdown
# Dev Guidelines

## Stack
Go, standard library only

## Build/Test
run: echo 'Verification passed'

## Schranken
Do not modify this file or guidelines/reviewer.md. Keep commits focused and minimal.
```

### 1.5 Update .golemic/guidelines/reviewer.md

Edit `.golemic/guidelines/reviewer.md` with basic guidelines for the reviewer bot:

```markdown
# Reviewer Guidelines

## Stack
Go, standard library only

## Build/Test
run: echo 'Verification passed'

## Schranken
Do not commit or modify any files. Review the PR diff and provide a formal review (APPROVE or REQUEST_CHANGES) only. Use golemic submit-review to submit your verdict.
```

### 1.6 Verify Configuration with Preflight

Run preflight again to ensure all checks pass:

```bash
cd /path/to/golemic_e2e
golemic preflight
```

Expected output (all six checks must show `OK`):

```
OK: gh installiert
OK: pi installiert
OK: git
OK: .golemic/ Scaffolding
OK: config.json valide
OK: Credentials
SUCCESS
```

If any check fails, the output will explain the issue. Fix the problem and run preflight again.

### 1.7 Create a Throwaway Issue

Create a test issue in the sandbox repository using the gh CLI:

```bash
gh issue create \
  --title "Smoke test issue" \
  --body "This is a throwaway issue for the E2E smoke test. Delete after testing." \
  --repo "<your-org>/golemic_e2e"
```

Note the issue number from the output. For example:

```
Created issue <your-org>/golemic_e2e#1
```

Write down the issue number; you will need it in Part 2.

### 1.8 Verify Issue State

Check that the issue was created and is in the right state:

```bash
gh issue view <issue-number> --repo "<your-org>/golemic_e2e"
```

Expected: The issue should exist, be open, have no open dependencies, and have minimal content (ideal for testing).

## Part 2: Run the Smoke Test

### 2.1 Set Up Environment

From the golemic_e2e directory, ensure authentication is configured:

**1. Your own `gh` CLI must be authenticated** (for manual verification commands like `gh pr list`, `gh issue view`, etc.):

```bash
gh auth status
```

If not authenticated, run `gh auth login` or export a personal token:

```bash
export GH_TOKEN="<your-personal-token>"
```

**2. Bot tokens must be configured for golemic** (the runner reads these, not `GH_TOKEN`). The tokens must be stored either:
- In environment variables `GOLEMIC_DEV_TOKEN` and `GOLEMIC_REVIEWER_TOKEN`:

  ```bash
  export GOLEMIC_DEV_TOKEN="ghp_..."
  export GOLEMIC_REVIEWER_TOKEN="ghp_..."
  ```

- Or in `~/.golemic/golemic_e2e/credentials.json`:

  ```json
  {
    "dev_token": "ghp_...",
    "reviewer_token": "ghp_..."
  }
  ```

  (Ensure file permissions are `0600`: `chmod 600 ~/.golemic/golemic_e2e/credentials.json`)

The runner automatically uses the dev bot token when starting the dev phase and switches to the reviewer bot token for the review phase.

### 2.2 Run the Smoke Test Command

Execute the golemic run command with the issue number you noted in Part 1:

```bash
cd /path/to/golemic_e2e
golemic run --issue <issue-number>
```

**Do not interrupt this command.** The run will take several minutes (typically 2–10 minutes depending on model latency). During execution, you will see:

1. Preflight checks running
2. Issue details being loaded from GitHub
3. Collision checks (ensuring no stale branches or PRs exist)
4. A development phase where the dev bot works on the issue
5. A review phase where the reviewer bot submits a review

The output will show:

```
OK: gh installiert
OK: pi installiert
OK: git
OK: .golemic/ Scaffolding
OK: config.json valide
OK: Credentials
... (dev and reviewer execution)
issue-<number>-<timestamp>   # Success case: exit code 0, logs in runs/<run-id>/
```

or

```
runs/issue-<number>-<timestamp>   # Failure case: exit code 1, logs in runs/<run-id>/
```

**Success** means exit code 0 and the first output line is a run ID without the `runs/` prefix.
**Failure** means exit code 1 and the output line starts with `runs/`.

### 2.3 Verify the Run in Real Time (Optional)

While the run is executing, you can check GitHub directly in your browser or using `gh`:

**Check if a PR was created:**

```bash
gh pr list --repo "<your-org>/golemic_e2e" --state open
```

You should see a PR with a branch name like `golemic/issue-<number>`.

**Check if a review was submitted:**

```bash
gh pr view <pr-number> --repo "<your-org>/golemic_e2e"
```

Look for a review section showing a formal review (APPROVE or REQUEST_CHANGES) from the reviewer bot account.

### 2.4 Capture the Run ID

If the run succeeded (exit code 0), note the run ID printed on stdout. Example:

```
issue-42-20260716T143025Z
```

This ID is used to locate logs if you need to debug the execution.

## Part 3: Verify Expected Outcome

### 3.1 Verify PR Was Created

List open PRs in the sandbox repository:

```bash
gh pr list --repo "<your-org>/golemic_e2e" --state open
```

Expected: You should see exactly one open PR with a branch name matching `golemic/issue-<number>`, created by the dev bot account.

### 3.2 Verify PR Details

View the PR:

```bash
gh pr view <pr-number> --repo "<your-org>/golemic_e2e"
```

Expected output includes:
- **State**: Open
- **Author**: The dev bot account (e.g., `dev-bot-username`)
- **Branch**: `golemic/issue-<number>` merged from this branch into `main`
- **Commits**: At least one commit from the dev bot
- **Title**: Matches the issue title or a reasonable transformation
- **Body**: References the original issue

### 3.3 Verify Formal Review Submitted

Check the PR's reviews section:

```bash
gh pr view <pr-number> --repo "<your-org>/golemic_e2e" --json reviews
```

Or view it directly in the browser:

```
https://github.com/<your-org>/golemic_e2e/pull/<pr-number>#reviews
```

Expected:
- At least one formal review (type: "APPROVE" or "REQUEST_CHANGES") from the reviewer bot account
- The review should be a GitHub-native review, not just a comment
- If REQUEST_CHANGES, the body should explain what needs to be fixed

### 3.4 Verify Two Different Bot Identities

Confirm that the PR author and reviewer are different GitHub accounts:

```bash
gh pr view <pr-number> --repo "<your-org>/golemic_e2e" --json author,reviews
```

The output should show:
- `author`: Dev bot login (e.g., `dev-bot-username`)
- `reviews` with at least one entry where `author`: is the reviewer bot login (e.g., `reviewer-bot-username`)

These **must be different** GitHub accounts.

**Why this matters:** GitHub does not permit a user to approve their own PRs with a formal review (only comments are allowed). The smoke test verifies that the system correctly uses two separate bot identities to overcome this limitation.

### 3.5 Verify Run Logs (If Needed)

If the run failed or you want to inspect the execution logs, they are stored on your local machine:

```bash
cat ~/.golemic/golemic_e2e/runs/<run-id>/events.jsonl
```

This JSONL file contains all lifecycle events (run_started, pr_opened, review_submitted, run_finished) with timestamps and payloads.

**Troubleshooting log contents:**
- If no `pr_opened` event: dev bot did not successfully open a PR (check dev role output).
- If no `review_submitted` event: reviewer bot did not successfully submit a review (check reviewer role output).
- Check exit codes and stderr in the raw output files.

Transcript output from the agent runs is also available:

```bash
ls -la ~/.golemic/golemic_e2e/runs/<run-id>/
```

You may find files like `dev.stdout.log`, `dev.stderr.log`, `reviewer.stdout.log`, `reviewer.stderr.log` containing the full conversation/output from each agent role.

## Part 4: Teardown

After verifying the expected outcome, clean up all test artifacts. **These commands are destructive; run them only after you have verified the smoke test.**

### 4.1 Close the PR

Close the PR on GitHub to restore the sandbox to a clean state:

```bash
gh pr close <pr-number> --repo "<your-org>/golemic_e2e"
```

Expected output:

```
Closed pull request <your-org>/golemic_e2e#<pr-number>
```

### 4.2 Delete the Branch

If the branch still exists remotely, delete it:

```bash
git push origin --delete golemic/issue-<issue-number>
```

If the branch has already been deleted, this command will fail with "remote ref does not exist" — that is fine.

Verify the branch is gone:

```bash
git branch -r | grep "golemic/issue"
```

Expected: No output (branch is deleted).

### 4.3 Remove Worktrees

The runner creates temporary worktrees for dev and reviewer. Remove them:

```bash
git worktree list
```

You should see entries like:

```
/Users/sergej/Dev/golemic_e2e                [main]
/Users/sergej/.golemic/golemic_e2e/worktrees/issue-<number> [golemic/issue-<number>]
/Users/sergej/.golemic/golemic_e2e/worktrees/issue-<number>-review (detached HEAD origin/golemic/issue-<number>)
```

Remove each worktree:

```bash
git worktree remove ~/.golemic/golemic_e2e/worktrees/issue-<issue-number>
git worktree remove ~/.golemic/golemic_e2e/worktrees/issue-<issue-number>-review
```

Verify they are gone:

```bash
git worktree list
```

Expected: Only the main working directory remains (the one you are currently in).

### 4.4 Delete the Throwaway Issue

Finally, delete the test issue:

```bash
gh issue delete <issue-number> --repo "<your-org>/golemic_e2e" --yes
```

Expected output:

```
Deleted issue <your-org>/golemic_e2e#<issue-number>
```

### 4.5 Verify Sandbox is Clean

Confirm the sandbox repository is back to a clean state:

```bash
cd /path/to/golemic_e2e
git status
```

Expected output:

```
On branch main
Your branch is up to date with 'origin/main'.

nothing to commit, working tree clean
```

Verify no stale branches exist locally:

```bash
git branch
```

Expected: Only `main` or tracking branches unrelated to golemic.

## Common Failure Modes and Diagnostics

### Collision: Worktree or Branch Already Exists

**Symptom:** golemic run exits with message like:

```
Worktree exists at ~/.golemic/golemic_e2e/worktrees/issue-<number>; remove with: git worktree remove ...
```

or

```
Branch golemic/issue-<number> already exists
```

**Diagnosis:** A previous run left behind artifacts. The runner aborts to prevent collisions.

**Fix:**

```bash
# Remove the stale worktree(s)
git worktree remove ~/.golemic/golemic_e2e/worktrees/issue-<number>
git worktree remove ~/.golemic/golemic_e2e/worktrees/issue-<number>-review

# Delete the stale branch
git push origin --delete golemic/issue-<number>

# Close any stale PR
gh pr list --repo "<your-org>/golemic_e2e" --state open
# (find the PR, note its number, then:)
gh pr close <pr-number> --repo "<your-org>/golemic_e2e"

# Then retry
golemic run --issue <issue-number>
```

### Preflight Fails: gh CLI Not Ready

**Symptom:**

```
FEHLT: gh installiert — gh not found: ...
```

**Fix:** Install the GitHub CLI:

```bash
# macOS
brew install gh

# Or download from https://github.com/cli/cli/releases
gh auth login
```

### Preflight Fails: pi CLI Not Ready

**Symptom:**

```
FEHLT: pi installiert — pi not found: ...
```

**Fix:** Ensure `pi` is installed and in your PATH. Check the golemic architecture (§2.1) for agent requirements. You can verify with:

```bash
which pi
pi --version
```

### Preflight Fails: Tokens Not Different

**Symptom:**

```
FEHLT: Credentials — dev and reviewer tokens are identical
```

**Diagnosis:** Both `GOLEMIC_DEV_TOKEN` and `GOLEMIC_REVIEWER_TOKEN` are set to the same token, or the credentials file contains identical tokens.

**Fix:** Create two separate bot accounts on GitHub and generate distinct tokens:

1. Create or use a dev bot account (e.g., `dev-bot-username`).
2. Create or use a reviewer bot account (e.g., `reviewer-bot-username`).
3. Generate a personal access token for each with write access to the sandbox repo.
4. Store the tokens in `~/.golemic/golemic_e2e/credentials.json`:

   ```json
   {
     "dev_token": "ghp_...",
     "reviewer_token": "ghp_..."
   }
   ```

   Or set environment variables:

   ```bash
   export GOLEMIC_DEV_TOKEN="ghp_..."
   export GOLEMIC_REVIEWER_TOKEN="ghp_..."
   ```

### Run Fails: Dev Role Did Not Open PR

**Symptom:** golemic run exits with exit code 1, and you see no PR created on GitHub.

**Diagnosis:** The dev bot did not successfully call `golemic open-pr` or the PR creation failed.

**Fix:**

1. Check the event log:

   ```bash
   cat ~/.golemic/golemic_e2e/runs/<run-id>/events.jsonl | grep pr_opened
   ```

   If no output, the dev bot did not create a PR.

2. Check the dev role's stderr:

   ```bash
   cat ~/.golemic/golemic_e2e/runs/<run-id>/dev.stderr.log
   ```

   Look for error messages from `pi` or the dev bot.

3. Check if the branch was created locally:

   ```bash
   git branch -a | grep golemic
   ```

   If the branch exists, the dev bot created it but failed to push or open the PR.

4. Common causes:
   - **Token expired or insufficient permissions:** Regenerate the dev bot token.
   - **verify_command failed:** The dev bot runs your `verify_command` before opening the PR. If it exits non-zero, the PR is not opened. Check `.golemic/config.json` `verify_command`.
   - **Network issues:** Retry the run.

### Run Fails: Reviewer Role Did Not Submit Review

**Symptom:** golemic run exits with exit code 1, PR exists but has no formal review.

**Diagnosis:** The reviewer bot did not successfully call `golemic submit-review`.

**Fix:**

1. Check the event log:

   ```bash
   cat ~/.golemic/golemic_e2e/runs/<run-id>/events.jsonl | grep review_submitted
   ```

   If no output, the reviewer bot did not submit a review.

2. Check the reviewer role's stderr:

   ```bash
   cat ~/.golemic/golemic_e2e/runs/<run-id>/reviewer.stderr.log
   ```

3. Common causes:
   - **Reviewer bot token invalid:** Regenerate the reviewer bot token and verify it has write access to the repo.
   - **verify_command failed in reviewer context:** The reviewer also runs `verify_command`. If it fails, the review is not submitted.
   - **Reviewer worktree creation failed:** Check if the reviewer worktree exists:

     ```bash
     git worktree list | grep reviewer
     ```

     If not, check GitHub to ensure the PR branch was pushed successfully.

### Run Timeout

**Symptom:** golemic run exits with message like:

```
run_finished outcome: timeout
```

**Diagnosis:** The dev or reviewer role ran longer than the configured timeout (default 30 minutes).

**Fix:**

1. Increase the timeout in `.golemic/config.json`:

   ```json
   "timeout_minutes": 60
   ```

2. Retry the run.

3. If timeouts are frequent, check:
   - Model latency (switch to faster models if available).
   - `verify_command` complexity (optimize if possible).
   - Network conditions.

## Exit Codes and Outcomes

**golemic run exit codes:**

- **Exit 0 (success):** Full loop completed. A PR was created and a formal review was submitted. The output is a run ID like `issue-42-20260716T143025Z`.
- **Exit 1 (failure):** Loop did not complete or encountered an error. The output is `runs/<run-id>` pointing to the log directory. Inspect events.jsonl and transcript files for details.

**Run outcome values** (in events.jsonl `run_finished` event):

- `success` — Full loop completed successfully.
- `dev_failed` — Dev role did not create/open a PR.
- `review_failed` — Reviewer role did not submit a review.
- `timeout` — Dev or reviewer role exceeded the timeout.
- `aborted` — Run was aborted due to collision or preflight failure.

## Next Steps

After a successful smoke test, you can be confident that:

1. The golemic binary builds and runs correctly.
2. The dev bot can successfully implement code, commit, push, and open a PR against real GitHub.
3. The reviewer bot can review, approve, and submit a formal GitHub review.
4. Two distinct bot identities are working correctly (different GitHub accounts for dev and reviewer).
5. The event logging system works and logs are persistent.

You can now proceed to deploy golemic to a production project or refine agent prompts and configuration for your specific stack.

---

## Part 5: AC-008 Full-Chain Scenario — grill-me Issue Creation

> **Status: MANUAL procedure. NOT executed.** This scenario requires a live
> `golemic_e2e` sandbox with two bot tokens and the ability to hit GitHub APIs.
> Do not claim it was executed unless you have actually run the steps below.

This procedure covers AC-008 from the `github-issue-creation-from-grill-me-slice`
specification: create a real issue in `golemic_e2e` via `create_issue.py`, then
run `golemic run --issue N` against it and verify the full chain through PR
creation and review submission.

### Prerequisites

In addition to the prerequisites in Part 1:

- `python3` available on PATH with `jsonschema` installed (`pip install jsonschema`)
- A schema-valid slice JSON produced by the grill-me skill (save it as `slice.json`)
- An existing open issue in `golemic_e2e` to use as a hard dependency; note its
  number as `BLOCKER_N` (or omit `--blocked-by` if there are no dependencies)
- The `golemic` binary built and on PATH

### Step 1: Preflight the sandbox

```bash
cd /path/to/golemic_e2e
golemic preflight
```

All checks must show `OK` before continuing.

### Step 2: Create the issue via create_issue.py

Run from the `golemic_e2e` repository root so `gh` resolves the correct repo:

```bash
cd /path/to/golemic_e2e

python3 /path/to/golemic/.pi/skills/grill-me/scripts/create_issue.py \
  /path/to/slice.json \
  --blocked-by BLOCKER_N
```

Omit `--blocked-by` if there are no hard dependencies.

Expected output (exit 0):
```
#N https://github.com/<your-org>/golemic_e2e/issues/N
```

Note the issue number as `ISSUE_N`.

> **Endpoint verification:** The `gh api --method POST
> /repos/{owner}/{repo}/issues/ISSUE_N/blocked-by -f issue_number=BLOCKER_N`
> call must be verified against the live GitHub API during this step. If the
> endpoint does not exist or returns an unexpected error, inspect the response,
> find the correct endpoint shape in the GitHub REST API documentation, update
> `create_issue.py` accordingly, and re-run.

### Step 3: Verify the issue on GitHub

```bash
gh issue view ISSUE_N --repo "<your-org>/golemic_e2e"
```

Verify all of the following before proceeding:

1. **Title** equals the `title` field from `slice.json`.

2. **Body sections** are present in order: Summary, Stakeholder Intent, Scope,
   Preconditions, Business Rules, Interfaces, Acceptance Scenarios, Definition
   of Done.

3. **Authoritative spec statement** appears above the `<details>` block:
   ```
   > The embedded JSON block below is the authoritative machine-readable
   > specification. The Markdown sections above are its human-readable
   > projection. In any conflict, the JSON is correct.
   ```

4. **Embedded JSON round-trips** to the exact input. Verify with:
   ```bash
   gh issue view ISSUE_N --repo "<your-org>/golemic_e2e" --json body \
     | python3 -c "
   import json, sys, re
   body = json.load(sys.stdin)['body']
   m = re.search(r'\x60\x60\x60json\n(.*?)\n\x60\x60\x60', body, re.DOTALL)
   assert m, 'no json block found in body'
   parsed = json.loads(m.group(1))
   print('JSON round-trip OK, title:', parsed['title'])
   "
   ```

5. **`ready-for-agent` label** (or the label from `.golemic/config.json`) is
   attached to the issue.

6. **Blocked-by relation** to `BLOCKER_N` is visible in the issue sidebar or
   via the API. If `create_issue.py` reported `PARTIAL_FAILURE` on the
   blocked-by step, fix the endpoint (see Step 2 note), set the relation
   manually, then reattach the label before continuing.

### Step 4: Run golemic on the created issue

```bash
cd /path/to/golemic_e2e
golemic run --issue ISSUE_N
```

Expected: exit 0, a run-ID printed to stdout. The dev bot implements the slice,
opens a PR, and the reviewer bot submits a formal review.

### Step 5: Verify the full chain

After `golemic run --issue ISSUE_N` exits 0:

**PR created by the dev bot:**
```bash
gh pr list --repo "<your-org>/golemic_e2e" --state open
```
Expected: one PR on branch `golemic/issue-ISSUE_N` authored by the dev bot.

**Formal review submitted by the reviewer bot:**
```bash
gh pr view <PR_N> --repo "<your-org>/golemic_e2e" --json reviews
```
Expected: at least one review of type `APPROVE` or `REQUEST_CHANGES` from the
reviewer bot account (different from the dev bot account).

**Dev agent referenced slice JSON content:** Inspect the PR description or
commits and confirm they reference content derived from the embedded slice JSON
(e.g., a business rule, acceptance criterion, or Definition of Done item). This
proves that `internal/runner/issue.go` consumed the `body` field and the dev
agent received the full embedded specification.

### Step 6: Teardown

Follow Part 4 of this document to close the PR, delete the branch, remove
worktrees, and delete `ISSUE_N` from `golemic_e2e`.
