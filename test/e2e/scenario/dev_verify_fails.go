package scenario

import (
	"fmt"
	"os"

	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "dev_verify_fails",
		Terminal: loop.StepTerminalDevFailed,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			if err := os.WriteFile("VERIFY_BREAK", []byte("detagent: verify break sentinel\n"), 0644); err != nil {
				return fmt.Errorf("write VERIFY_BREAK: %w", err)
			}
			if err := detbroker.GitInCWD("add", "VERIFY_BREAK"); err != nil {
				return fmt.Errorf("git add VERIFY_BREAK: %w", err)
			}

			ok, err := c.RunProjectCheck()
			if err != nil {
				fmt.Fprintf(os.Stderr, "detagent dev_verify_fails: gm_project_check error: %v\n", err)
				return err
			}
			if ok {
				fmt.Fprintln(os.Stderr, "detagent dev_verify_fails: verify passed despite VERIFY_BREAK; sandbox needs verify.sh that checks for VERIFY_BREAK")
			} else {
				fmt.Fprintln(os.Stderr, "detagent dev_verify_fails: verify correctly failed due to VERIFY_BREAK")
			}
			return fmt.Errorf("dev_verify_fails: exiting with error to trigger dev_failed")
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return c.Approve("E2E detagent: LGTM — changes look good.")
		},
		Expect: Expect{
			Outcome:  OutcomeDevFailed,
			ExitZero: false,
		},
	})
}
