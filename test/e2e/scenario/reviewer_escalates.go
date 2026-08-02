package scenario

import (
	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "reviewer_escalates",
		Terminal: loop.StepTerminalEscalated,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			return c.WriteCommitAndDone(
				"e2e_reviewer_escalates.txt",
				"E2E reviewer_escalates marker\n",
				"feat: add e2e reviewer_escalates marker (detagent)",
				"E2E reviewer_escalates test",
				"Automated E2E reviewer_escalates test driven by detagent.",
			)
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return c.RequestChanges("E2E detagent: changes requested — escalation path test.")
		},
		Expect: Expect{
			Outcome:       OutcomeEscalated,
			ExitZero:      false,
			RequireEvents: []string{"review_submitted"},
			ForbidEvents:  []string{"pr_merged"},
		},
	})
}
