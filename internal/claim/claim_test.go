package claim

import (
	"fmt"
	"strings"
	"testing"
)

// scriptedExecutor replays a fixed sequence of (result, error) pairs for RunWithEnv.
type scriptedExecutor struct {
	calls []scriptedCall
	idx   int
	t     *testing.T
}

type scriptedCall struct {
	result string
	err    error
	// optional assertions
	wantArgs []string
	wantEnv  map[string]string
}

func (e *scriptedExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	if e.idx >= len(e.calls) {
		e.t.Fatalf("unexpected RunWithEnv call #%d: %s %v", e.idx+1, name, args)
	}
	c := e.calls[e.idx]
	e.idx++

	if c.wantEnv != nil {
		for k, want := range c.wantEnv {
			if got := env[k]; got != want {
				e.t.Errorf("call #%d env[%q]: got %q, want %q", e.idx, k, got, want)
			}
		}
	}
	if c.wantArgs != nil {
		full := append([]string{name}, args...)
		for i, want := range c.wantArgs {
			if i >= len(full) || full[i] != want {
				e.t.Errorf("call #%d arg[%d]: got %q, want %q (full: %v)", e.idx, i, safeGet(full, i), want, full)
			}
		}
	}
	return c.result, c.err
}

func (e *scriptedExecutor) Run(name string, args ...string) (string, error) {
	e.t.Fatalf("unexpected Run call: %s %v", name, args)
	return "", nil
}
func (e *scriptedExecutor) RunInDir(_ string, name string, args ...string) (string, error) {
	e.t.Fatalf("unexpected RunInDir call: %s %v", name, args)
	return "", nil
}
func (e *scriptedExecutor) RunWithEnvInDir(env map[string]string, _ string, name string, args ...string) (string, error) {
	return e.RunWithEnv(env, name, args...)
}

func safeGet(ss []string, i int) string {
	if i < len(ss) {
		return ss[i]
	}
	return "<missing>"
}

func issueViewJSON(labels []string, assignees []string) string {
	lblParts := make([]string, len(labels))
	for i, l := range labels {
		lblParts[i] = fmt.Sprintf(`{"name":%q}`, l)
	}
	asgParts := make([]string, len(assignees))
	for i, a := range assignees {
		asgParts[i] = fmt.Sprintf(`{"login":%q}`, a)
	}
	return fmt.Sprintf(`{"labels":[%s],"assignees":[%s]}`,
		strings.Join(lblParts, ","), strings.Join(asgParts, ","))
}

const (
	testNumber   = 42
	testDevLogin = "golemic-dev"
	testDevToken = "ghp_dev_xxx"
)

