package runner

import (
	"context"
	"testing"

	"golemic/internal/agent"
	"golemic/internal/eventlog"
)

// --- AC-006: Runner assigns strictly increasing turnId per agent turn ---

func TestRunnerTurnIDMonotonic_AC006(t *testing.T) {
	// dev → reviewer (approved) — two RunRole calls must get TurnID 1, 2.
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	var captured []agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		captured = append(captured, cfg)
		switch cfg.Role {
		case "dev":
			// Satisfy the §10 gate; runner writes pr_opened.
			if !sendGMProjectCheck(cfg.Env) {
				t.Errorf("TestRunnerTurnIDMonotonic_AC006: sendGMProjectCheck failed")
			}
			if !sendGMDevDone(cfg.Env) {
				t.Errorf("TestRunnerTurnIDMonotonic_AC006: sendGMDevDone failed")
			}
		case "reviewer":
			writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
		}
		return 0, agent.TranscriptPaths{}, nil
	})

	runOrchestrate(t, r, logPath)

	if len(captured) < 2 {
		t.Fatalf("expected >= 2 RunRole calls, got %d", len(captured))
	}

	// First call (dev) must have TurnID=1.
	if captured[0].TurnID != 1 {
		t.Errorf("first RunRole (dev): want TurnID=1, got %d", captured[0].TurnID)
	}
	// Each subsequent call must be strictly greater.
	for i := 1; i < len(captured); i++ {
		if captured[i].TurnID <= captured[i-1].TurnID {
			t.Errorf("RunRole call %d (role=%s): TurnID=%d not > previous %d",
				i, captured[i].Role, captured[i].TurnID, captured[i-1].TurnID)
		}
	}
}

func TestRunnerTurnIDMonotonicPingPong_AC006(t *testing.T) {
	// dev(1) → reviewer-changes_requested(2) → dev-retry(3) → reviewer-approved(4)
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	var captured []agent.RoleConfig
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		captured = append(captured, cfg)
		switch cfg.Role {
		case "dev":
			// Satisfy the §10 gate; runner writes pr_opened on the first call.
			if !sendGMProjectCheck(cfg.Env) {
				t.Errorf("TestRunnerTurnIDMonotonicPingPong_AC006: sendGMProjectCheck failed")
			}
			if !sendGMDevDone(cfg.Env) {
				t.Errorf("TestRunnerTurnIDMonotonicPingPong_AC006: sendGMDevDone failed")
			}
		case "reviewer":
			if len(captured) == 2 { // first reviewer round
				writeReviewEvent(t, cfg.EventLogPath, "changes_requested", "please fix")
			} else {
				writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
			}
		}
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("expected success outcome, got %q", outcome)
	}

	if len(captured) < 4 {
		t.Fatalf("expected 4 RunRole calls (dev/reviewer/dev-retry/reviewer), got %d; roles: %v",
			len(captured), roleNames(captured))
	}

	// TurnIDs must be 1, 2, 3, 4 in strictly increasing order.
	for i, cfg := range captured {
		want := i + 1
		if cfg.TurnID != want {
			t.Errorf("call %d (role=%s): want TurnID=%d, got %d", i, cfg.Role, want, cfg.TurnID)
		}
	}
}

func roleNames(cfgs []agent.RoleConfig) []string {
	names := make([]string, len(cfgs))
	for i, c := range cfgs {
		names[i] = c.Role
	}
	return names
}

// --- AC-008: Runner-emitted repeated event types are never deduped ---

func TestRunnerWorktreeCreatedAcrossRoundsNotDeduped_AC008(t *testing.T) { //nolint:cyclop
	// dev(1) → reviewer-changes_requested(2) → dev-retry(3) → reviewer-approved(4)
	// Must see at least 2 worktree_created events with distinct turnIds.
	exec := pingPongExecutor(false, nil)
	r, logPath, _ := setupPingPongRunner(t, exec)

	devCallCount := 0
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			// Satisfy the §10 gate; runner writes pr_opened on the first call.
			if !sendGMProjectCheck(cfg.Env) {
				t.Errorf("TestRunnerWorktreeCreated: sendGMProjectCheck failed")
			}
			if !sendGMDevDone(cfg.Env) {
				t.Errorf("TestRunnerWorktreeCreated: sendGMDevDone failed")
			}
			devCallCount++
		case "reviewer":
			if devCallCount == 1 { // first reviewer round
				writeReviewEvent(t, cfg.EventLogPath, "changes_requested", "fix it")
			} else {
				writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
			}
		}
		return 0, agent.TranscriptPaths{}, nil
	})

	runOrchestrate(t, r, logPath)

	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}

	var wtEvents []eventlog.Event
	for _, ev := range events {
		if ev.Type == eventlog.EventWorktreeCreated {
			wtEvents = append(wtEvents, ev)
		}
	}

	// Must have at least 2 worktree_created events (dev + at least one reviewer).
	if len(wtEvents) < 2 {
		t.Errorf("expected >= 2 worktree_created events, got %d", len(wtEvents))
		return
	}

	// All must have a non-zero turnId.
	for i, ev := range wtEvents {
		if ev.TurnID == 0 {
			t.Errorf("worktree_created[%d] has TurnID=0, want > 0", i)
		}
	}

	// The dev and first reviewer worktree must have distinct turnIds.
	if wtEvents[0].TurnID == wtEvents[1].TurnID {
		t.Errorf("dev and reviewer worktree_created should have distinct turnIds, both have %d", wtEvents[0].TurnID)
	}
}
