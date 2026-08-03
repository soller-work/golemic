package scenario

import (
	"fmt"
	"os"

	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "gate_recovers",
		Terminal: loop.StepTerminalSuccess,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			if inv.Attempt == 0 {
				if err := os.WriteFile("e2e_gate_recovers.txt", []byte("E2E gate_recovers marker\n"), 0644); err != nil {
					return fmt.Errorf("write file: %w", err)
				}
				if err := detbroker.GitInCWD("add", "e2e_gate_recovers.txt"); err != nil {
					return err
				}
				// Call DevDone without a prior gm_project_check to trip the section 10 gate.
				return c.DevDone(
					"E2E gate_recovers: initial attempt (no check)",
					"feat: add e2e gate_recovers marker (detagent)",
					"E2E gate_recovers test",
					"Automated E2E gate_recovers test driven by detagent.\n\nCloses #0",
				)
			}
			return c.WriteCommitAndDone(
				"e2e_gate_recovers.txt",
				"E2E gate_recovers marker — recovered\n",
				"feat: add e2e gate_recovers marker (detagent)",
				"E2E gate_recovers test",
				"Automated E2E gate_recovers test driven by detagent.\n\nCloses #0",
			)
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return c.Approve("E2E detagent: LGTM — gate_recovers changes look good.")
		},
		Expect: Expect{
			Outcome:       OutcomeSuccess,
			ExitZero:      true,
			RequireEvents: []string{"pr_opened", "review_submitted", "pr_merged"},
		},
	})
}
