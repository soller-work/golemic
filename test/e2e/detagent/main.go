// Command detagent is the deterministic fake pi binary used by E2E tests.
// It is built as an executable named "pi" and placed first on PATH so golemic's
// newPiCmd resolves it instead of the real Claude Code CLI.
//
// Invocation (from golemic's buildPiArgs):
//
//	pi -p --mode json --session-id <id> --append-system-prompt @<file>
//	   --tools <csv> --model <model> <userPrompt>
//
// Required env vars (set by golemic's runner):
//
//	GOLEMIC_GM_SOCK       — unix socket for the gm_ broker
//	GOLEMIC_RUN_ID        — current run ID
//	GOLEMIC_INVOCATION_ID — current invocation ID (contains role and round)
//	GOLEMIC_ROLE          — "dev" or "reviewer"
//
// The scenario is read from the issue spec via gm_slice_get by looking for the
// line "E2E-SCENARIO: <name>" in the spec body.
package main

import (
	"fmt"
	"os"

	"golemic/test/e2e/detbroker"
	"golemic/test/e2e/scenario"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("detagent 1.0.0 (golemic e2e fake pi)")
		os.Exit(0)
	}

	role := os.Getenv("GOLEMIC_ROLE")
	var err error
	switch role {
	case "dev":
		err = runDev()
	case "reviewer":
		err = runReviewer()
	default:
		fmt.Fprintf(os.Stderr, "detagent: unknown role %q\n", role)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "detagent %s: %v\n", role, err)
		os.Exit(1)
	}
}

func runDev() error {
	c, inv, sc, err := setupInvocation("dev")
	if err != nil {
		return err
	}
	if sc.Dev == nil {
		return fmt.Errorf("scenario %q has no Dev function", sc.Name)
	}
	return sc.Dev(c, inv)
}

func runReviewer() error {
	c, inv, sc, err := setupInvocation("reviewer")
	if err != nil {
		return err
	}
	// gm_pr_view before the verdict is cross-cutting: every reviewer does it.
	if err := c.PRView(); err != nil {
		return fmt.Errorf("gm_pr_view: %w", err)
	}
	if sc.Reviewer == nil {
		return fmt.Errorf("scenario %q has no Reviewer function", sc.Name)
	}
	return sc.Reviewer(c, inv)
}

// setupInvocation creates the broker client, fetches the scenario name, parses
// the invocation context, and looks up the registered scenario.
func setupInvocation(role string) (*detbroker.Client, scenario.Invocation, scenario.Scenario, error) {
	c := detbroker.NewFromEnv()
	if c == nil {
		return nil, scenario.Invocation{}, scenario.Scenario{}, fmt.Errorf("GOLEMIC_GM_SOCK not set")
	}

	name, err := c.FetchScenario()
	if err != nil {
		return nil, scenario.Invocation{}, scenario.Scenario{}, err
	}

	inv := scenario.Invocation{
		Round:   detbroker.RoundFromEnv(),
		Attempt: detbroker.AttemptFromEnv(),
	}

	sc, ok := scenario.Lookup(name)
	if !ok {
		return nil, scenario.Invocation{}, scenario.Scenario{}, fmt.Errorf("unknown scenario %q", name)
	}

	fmt.Fprintf(os.Stderr, "detagent %s: scenario=%s round=%d\n", role, name, inv.Round)
	return c, inv, sc, nil
}
