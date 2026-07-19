package claim

import (
	"fmt"
	"strings"
	"testing"

	"golemic/internal/preflight"
)

// seqExecutor is a test executor that returns responses in order.
type seqExecutor struct {
	t         *testing.T
	responses []seqResponse
	idx       int
	calls     []seqCall
}

type seqResponse struct {
	out string
	err error
}

type seqCall struct {
	env  map[string]string
	name string
	args []string
}

func (s *seqExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	s.calls = append(s.calls, seqCall{env: env, name: name, args: args})
	if s.idx >= len(s.responses) {
		s.t.Fatalf("unexpected call %d: %s %v (only %d responses defined)", s.idx, name, args, len(s.responses))
	}
	resp := s.responses[s.idx]
	s.idx++
	return resp.out, resp.err
}

func (s *seqExecutor) Run(name string, args ...string) (string, error) {
	return s.RunWithEnv(nil, name, args...)
}

func (s *seqExecutor) RunInDir(_ string, name string, args ...string) (string, error) {
	return s.Run(name, args...)
}

func (s *seqExecutor) RunWithEnvInDir(env map[string]string, _ string, name string, args ...string) (string, error) {
	return s.RunWithEnv(env, name, args...)
}

var _ preflight.Executor = (*seqExecutor)(nil)

const (
	devToken = "ghp_dev_test"
	devLogin = "dev-bot"
)

func userResp() seqResponse {
	return seqResponse{out: `{"login":"dev-bot"}`}
}

func issueResp(labels []string, assignees []string) seqResponse {
	labelJSON := "["
	for i, l := range labels {
		if i > 0 {
			labelJSON += ","
		}
		labelJSON += `{"name":"` + l + `"}`
	}
	labelJSON += "]"

	assigneeJSON := "["
	for i, a := range assignees {
		if i > 0 {
			assigneeJSON += ","
		}
		assigneeJSON += `{"login":"` + a + `"}`
	}
	assigneeJSON += "]"

	return seqResponse{out: fmt.Sprintf(`{"labels":%s,"assignees":%s}`, labelJSON, assigneeJSON)}
}

func editResp() seqResponse {
	return seqResponse{out: ""}
}

func TestClaim_HappyPath(t *testing.T) {
	exec := &seqExecutor{
		t: t,
		responses: []seqResponse{
			userResp(),
			issueResp([]string{"ready-for-agent"}, nil),
			editResp(),
			issueResp([]string{"in-progress"}, []string{devLogin}),
		},
	}

	result, err := Claim(exec, 42, devToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != OutcomeOK {
		t.Errorf("outcome = %v; want OutcomeOK", result.Outcome)
	}
	if exec.idx != 4 {
		t.Errorf("expected 4 calls; got %d", exec.idx)
	}
	// Verify GH_TOKEN injection on all calls.
	for i, call := range exec.calls {
		if call.env["GH_TOKEN"] != devToken {
			t.Errorf("call %d missing GH_TOKEN", i)
		}
	}
	// Verify edit call removes ready-for-agent and adds in-progress.
	editCall := exec.calls[2]
	editArgs := strings.Join(editCall.args, " ")
	if !strings.Contains(editArgs, "--remove-label ready-for-agent") {
		t.Errorf("edit call missing --remove-label ready-for-agent: %s", editArgs)
	}
	if !strings.Contains(editArgs, "--add-label in-progress") {
		t.Errorf("edit call missing --add-label in-progress: %s", editArgs)
	}
	if !strings.Contains(editArgs, "--add-assignee dev-bot") {
		t.Errorf("edit call missing --add-assignee dev-bot: %s", editArgs)
	}
}

func TestClaim_Idempotent(t *testing.T) {
	exec := &seqExecutor{
		t: t,
		responses: []seqResponse{
			userResp(),
			issueResp([]string{"in-progress"}, []string{devLogin}),
		},
	}

	result, err := Claim(exec, 42, devToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != OutcomeIdempotent {
		t.Errorf("outcome = %v; want OutcomeIdempotent", result.Outcome)
	}
	if exec.idx != 2 {
		t.Errorf("expected 2 calls (user + pre-read only); got %d", exec.idx)
	}
}

func TestClaim_RaceLost(t *testing.T) {
	exec := &seqExecutor{
		t: t,
		responses: []seqResponse{
			userResp(),
			issueResp([]string{"ready-for-agent"}, nil),
			editResp(),
			// Post-verify: foreign assignee won the race.
			issueResp([]string{"in-progress"}, []string{"other-bot"}),
			editResp(), // rollback
		},
	}

	result, err := Claim(exec, 42, devToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != OutcomeRaceLost {
		t.Errorf("outcome = %v; want OutcomeRaceLost", result.Outcome)
	}
	if !strings.Contains(result.Details, "claim conflict on issue #42") {
		t.Errorf("details missing conflict message: %s", result.Details)
	}
	if exec.idx != 5 {
		t.Errorf("expected 5 calls; got %d", exec.idx)
	}
	// Verify rollback call adds back ready-for-agent.
	rollbackCall := exec.calls[4]
	rollbackArgs := strings.Join(rollbackCall.args, " ")
	if !strings.Contains(rollbackArgs, "--add-label ready-for-agent") {
		t.Errorf("rollback missing --add-label ready-for-agent: %s", rollbackArgs)
	}
	if !strings.Contains(rollbackArgs, "--remove-label in-progress") {
		t.Errorf("rollback missing --remove-label in-progress: %s", rollbackArgs)
	}
	if !strings.Contains(rollbackArgs, "--remove-assignee dev-bot") {
		t.Errorf("rollback missing --remove-assignee dev-bot: %s", rollbackArgs)
	}
}

func TestClaim_NotTakeable(t *testing.T) {
	exec := &seqExecutor{
		t: t,
		responses: []seqResponse{
			userResp(),
			// Issue has no ready-for-agent and is not owned by dev-bot.
			issueResp(nil, nil),
		},
	}

	result, err := Claim(exec, 42, devToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != OutcomeNotTakeable {
		t.Errorf("outcome = %v; want OutcomeNotTakeable", result.Outcome)
	}
	if !strings.Contains(result.Details, "issue #42 is not takeable") {
		t.Errorf("details missing not-takeable message: %s", result.Details)
	}
	if exec.idx != 2 {
		t.Errorf("expected 2 calls (no edit); got %d", exec.idx)
	}
}

func TestClaim_GHApiUserError(t *testing.T) {
	exec := &seqExecutor{
		t: t,
		responses: []seqResponse{
			{err: fmt.Errorf("network error")},
		},
	}

	_, err := Claim(exec, 42, devToken)
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !strings.Contains(err.Error(), "gh api user") {
		t.Errorf("error missing 'gh api user': %v", err)
	}
}
