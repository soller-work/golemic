package scenario

import (
	"fmt"
	"os"

	"golemic/internal/loop"
	"golemic/test/e2e/detbroker"
)

func init() {
	Register(Scenario{
		Name:     "reviewer_rejects_once",
		Terminal: loop.StepTerminalSuccess,
		Dev: func(c *detbroker.Client, inv Invocation) error {
			content := fmt.Sprintf("E2E reviewer_rejects_once marker — round %d\n", inv.Round)
			commitMsg := fmt.Sprintf("feat: e2e reviewer_rejects_once round %d (detagent)", inv.Round)
			return c.WriteCommitAndDone(
				"e2e_review_reject.txt",
				content,
				commitMsg,
				"E2E reviewer_rejects_once test",
				"Automated E2E reviewer_rejects_once test driven by detagent.",
			)
		},
		Reviewer: func(c *detbroker.Client, inv Invocation) error {
			if inv.Round == 1 {
				if err := c.SubmitComment(
					"e2e_review_reject.txt", 1,
					"E2E detagent blocking comment: please fix before approval.",
					"blocking",
				); err != nil {
					fmt.Fprintf(os.Stderr, "detagent reviewer: gm_review_submit_comment: %v (continuing)\n", err)
				}
				return c.RequestChanges("E2E detagent: changes requested — see blocking comment.")
			}
			return c.Approve("E2E detagent: LGTM — changes look good.")
		},
		Expect: Expect{
			Outcome:       OutcomeSuccess,
			ExitZero:      true,
			RequireEvents: []string{"pr_merged"},
			MinEventCount: map[string]int{"review_submitted": 2},
		},
	})
}
