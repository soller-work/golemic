package scenario

import (
	"fmt"
	"os"
	"time"

	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "timeout",
		Terminal: loop.StepTerminalTimeout,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			fmt.Fprintln(os.Stderr, "detagent dev: sleeping to trigger timeout")
			time.Sleep(24 * time.Hour)
			return nil
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			return c.Approve("E2E detagent: LGTM — changes look good.")
		},
		TimeoutSec: 120,
		Env: []string{
			"GOLEMIC_AGENT_IDLE_TIMEOUT_SEC=20",
			"GOLEMIC_AGENT_MAX_STALL_RETRIES=0",
		},
		Expect: Expect{
			AllowedOutcomes: []Outcome{OutcomeTimeout, OutcomeStalled},
			ExitZero:        false,
		},
	})
}
