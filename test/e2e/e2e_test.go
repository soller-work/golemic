//go:build e2e

// Package e2e contains the deterministic E2E test suite for golemic.
// All scenarios use the detagent binary (built from test/e2e/detagent) as a
// fake pi that drives golemic through scripted outcomes without LLM calls.
//
// Run with:
//
//	go test -tags e2e ./test/e2e/... -v
//
// Prerequisites: GOLEMIC_E2E_PATH, GOLEMIC_E2E_REPO (or gh auth), GOLEMIC_DEV_TOKEN,
// GOLEMIC_REVIEWER_TOKEN. Missing prerequisites cause t.Skip, never t.Fatal.
package e2e

import (
	"fmt"
	"strings"
	"testing"

	"golemic/test/e2e/harness"
	"golemic/test/e2e/scenarios"
)

// TestE2EHappyPath exercises the full issue → dev → PR → approved review → merge loop.
// Expects golemic to exit 0 and the run_finished outcome to be "ok".
func TestE2EHappyPath(t *testing.T) {
	h := harness.New(t)
	if h == nil {
		return
	}

	issueNum, err := h.CreateIssue(scenarios.HappyPath)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	branch := fmt.Sprintf("golemic/issue-%d", issueNum)

	t.Cleanup(func() {
		h.CloseIssue(issueNum)
		h.DeleteBranch(branch)
		if err := h.RemoveWorktrees(); err != nil {
			t.Logf("cleanup: RemoveWorktrees: %v", err)
		}
		if err := h.RemoveRuns(); err != nil {
			t.Logf("cleanup: RemoveRuns: %v", err)
		}
	})

	result := h.RunWithTimeout(t, issueNum, 0)
	t.Logf("golemic stdout:\n%s", result.Stdout)
	t.Logf("golemic stderr:\n%s", result.Stderr)

	eventsPath := h.LatestRunEventsPath()

	t.Run("ExitCode", func(t *testing.T) {
		if result.ExitCode != 0 {
			t.Errorf("want exit 0, got %d", result.ExitCode)
		}
	})

	t.Run("Outcome", func(t *testing.T) {
		outcome := harness.RunFinishedOutcome(eventsPath)
		if outcome != "ok" {
			t.Errorf("run_finished outcome: got %q, want %q", outcome, "ok")
		}
	})

	t.Run("PROpened", func(t *testing.T) {
		if !harness.HasEvent(eventsPath, "pr_opened") {
			t.Error("pr_opened event not found in events.jsonl")
		}
	})

	t.Run("ReviewSubmitted", func(t *testing.T) {
		if !harness.HasEvent(eventsPath, "review_submitted") {
			t.Error("review_submitted event not found in events.jsonl")
		}
	})

	t.Run("PRMerged", func(t *testing.T) {
		if !harness.HasEvent(eventsPath, "pr_merged") {
			t.Error("pr_merged event not found in events.jsonl")
		}
	})
}

// TestE2EReviewerRejectsOnce exercises the changes_requested → fix → approved loop.
// Expects two review_submitted events (changes_requested then approved) and a merged PR.
func TestE2EReviewerRejectsOnce(t *testing.T) {
	h := harness.New(t)
	if h == nil {
		return
	}

	issueNum, err := h.CreateIssue(scenarios.ReviewerRejectsOnce)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	branch := fmt.Sprintf("golemic/issue-%d", issueNum)

	t.Cleanup(func() {
		h.CloseIssue(issueNum)
		h.DeleteBranch(branch)
		if err := h.RemoveWorktrees(); err != nil {
			t.Logf("cleanup: RemoveWorktrees: %v", err)
		}
		if err := h.RemoveRuns(); err != nil {
			t.Logf("cleanup: RemoveRuns: %v", err)
		}
	})

	result := h.RunWithTimeout(t, issueNum, 0)
	t.Logf("golemic stdout:\n%s", result.Stdout)
	t.Logf("golemic stderr:\n%s", result.Stderr)

	eventsPath := h.LatestRunEventsPath()

	t.Run("ExitCode", func(t *testing.T) {
		if result.ExitCode != 0 {
			t.Errorf("want exit 0, got %d", result.ExitCode)
		}
	})

	t.Run("Outcome", func(t *testing.T) {
		outcome := harness.RunFinishedOutcome(eventsPath)
		if outcome != "ok" {
			t.Errorf("run_finished outcome: got %q, want %q", outcome, "ok")
		}
	})

	t.Run("TwoReviews", func(t *testing.T) {
		count := harness.CountEvents(eventsPath, "review_submitted")
		if count < 2 {
			t.Errorf("want at least 2 review_submitted events, got %d", count)
		}
	})

	t.Run("PRMerged", func(t *testing.T) {
		if !harness.HasEvent(eventsPath, "pr_merged") {
			t.Error("pr_merged event not found in events.jsonl")
		}
	})
}

