// Package claim provides race-safe exclusive locking for GitHub issues.
package claim

import (
	"encoding/json"
	"fmt"
	"strconv"

	"golemic/internal/preflight"
)

// Outcome is the result code of a Claim attempt.
type Outcome int

const (
	OutcomeOK          Outcome = iota // claim succeeded; caller should write issue_claimed event
	OutcomeIdempotent                 // already owned; no edit or event needed
	OutcomeRaceLost                   // another runner won; own edits have been rolled back
	OutcomeNotTakeable                // issue lacks ready-for-agent and is not already owned
)

// Result is returned by Claim on non-error outcomes.
type Result struct {
	Outcome Outcome
	Details string // human-readable; populated on RaceLost and NotTakeable
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghAssignee struct {
	Login string `json:"login"`
}

type ghIssueView struct {
	Labels    []ghLabel    `json:"labels"`
	Assignees []ghAssignee `json:"assignees"`
}

type ghUser struct {
	Login string `json:"login"`
}

// Claim attempts to acquire an exclusive lock on GitHub issue issueNum.
// devToken is injected as GH_TOKEN for all gh calls (BR-005).
func Claim(executor preflight.Executor, issueNum int, devToken string) (Result, error) { //nolint:cyclop
	env := map[string]string{"GH_TOKEN": devToken}
	issueArg := strconv.Itoa(issueNum)

	devLogin, err := resolveLogin(executor, env)
	if err != nil {
		return Result{}, err
	}

	preState, err := viewIssue(executor, env, issueArg)
	if err != nil {
		return Result{}, fmt.Errorf("gh issue view: %w", err)
	}

	labels := labelSet(preState)
	hasReady := labels.has("ready-for-agent")
	hasInProgress := labels.has("in-progress")
	soleOwner := isSoleOwner(preState, devLogin)

	// BR-002: idempotent re-claim.
	if hasInProgress && !hasReady && soleOwner {
		return Result{Outcome: OutcomeIdempotent}, nil
	}

	// BR-003: not takeable.
	if !hasReady {
		return Result{
			Outcome: OutcomeNotTakeable,
			Details: fmt.Sprintf("issue #%d is not takeable", issueNum),
		}, nil
	}

	// BR-001: edit — remove ready-for-agent, add in-progress, set dev-bot as assignee.
	_, err = executor.RunWithEnv(env, "gh", "issue", "edit", issueArg,
		"--remove-label", "ready-for-agent",
		"--add-label", "in-progress",
		"--add-assignee", devLogin,
	)
	if err != nil {
		return Result{}, fmt.Errorf("gh issue edit: %w", err)
	}

	// Post-verify: re-read and confirm own-and-only ownership.
	postState, err := viewIssue(executor, env, issueArg)
	if err != nil {
		return Result{}, fmt.Errorf("gh issue view (verify): %w", err)
	}

	postLabels := labelSet(postState)
	ownedOK := postLabels.has("in-progress") && !postLabels.has("ready-for-agent") && isSoleOwner(postState, devLogin)

	if !ownedOK {
		details := fmt.Sprintf("claim conflict on issue #%d: in-progress=%v ready-for-agent=%v sole-assignee=%v",
			issueNum, postLabels.has("in-progress"), postLabels.has("ready-for-agent"), isSoleOwner(postState, devLogin))

		_, rollErr := executor.RunWithEnv(env, "gh", "issue", "edit", issueArg,
			"--remove-label", "in-progress",
			"--add-label", "ready-for-agent",
			"--remove-assignee", devLogin,
		)
		if rollErr != nil {
			details += "; rollback error: " + rollErr.Error()
		}
		return Result{Outcome: OutcomeRaceLost, Details: details}, nil
	}

	return Result{Outcome: OutcomeOK}, nil
}

func resolveLogin(executor preflight.Executor, env map[string]string) (string, error) {
	out, err := executor.RunWithEnv(env, "gh", "api", "user")
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	var u ghUser
	if err := json.Unmarshal([]byte(out), &u); err != nil {
		return "", fmt.Errorf("gh api user: invalid response: %w", err)
	}
	if u.Login == "" {
		return "", fmt.Errorf("gh api user: empty login")
	}
	return u.Login, nil
}

func viewIssue(executor preflight.Executor, env map[string]string, issueArg string) (*ghIssueView, error) {
	out, err := executor.RunWithEnv(env, "gh", "issue", "view", issueArg, "--json", "labels,assignees")
	if err != nil {
		return nil, err
	}
	var state ghIssueView
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &state, nil
}

type labelMap map[string]struct{}

func (lm labelMap) has(name string) bool {
	_, ok := lm[name]
	return ok
}

func labelSet(state *ghIssueView) labelMap {
	m := make(labelMap, len(state.Labels))
	for _, l := range state.Labels {
		m[l.Name] = struct{}{}
	}
	return m
}

func isSoleOwner(state *ghIssueView, login string) bool {
	return len(state.Assignees) == 1 && state.Assignees[0].Login == login
}