// Unit: happy path — pre-read takeable, edit OK, post-verify owns.
func TestClaim_HappyPath(t *testing.T) {
	preView := issueViewJSON([]string{"ready-for-agent"}, nil)
	postView := issueViewJSON([]string{"in-progress"}, []string{testDevLogin})

	exec := &scriptedExecutor{t: t, calls: []scriptedCall{
		{result: preView, wantEnv: map[string]string{"GH_TOKEN": testDevToken}}, // pre-read
		{result: ""}, // edit
		{result: postView, wantEnv: map[string]string{"GH_TOKEN": testDevToken}}, // post-verify
	}}

	result, err := Claim(exec, testNumber, testDevLogin, testDevToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != ResultOK {
		t.Errorf("result: got %v, want ResultOK", result)
	}
	if exec.idx != 3 {
		t.Errorf("expected 3 RunWithEnv calls, got %d", exec.idx)
	}
}

// Unit: idempotent — pre-read shows already owned, no further calls.
func TestClaim_Idempotent(t *testing.T) {
	preView := issueViewJSON([]string{"in-progress"}, []string{testDevLogin})

	exec := &scriptedExecutor{t: t, calls: []scriptedCall{
		{result: preView}, // pre-read only
	}}

	result, err := Claim(exec, testNumber, testDevLogin, testDevToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != ResultIdempotent {
		t.Errorf("result: got %v, want ResultIdempotent", result)
	}
	if exec.idx != 1 {
		t.Errorf("expected 1 call (pre-read only), got %d", exec.idx)
	}
}

// Unit: not takeable — no ready-for-agent, not already owned.
func TestClaim_NotTakeable(t *testing.T) {
	preView := issueViewJSON([]string{"enhancement"}, nil)

	exec := &scriptedExecutor{t: t, calls: []scriptedCall{
		{result: preView}, // pre-read only
	}}

	result, err := Claim(exec, testNumber, testDevLogin, testDevToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != ResultNotTakeable {
		t.Errorf("result: got %v, want ResultNotTakeable", result)
	}
	if exec.idx != 1 {
		t.Errorf("expected 1 call (pre-read only), got %d", exec.idx)
	}
}

// Unit: race lost — post-verify shows foreign assignee; rollback issued.
func TestClaim_RaceLost(t *testing.T) {
	preView := issueViewJSON([]string{"ready-for-agent"}, nil)
	postView := issueViewJSON([]string{"in-progress"}, []string{"other-bot"})

	exec := &scriptedExecutor{t: t, calls: []scriptedCall{
		{result: preView},  // pre-read
		{result: ""},       // edit
		{result: postView}, // post-verify (foreign assignee)
		{result: ""},       // rollback
	}}

	result, err := Claim(exec, testNumber, testDevLogin, testDevToken)
	if result != ResultRaceLost {
		t.Errorf("result: got %v, want ResultRaceLost", result)
	}
	if err == nil {
		t.Error("expected non-nil error with verify details on race lost")
	}
	if exec.idx != 4 {
		t.Errorf("expected 4 calls (pre-read+edit+verify+rollback), got %d", exec.idx)
	}
}

// Unit: race lost with rollback failure — error wraps both details.
func TestClaim_RaceLost_RollbackFails(t *testing.T) {
	preView := issueViewJSON([]string{"ready-for-agent"}, nil)
	postView := issueViewJSON([]string{"in-progress"}, []string{"other-bot"})

	exec := &scriptedExecutor{t: t, calls: []scriptedCall{
		{result: preView},
		{result: ""},
		{result: postView},
		{err: fmt.Errorf("network error")}, // rollback fails
	}}

	result, err := Claim(exec, testNumber, testDevLogin, testDevToken)
	if result != ResultRaceLost {
		t.Errorf("result: got %v, want ResultRaceLost", result)
	}
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Errorf("expected error containing 'rollback failed', got %v", err)
	}
}

// Unit: edit failure returns error.
func TestClaim_EditFails(t *testing.T) {
	preView := issueViewJSON([]string{"ready-for-agent"}, nil)

	exec := &scriptedExecutor{t: t, calls: []scriptedCall{
		{result: preView},
		{err: fmt.Errorf("gh: HTTP 422")},
	}}

	_, err := Claim(exec, testNumber, testDevLogin, testDevToken)
	if err == nil {
		t.Error("expected error on edit failure")
	}
	if !strings.Contains(err.Error(), "gh issue edit") {
		t.Errorf("error should mention gh issue edit, got: %v", err)
	}
}

// Unit: GH_TOKEN is injected in all RunWithEnv calls.
func TestClaim_TokenInjected(t *testing.T) {
	preView := issueViewJSON([]string{"ready-for-agent"}, nil)
	postView := issueViewJSON([]string{"in-progress"}, []string{testDevLogin})

	var capturedTokens []string
	captureExec := &captureEnvExecutor{
		t:        t,
		token:    testDevToken,
		captured: &capturedTokens,
		calls: []scriptedCall{
			{result: preView},
			{result: ""},
			{result: postView},
		},
	}

	result, err := Claim(captureExec, testNumber, testDevLogin, testDevToken)
	if err != nil || result != ResultOK {
		t.Fatalf("unexpected: result=%v err=%v", result, err)
	}
	for i, tok := range capturedTokens {
		if tok != testDevToken {
			t.Errorf("call #%d: GH_TOKEN=%q, want %q", i+1, tok, testDevToken)
		}
	}
}

// captureEnvExecutor records GH_TOKEN from every RunWithEnv call.
type captureEnvExecutor struct {
	t        *testing.T
	token    string
	captured *[]string
	calls    []scriptedCall
	idx      int
}

func (e *captureEnvExecutor) RunWithEnv(env map[string]string, _ string, _ ...string) (string, error) {
	*e.captured = append(*e.captured, env["GH_TOKEN"])
	if e.idx >= len(e.calls) {
		e.t.Fatalf("unexpected call #%d", e.idx+1)
	}
	c := e.calls[e.idx]
	e.idx++
	return c.result, c.err
}
func (e *captureEnvExecutor) Run(_ string, _ ...string) (string, error) { return "", nil }
func (e *captureEnvExecutor) RunInDir(_ string, _ string, _ ...string) (string, error) {
	return "", nil
}
func (e *captureEnvExecutor) RunWithEnvInDir(_ map[string]string, _ string, _ string, _ ...string) (string, error) {
	return "", nil
}
