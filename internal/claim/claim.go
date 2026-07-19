// Package claim implements the race-safe exclusive lock on a GitHub issue
// for the autonomous golemic runner (BR-001 through BR-003).
package claim

import (
	"encoding/json"
	"fmt"
	"strconv"

	"golemic/internal/preflight"
)

// Result is the outcome of a Claim call. It is meaningful only when the
// returned error is nil, except ResultRaceLost which may carry a non-nil error
// containing rollback failure details.
type Result int

const (
	ResultOK          Result = iota // edit + post-verify succeeded; caller must write issue_claimed event
	ResultIdempotent                // issue already owned by devLogin; no edit, no event needed
	ResultRaceLost                  // post-verify showed foreign ownership; rollback attempted
	ResultNotTakeable               // ready-for-agent label absent at pre-read time
	ResultError                     // gh/parse failure; error carries details
)

// ReleaseResult is the outcome of a Release call.
type ReleaseResult int

const (
	ReleaseResultOK           ReleaseResult = iota // edit succeeded; caller must write issue_released event
	ReleaseResultIdempotent                        // issue already released; no edit, no event needed
	ReleaseResultForeignClaim                      // in-progress owned by a different assignee
	ReleaseResultError                             // gh/parse failure
)

type issueView struct {
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
}

func (v *issueView) hasLabel(name string) bool {
	for _, l := range v.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

func (v *issueView) isSoleAssignee(login string) bool {
	return len(v.Assignees) == 1 && v.Assignees[0].Login == login
}

func (v *issueView) hasAssignee(login string) bool {
	for _, a := range v.Assignees {
		if a.Login == login {
			return true
		}
	}
	return false
}

func (v *issueView) assigneeLogins() []string {
	logins := make([]string, len(v.Assignees))
	for i, a := range v.Assignees {
		logins[i] = a.Login
	}
	return logins
}

func viewIssue(executor preflight.Executor, devToken string, number int) (*issueView, error) {
	out, err := executor.RunWithEnv(
		map[string]string{"GH_TOKEN": devToken},
		"gh", "issue", "view", strconv.Itoa(number), "--json", "labels,assignees",
	)
	if err != nil {
		return nil, fmt.Errorf("gh issue view %d: %w", number, err)
	}
	var v issueView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, fmt.Errorf("parse issue view %d: %w", number, err)
	}
	return &v, nil
}

// Release removes the exclusive lock on GitHub issue number.
//
// Idempotent if in-progress label is absent (BR-002). Exits with
// ReleaseResultForeignClaim if in-progress is present but the dev-bot is not
// an assignee (BR-003). On success: removes in-progress, clears assignees, and
// applies reason-specific labels per DT-001 (done=none, failed=needs-human,
// abandoned=ready-for-agent).
func Release(executor preflight.Executor, number int, devLogin, devToken, reason string) (ReleaseResult, error) {
	ghEnv := map[string]string{"GH_TOKEN": devToken}
	num := strconv.Itoa(number)

	pre, err := viewIssue(executor, devToken, number)
	if err != nil {
		return ReleaseResultError, err
	}

	// BR-002: idempotent — in-progress absent means already released.
	if !pre.hasLabel("in-progress") {
		return ReleaseResultIdempotent, nil
	}

	// BR-003: foreign ownership — in-progress present but dev-bot not an assignee.
	if !pre.hasAssignee(devLogin) {
		return ReleaseResultForeignClaim, fmt.Errorf("issue #%d is claimed by %v, not %s",
			number, pre.assigneeLogins(), devLogin)
	}

	editArgs := []string{"issue", "edit", num,
		"--remove-label", "in-progress",
		"--remove-assignee", "@me",
	}
	switch reason {
	case "failed":
		editArgs = append(editArgs, "--add-label", "needs-human")
	case "abandoned":
		editArgs = append(editArgs, "--add-label", "ready-for-agent")
	}

	if _, err := executor.RunWithEnv(ghEnv, "gh", editArgs...); err != nil {
		return ReleaseResultError, fmt.Errorf("gh issue edit %d: %w", number, err)
	}
	return ReleaseResultOK, nil
}

// Claim acquires an exclusive lock on GitHub issue number.
//
// Pre-read checks idempotency (ResultIdempotent) and takeability (ResultNotTakeable).
// On takeable: removes ready-for-agent, adds in-progress, sets sole assignee to
// devLogin via @me (GH_TOKEN = devToken). Post-verify confirms own-and-only
// ownership; on mismatch, rollback is attempted and ResultRaceLost is returned.
func Claim(executor preflight.Executor, number int, devLogin, devToken string) (Result, error) { //nolint:cyclop
	ghEnv := map[string]string{"GH_TOKEN": devToken}
	num := strconv.Itoa(number)

	pre, err := viewIssue(executor, devToken, number)
	if err != nil {
		return ResultError, err
	}

	// BR-002: idempotent — issue already in claimed state for this bot.
	if pre.hasLabel("in-progress") && !pre.hasLabel("ready-for-agent") && pre.isSoleAssignee(devLogin) {
		return ResultIdempotent, nil
	}

	// BR-003: not takeable — ready-for-agent label absent and not already owned.
	if !pre.hasLabel("ready-for-agent") {
		return ResultNotTakeable, nil
	}

	// Edit: swap labels, claim assignee.
	if _, err := executor.RunWithEnv(ghEnv, "gh", "issue", "edit", num,
		"--remove-label", "ready-for-agent",
		"--add-label", "in-progress",
		"--add-assignee", "@me"); err != nil {
		return ResultError, fmt.Errorf("gh issue edit %d: %w", number, err)
	}

	// Post-verify (BR-001).
	post, err := viewIssue(executor, devToken, number)
	if err != nil {
		return ResultError, fmt.Errorf("post-verify: %w", err)
	}

	if post.hasLabel("in-progress") && !post.hasLabel("ready-for-agent") && post.isSoleAssignee(devLogin) {
		return ResultOK, nil
	}

	// Race lost — roll back own edits.
	verifyDetail := fmt.Sprintf("assignees=%v in-progress=%v ready-for-agent=%v",
		post.assigneeLogins(), post.hasLabel("in-progress"), post.hasLabel("ready-for-agent"))

	_, rbErr := executor.RunWithEnv(ghEnv, "gh", "issue", "edit", num,
		"--add-label", "ready-for-agent",
		"--remove-label", "in-progress",
		"--remove-assignee", "@me")
	if rbErr != nil {
		return ResultRaceLost, fmt.Errorf("%s; rollback failed: %w", verifyDetail, rbErr)
	}
	return ResultRaceLost, fmt.Errorf("%s", verifyDetail)
}
