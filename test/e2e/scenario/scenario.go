// Package scenario holds the E2E scenario registry and shared types.
// Each scenario lives in its own file and self-registers via init().
// This package has no build tags so both the detagent binary and the e2e
// test suite can import it.
package scenario

import (
	"fmt"

	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

// Outcome mirrors the nine terminal outcome strings from internal/runner/outcome.go.
type Outcome string

const (
	OutcomeSuccess      Outcome = "success"
	OutcomeDevFailed    Outcome = "dev_failed"
	OutcomeReviewFailed Outcome = "review_failed"
	OutcomeEscalated    Outcome = "escalated"
	OutcomeMergeFailed  Outcome = "merge_failed"
	OutcomeTimeout      Outcome = "timeout"
	OutcomeStalled      Outcome = "stalled"
	OutcomeAborted      Outcome = "aborted"
	OutcomeSkipped      Outcome = "skipped"
)

// AllOutcomes returns every valid terminal outcome value.
func AllOutcomes() []Outcome {
	return []Outcome{
		OutcomeSuccess, OutcomeDevFailed, OutcomeReviewFailed,
		OutcomeEscalated, OutcomeMergeFailed, OutcomeTimeout,
		OutcomeStalled, OutcomeAborted, OutcomeSkipped,
	}
}

// Invocation is the per-invocation context passed to Dev and Reviewer functions.
// New per-invocation state must be added as a field here, never as a new
// positional parameter, so existing scenario function signatures never change.
type Invocation struct {
	Round   int
	Attempt int
}

// Sandbox exposes harness capabilities needed by scenario Setup functions.
type Sandbox interface {
	// CreateCollisionPR creates an open PR for the issue's branch before golemic
	// runs, triggering collision detection. Returns the PR number and a cleanup
	// function that closes the PR and removes the branch.
	CreateCollisionPR(issueNum int) (prNum int, cleanup func())
}

// Expect holds declarative assertions that the test runner interprets generically.
// All fields are optional; zero values mean "do not assert".
type Expect struct {
	Outcome         Outcome   // exact expected outcome; ignored when AllowedOutcomes is set
	AllowedOutcomes []Outcome // when set, outcome must be one of these values
	ExitZero        bool
	RequireEvents   []string
	ForbidEvents    []string
	MinEventCount   map[string]int
	StderrContains  []string
}

// Scenario is the complete self-contained description of one E2E scenario.
// Always use keyed struct literals when calling Register so that new optional
// fields have zero-value defaults without requiring edits to existing files.
type Scenario struct {
	Name     string
	Terminal loop.StepKey
	Dev      func(*detbroker.Client, Invocation) error
	Reviewer func(*detbroker.Client, Invocation) error
	// Setup runs after issue creation and before golemic starts.
	// It receives the Sandbox and the issue number; returns a cleanup function.
	Setup      func(sb Sandbox, issueNum int) (cleanup func())
	Env        []string // extra env vars passed to golemic (format: "KEY=VALUE")
	TimeoutSec int      // wall-clock timeout for h.RunWithTimeout; 0 = 30 min default
	NoClean    bool     // skip --clean flag (required for collision detection)
	Expect     Expect
}

var registry []Scenario

// Register adds a scenario to the global registry. Called from init().
func Register(s Scenario) {
	registry = append(registry, s)
}

// Lookup returns the scenario with the given name and true, or (zero, false)
// if no scenario with that name is registered.
func Lookup(name string) (Scenario, bool) {
	for _, s := range registry {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}

// All returns all registered scenarios in registration order.
func All() []Scenario {
	out := make([]Scenario, len(registry))
	copy(out, registry)
	return out
}

// ValidateOutcome returns an error if o is not one of the nine terminal outcomes.
func ValidateOutcome(o Outcome) error {
	for _, valid := range AllOutcomes() {
		if o == valid {
			return nil
		}
	}
	return fmt.Errorf("scenario: %q is not a valid terminal outcome", o)
}
