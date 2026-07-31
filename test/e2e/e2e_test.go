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
	"golemic/test/e2e/scenario"
)

// harnessAdapter adapts *harness.Harness to the scenario.Sandbox interface.
type harnessAdapter struct {
	h *harness.Harness
	t *testing.T
}

func (a *harnessAdapter) CreateCollisionPR(issueNum int) (int, func()) {
	a.t.Helper()
	return a.h.CreateCollisionPR(a.t, issueNum)
}

// TestE2EScenarios is the single table-driven runner for all registered E2E scenarios.
func TestE2EScenarios(t *testing.T) {
	h := harness.New(t)
	if h == nil {
		return
	}

	for _, sc := range scenario.All() {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			runScenario(t, h, sc)
		})
	}
}

func runScenario(t *testing.T, h *harness.Harness, sc scenario.Scenario) {
	t.Helper()

	issueNum, err := h.CreateIssue(sc.Name)
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

	if sc.Setup != nil {
		sb := &harnessAdapter{h: h, t: t}
		cleanup := sc.Setup(sb, issueNum)
		t.Cleanup(cleanup)
	}

	result := h.RunWithTimeout(t, issueNum, sc.TimeoutSec, sc.NoClean, sc.Env...)
	t.Logf("golemic stdout:\n%s", result.Stdout)
	t.Logf("golemic stderr:\n%s", result.Stderr)

	eventsPath := h.LatestRunEventsPath()
	assertExpect(t, sc.Expect, result, eventsPath)
}

func assertExpect(t *testing.T, ex scenario.Expect, result *harness.RunResult, eventsPath string) {
	t.Helper()

	t.Run("ExitCode", func(t *testing.T) {
		if ex.ExitZero && result.ExitCode != 0 {
			t.Errorf("want exit 0, got %d", result.ExitCode)
		}
		if !ex.ExitZero && result.ExitCode == 0 {
			t.Error("want non-zero exit, got 0")
		}
	})

	if ex.Outcome != "" || len(ex.AllowedOutcomes) > 0 {
		t.Run("Outcome", func(t *testing.T) {
			got := harness.RunFinishedOutcome(eventsPath)
			if len(ex.AllowedOutcomes) > 0 {
				for _, o := range ex.AllowedOutcomes {
					if strings.EqualFold(got, string(o)) {
						return
					}
				}
				t.Errorf("run_finished outcome: got %q, want one of %v", got, ex.AllowedOutcomes)
			} else {
				if !strings.EqualFold(got, string(ex.Outcome)) {
					t.Errorf("run_finished outcome: got %q, want %q", got, ex.Outcome)
				}
			}
		})
	}

	for _, evType := range ex.RequireEvents {
		evType := evType
		t.Run("RequireEvent/"+evType, func(t *testing.T) {
			if !harness.HasEvent(eventsPath, evType) {
				t.Errorf("%s event not found in events.jsonl", evType)
			}
		})
	}

	for _, evType := range ex.ForbidEvents {
		evType := evType
		t.Run("ForbidEvent/"+evType, func(t *testing.T) {
			if harness.HasEvent(eventsPath, evType) {
				t.Errorf("%s event must not appear in events.jsonl", evType)
			}
		})
	}

	for evType, minCount := range ex.MinEventCount {
		evType, minCount := evType, minCount
		t.Run("MinCount/"+evType, func(t *testing.T) {
			count := harness.CountEvents(eventsPath, evType)
			if count < minCount {
				t.Errorf("want at least %d %s events, got %d", minCount, evType, count)
			}
		})
	}

	for _, sub := range ex.StderrContains {
		sub := sub
		t.Run("Stderr/"+sub, func(t *testing.T) {
			if !strings.Contains(result.Stderr, sub) {
				t.Errorf("stderr does not contain %q", sub)
			}
		})
	}
}
