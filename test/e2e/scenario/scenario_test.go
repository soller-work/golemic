package scenario_test

import (
	"strings"
	"testing"

	"golemic/internal/loop"
	"golemic/test/e2e/scenario"
)

func TestScenarioOutcomesAreValid(t *testing.T) {
	for _, sc := range scenario.All() {
		if len(sc.Expect.AllowedOutcomes) > 0 {
			for _, o := range sc.Expect.AllowedOutcomes {
				if err := scenario.ValidateOutcome(o); err != nil {
					t.Errorf("scenario %q: AllowedOutcomes: %v", sc.Name, err)
				}
			}
		} else if sc.Expect.Outcome != "" {
			if err := scenario.ValidateOutcome(sc.Expect.Outcome); err != nil {
				t.Errorf("scenario %q: Outcome: %v", sc.Name, err)
			}
		}
	}
}

func TestScenarioNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range scenario.All() {
		if seen[sc.Name] {
			t.Errorf("duplicate scenario name: %q", sc.Name)
		}
		seen[sc.Name] = true
	}
}

func TestScenarioTerminalCoverage(t *testing.T) {
	covered := map[loop.StepKey]bool{}
	for _, sc := range scenario.All() {
		if sc.Terminal != "" {
			covered[sc.Terminal] = true
		}
	}

	var uncovered []string
	for _, step := range loop.AllSteps() {
		if strings.HasPrefix(string(step), "TERMINAL_") && !covered[step] {
			uncovered = append(uncovered, string(step))
		}
	}
	if len(uncovered) > 0 {
		t.Logf("terminal steps not yet covered by any scenario: %v", uncovered)
	}
}
