package scenario

import (
	"fmt"

	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "review_agent_fails",
		Terminal: loop.StepTerminalReviewFailed,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			return c.WriteCommitAndDone(
				"e2e_review_agent_fails.txt",
				"E2E review_agent_fails marker\n",
				"feat: add e2e review_agent_fails marker (detagent)",
				"E2E review_agent_fails test",
				"Automated E2E review_agent_fails test driven by detagent.",
			)
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return fmt.Errorf("review_agent_fails: simulated reviewer crash")
		},
		Expect: Expect{
			Outcome:       OutcomeReviewFailed,
			ExitZero:      false,
			RequireEvents: []string{"pr_opened"},
			ForbidEvents:  []string{"pr_merged"},
		},
	})
}
