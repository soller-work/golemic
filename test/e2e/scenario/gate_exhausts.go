package scenario

import (
	"fmt"
	"os"

	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "gate_exhausts",
		Terminal: loop.StepTerminalDevFailed,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			if err := os.WriteFile("e2e_gate_exhausts.txt", []byte("E2E gate_exhausts marker\n"), 0644); err != nil {
				return fmt.Errorf("write file: %w", err)
			}
			if err := detbroker.GitInCWD("add", "e2e_gate_exhausts.txt"); err != nil {
				return err
			}
			return c.DevDone(
				"E2E gate_exhausts: attempt without check",
				"feat: add e2e gate_exhausts marker (detagent)",
				"E2E gate_exhausts test",
				"Automated E2E gate_exhausts test driven by detagent.",
			)
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return c.Approve("E2E detagent: LGTM — gate_exhausts changes look good.")
		},
		Expect: Expect{
			Outcome:        OutcomeDevFailed,
			ExitZero:       false,
			StderrContains: []string{"after 3 invocations"},
		},
	})
}
