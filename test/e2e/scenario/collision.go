package scenario

import (
	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "collision",
		Terminal: loop.StepTerminalAborted,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			// golemic detects the collision during PREPARE and exits before the
			// dev agent is invoked. This function exists for completeness.
			return c.WriteCommitAndDone(
				"e2e_collision.txt",
				"E2E collision marker\n",
				"feat: add e2e collision marker (detagent)",
				"E2E collision test",
				"Automated E2E collision test driven by detagent.",
			)
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return c.Approve("E2E detagent: LGTM — changes look good.")
		},
		Setup: func(sb Sandbox, issueNum int) (cleanup func()) {
			_, cleanup = sb.CreateCollisionPR(issueNum)
			return cleanup
		},
		NoClean: true,
		Expect: Expect{
			Outcome:      OutcomeAborted,
			ExitZero:     false,
			ForbidEvents: []string{"pr_opened"},
		},
	})
}