// TestE2EDevVerifyFails exercises the dev-failure path where the verify command
// fails (or the detagent exits non-zero after a failing project check).
// Expects golemic to exit non-zero with a dev_failed outcome.
func TestE2EDevVerifyFails(t *testing.T) {
	h := harness.New(t)
	if h == nil {
		return
	}

	issueNum, err := h.CreateIssue(scenarios.DevVerifyFails)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	branch := fmt.Sprintf("golemic/issue-%d", issueNum)

	t.Cleanup(func() {
		h.CloseIssue(issueNum)
		h.DeleteBranch(branch)
		if err := h.RemoveWorktrees(); err != nil {
			t.Logf("cleanup: RemoveWorktrees: %v", err)
		}
		if err := h.RemoveRuns(); err != nil {
			t.Logf("cleanup: RemoveRuns: %v", err)
		}
	})

	result := h.RunWithTimeout(t, issueNum, 0)
	t.Logf("golemic stdout:\n%s", result.Stdout)
	t.Logf("golemic stderr:\n%s", result.Stderr)

	eventsPath := h.LatestRunEventsPath()

	t.Run("NonZeroExit", func(t *testing.T) {
		if result.ExitCode == 0 {
			t.Error("want non-zero exit, got 0")
		}
	})

	t.Run("FailureOutcome", func(t *testing.T) {
		outcome := harness.RunFinishedOutcome(eventsPath)
		if outcome == "ok" || outcome == "" {
			t.Errorf("run_finished outcome: got %q, want a failure outcome (dev_failed etc.)", outcome)
		}
	})
}

// TestE2ECollision exercises golemic's collision detection when an open PR
// already exists for the issue's branch.
// Expects golemic to exit non-zero with outcome "aborted".
func TestE2ECollision(t *testing.T) {
	h := harness.New(t)
	if h == nil {
		return
	}

	issueNum, err := h.CreateIssue(scenarios.Collision)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	branch := fmt.Sprintf("golemic/issue-%d", issueNum)

	// Pre-create the collision PR before running golemic.
	_, collisionCleanup := h.CreateCollisionPR(t, issueNum)

	t.Cleanup(func() {
		collisionCleanup()
		h.CloseIssue(issueNum)
		h.DeleteBranch(branch)
		if err := h.RemoveWorktrees(); err != nil {
			t.Logf("cleanup: RemoveWorktrees: %v", err)
		}
		if err := h.RemoveRuns(); err != nil {
			t.Logf("cleanup: RemoveRuns: %v", err)
		}
	})

	result := h.RunWithTimeout(t, issueNum, 0)
	t.Logf("golemic stdout:\n%s", result.Stdout)
	t.Logf("golemic stderr:\n%s", result.Stderr)

	eventsPath := h.LatestRunEventsPath()

	t.Run("NonZeroExit", func(t *testing.T) {
		if result.ExitCode == 0 {
			t.Error("want non-zero exit for collision, got 0")
		}
	})

	t.Run("AbortedOutcome", func(t *testing.T) {
		outcome := harness.RunFinishedOutcome(eventsPath)
		if outcome != "aborted" {
			t.Errorf("run_finished outcome: got %q, want %q", outcome, "aborted")
		}
	})

	t.Run("NoPROpened", func(t *testing.T) {
		if harness.HasEvent(eventsPath, "pr_opened") {
			t.Error("golemic should not open a PR when collision is detected")
		}
	})
}

// TestE2ETimeout exercises golemic's agent-stall handling when the detagent
// sleeps without producing output past the idle timeout.
// Expects golemic to exit non-zero with a timeout or stalled outcome.
func TestE2ETimeout(t *testing.T) {
	h := harness.New(t)
	if h == nil {
		return
	}

	issueNum, err := h.CreateIssue(scenarios.Timeout)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	branch := fmt.Sprintf("golemic/issue-%d", issueNum)

	t.Cleanup(func() {
		h.CloseIssue(issueNum)
		h.DeleteBranch(branch)
		if err := h.RemoveWorktrees(); err != nil {
			t.Logf("cleanup: RemoveWorktrees: %v", err)
		}
		if err := h.RemoveRuns(); err != nil {
			t.Logf("cleanup: RemoveRuns: %v", err)
		}
	})

	// Use a short idle timeout and no retries so the test completes quickly.
	// GOLEMIC_AGENT_IDLE_TIMEOUT_SEC=20: agent is killed after 20 s of silence.
	// GOLEMIC_AGENT_MAX_STALL_RETRIES=0: no retries, fail immediately on first stall.
	result := h.RunWithTimeout(t, issueNum, 120, // 2-minute wall-clock cap for the test
		"GOLEMIC_AGENT_IDLE_TIMEOUT_SEC=20",
		"GOLEMIC_AGENT_MAX_STALL_RETRIES=0",
	)
	t.Logf("golemic stdout:\n%s", result.Stdout)
	t.Logf("golemic stderr:\n%s", result.Stderr)

	eventsPath := h.LatestRunEventsPath()

	t.Run("NonZeroExit", func(t *testing.T) {
		if result.ExitCode == 0 {
			t.Error("want non-zero exit for timeout/stall, got 0")
		}
	})

	t.Run("FailureOutcome", func(t *testing.T) {
		outcome := harness.RunFinishedOutcome(eventsPath)
		if !isTimeoutOutcome(outcome) {
			t.Errorf("run_finished outcome: got %q, want timeout or stalled", outcome)
		}
	})
}

// isTimeoutOutcome returns true for outcomes produced by agent kill paths.
func isTimeoutOutcome(outcome string) bool {
	for _, o := range []string{"timeout", "stalled"} {
		if strings.EqualFold(outcome, o) {
			return true
		}
	}
	return false
}
