package scenario

import (
	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "happy_path",
		Terminal: loop.StepTerminalSuccess,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			return c.WriteCommitAndDone(
				"e2e_happy_path.txt",
				"E2E happy path marker\n",
				"feat: add e2e happy path marker (detagent)",
				"E2E happy path test",
				"Automated E2E happy path test driven by detagent.",
			)
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return c.Approve("E2E detagent: LGTM — changes look good.")
		},
		Expect: Expect{
			Outcome:       OutcomeSuccess,
			ExitZero:      true,
			RequireEvents: []string{"pr_opened", "review_submitted", "pr_merged"},
		},
	})
}
