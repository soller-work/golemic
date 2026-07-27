package runner

import (
	"testing"

	"golemic/internal/loop"
)

// AC-5: terminalOutcome is total over the 9 terminal steps with correct pairs.
func TestTerminalOutcome_AC5_Totality(t *testing.T) {
	cases := []struct {
		step     loop.StepKey
		outcome  string
		exitCode int
	}{
		{loop.StepTerminalSuccess, outcomeSuccess, 0},
		{loop.StepTerminalSkipped, outcomeSkipped, 0},
		{loop.StepTerminalDevFailed, outcomeDevFailed, 1},
		{loop.StepTerminalReviewFailed, outcomeReviewFailed, 1},
		{loop.StepTerminalEscalated, outcomeEscalated, 1},
		{loop.StepTerminalMergeFailed, outcomeMergeFailed, 1},
		{loop.StepTerminalTimeout, outcomeTimeout, 1},
		{loop.StepTerminalStalled, outcomeStalled, 1},
		{loop.StepTerminalAborted, outcomeAborted, 1},
	}

	covered := map[string]bool{}
	for _, tc := range cases {
		got, code := terminalOutcome(tc.step)
		if got != tc.outcome {
			t.Errorf("terminalOutcome(%s): outcome = %q, want %q", tc.step, got, tc.outcome)
		}
		if code != tc.exitCode {
			t.Errorf("terminalOutcome(%s): exitCode = %d, want %d", tc.step, code, tc.exitCode)
		}
		covered[got] = true
	}

	allOutcomes := []string{
		outcomeSuccess, outcomeSkipped, outcomeDevFailed, outcomeReviewFailed,
		outcomeEscalated, outcomeMergeFailed, outcomeTimeout, outcomeStalled, outcomeAborted,
	}
	for _, o := range allOutcomes {
		if !covered[o] {
			t.Errorf("outcome %q not covered by terminalOutcome", o)
		}
	}
}

// AC-5: terminalOutcome panics on an unknown step.
func TestTerminalOutcome_UnknownStepPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unknown step, got none")
		}
	}()
	terminalOutcome("NOT_A_TERMINAL")
}

// guardFixtures is the fixture set specified in AC-6.
var guardFixtures = func() []RunContext {
	var fixtures []RunContext
	maxRounds := 3
	for _, devAttempt := range []int{0, 2, 3, 4} {
		for _, round := range []int{0, maxRounds - 1, maxRounds} {
			for _, resume := range []bool{false, true} {
				for _, verdict := range []string{"", "approved", "changes_requested"} {
					fixtures = append(fixtures, RunContext{
						DevAttempt:    devAttempt,
						Round:         round,
						MaxRounds:     maxRounds,
						Resume:        resume,
						ResumeVerdict: verdict,
					})
				}
			}
		}
	}
	return fixtures
}()

// AC-6: for every (From, Event) group with >1 edge, exactly one guard matches per fixture.
func TestLoopTransitions_AC6_GuardDisjointness(t *testing.T) {
	transitions := loopTransitions()

	type key struct {
		from  loop.StepKey
		event loop.EventKey
	}
	groups := map[key][]loop.Transition[RunContext]{}
	for _, tr := range transitions {
		k := key{tr.From, tr.Event}
		groups[k] = append(groups[k], tr)
	}

	for k, edges := range groups {
		if len(edges) <= 1 {
			continue
		}
		for i, rc := range guardFixtures {
			rc := rc
			matches := 0
			for _, e := range edges {
				if e.Guard == nil || e.Guard(&rc) {
					matches++
				}
			}
			if matches != 1 {
				t.Errorf("(%s, %s) fixture[%d] %+v: %d edges matched, want exactly 1",
					k.from, k.event, i, rc, matches)
			}
		}
	}
}

// AC-7: every non-terminal To has outgoing transitions; every From is non-terminal.
func TestLoopTransitions_AC7_StructuralSanity(t *testing.T) {
	terminals := loopTerminals()
	transitions := loopTransitions()

	outgoing := map[loop.StepKey]bool{}
	for _, tr := range transitions {
		outgoing[tr.From] = true
	}

	for _, tr := range transitions {
		if terminals[tr.From] {
			t.Errorf("transition leaves terminal step %s (From must be non-terminal)", tr.From)
		}
		if !terminals[tr.To] && !outgoing[tr.To] {
			t.Errorf("non-terminal To %s has no outgoing transitions", tr.To)
		}
	}
}
