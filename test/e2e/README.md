# E2E Test Infrastructure

End-to-end test infrastructure for golemic. Provides process spawning harness, GitHub
API helpers, test fixture management, and automatic cleanup for reliable E2E testing.

## Directory structure

```
test/e2e/
  harness/
    harness.go        — GollemicRunner: subprocess spawning, config/credential loading
    cleanup.go        — Worktree and run directory cleanup (idempotent, P2-4 fix)
    fixtures.go       — Valid and broken config.json fixtures
    harness_test.go   — AC-001 (initialization), AC-003 (subprocess execution), P2-3 fix
    cleanup_test.go   — AC-005 (cleanup idempotency)
  github/
    github.go         — GitHub helpers via gh CLI (retries, P2-5; CreateTestPR repoPath, P1-1)
    github_test.go    — AC-002 (issue lifecycle), AC-004 (PR/review assertions)
```

## Prerequisites

The E2E tests require:

1. **Golemic binary** — built via `go build ./cmd/golemic`.
   Set `GOLEMIC_BINARY` env var to override the default repo-root location.

2. **golemic_e2e sandbox repository** — cloned and configured with:
   - `.golemic/config.json` (valid config)
   - `~/.golemic/golemic_e2e/credentials.json` or env vars `GOLEMIC_DEV_TOKEN`
     and `GOLEMIC_REVIEWER_TOKEN`

3. **gh CLI** — installed and authenticated (`gh auth status`).

## Quick start

```bash
# Run all harness tests (no GitHub access required)
go test ./test/e2e/harness -v

# Run GitHub helper tests (requires gh CLI and golemic_e2e repo)
go test ./test/e2e/github -v

# Run all E2E tests
go test ./test/e2e/... -v
```

## Test isolation (BR-002)

Each test creates issues with unique titles (including a nanosecond timestamp).
This prevents collisions in parallel runs. GitHub assigns auto-incrementing issue
numbers, ensuring unique identifiers per test.

## Token redaction (BR-003)

The harness `Exec` method automatically replaces token values in captured stdout
and stderr with `***REDACTED***`. Test output must never contain real token values.

Verify with:
```bash
go test ./test/e2e/harness -v -run TestTokenRedaction
```

## Cleanup (BR-001)

All cleanup operations are idempotent — safe to call multiple times:

- `RemoveWorktrees` — uses `git worktree remove --force` for proper cleanup (P2-4);
  falls back to `os.RemoveAll` on git failure
- `CleanupRuns` — removes `runs/issue-*` directories
- `DeleteTestIssue` — calls `gh issue delete`; swallows only 404/not-found errors (P1-2)

Use `defer` for automatic teardown:
```go
issueNum, err := github.CreateTestIssue(repo, title, body)
defer github.DeleteTestIssue(repo, issueNum)
```

## CreateTestPR API (P1-1, P2-7 fixes)

`CreateTestPR` now accepts a `repoPath` parameter to operate on the golemic_e2e
repository. Each PR uses an isolated git worktree (not process-wide `git checkout`)
to ensure parallel test safety. Unique branch names (timestamp-based) prevent collisions.

Signature:
```go
prNum, err := github.CreateTestPR(repo, repoPath, issueNum)
if err != nil { /* handle */ }
defer github.CloseTestPR(repo, prNum)
```

## Timeout handling (P2-6 fix)

`GollemicRunner.Exec` applies the config's `timeout_minutes` to the context
if no deadline is already set. This ensures subprocesses don't hang indefinitely.

## Retry logic (P2-5 fix)

`retryGhCmd` retries transient GitHub API errors (rate limits, timeouts, connection
resets) with exponential backoff (1s, 2s, 4s). Up to 3 attempts per operation.

## Auth error validation (P2-3 fix)

`TestHarnessAuthErrorValidation` verifies that missing credentials are caught
at harness initialization (fail-fast, BR-004).

## Extending

To add new E2E test scenarios (I2–I7):

1. Import `test/e2e/harness` for `GollemicRunner`
2. Import `test/e2e/github` for issue/PR helpers
3. Create unique issues with `github.CreateTestIssue(repo, title, body)`
4. Run golemic with `runner.Exec(ctx, "run", "--issue", fmt.Sprint(issueNum))`
   - If ctx has no deadline, Exec applies config timeout (P2-6)
5. Assert outcomes with `github.AssertPRExists(repo, prNum)`, `github.AssertReviewExists(repo, prNum)`
6. Defer cleanup: `github.DeleteTestIssue(repo, issueNum)`, `harness.RemoveWorktrees(golemicDir, repoRoot)`

See existing test files for patterns.

## CI integration

The test infrastructure has no local-only dependencies. All external tools
(gh CLI, golemic binary) are configurable via environment variables. Tests
automatically skip when prerequisites are not met (e.g., `requireGh` for
GitHub-dependent tests).

## All tests passing

- AC-001: Harness initialization ✓
- AC-002: Issue lifecycle (create/delete) ✓
- AC-003: Subprocess execution with output capture ✓
- AC-004: GitHub PR/review assertions ✓
- AC-005: Cleanup idempotency ✓
