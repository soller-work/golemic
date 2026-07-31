// Package detbroker provides the deterministic broker client shared between
// the detagent binary and E2E scenario functions.
// It has no build tags so both the binary and the test suite can import it.
package detbroker

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	roundRe    = regexp.MustCompile(`round-(\d+)`)
	attemptRe  = regexp.MustCompile(`attempt-(\d+)`)
	scenarioRe = regexp.MustCompile(`(?m)^E2E-SCENARIO:\s*(\S+)`)
)

// Client is the deterministic broker client.
type Client struct {
	env         brokerEnv
	callCounter int
}

type brokerEnv struct {
	sockPath string
	runID    string
	invID    string
}

// NewFromEnv creates a Client from OS environment variables.
// Returns nil if GOLEMIC_GM_SOCK is not set.
func NewFromEnv() *Client {
	be := brokerEnv{
		sockPath: os.Getenv("GOLEMIC_GM_SOCK"),
		runID:    os.Getenv("GOLEMIC_RUN_ID"),
		invID:    os.Getenv("GOLEMIC_INVOCATION_ID"),
	}
	if be.sockPath == "" {
		return nil
	}
	return &Client{env: be}
}

// RoundFromEnv parses the round number from GOLEMIC_INVOCATION_ID.
// Returns 0 if not found.
func RoundFromEnv() int {
	return parseInvField(roundRe, os.Getenv("GOLEMIC_INVOCATION_ID"))
}

// AttemptFromEnv parses the attempt number from GOLEMIC_INVOCATION_ID.
// Returns 0 if not found.
func AttemptFromEnv() int {
	return parseInvField(attemptRe, os.Getenv("GOLEMIC_INVOCATION_ID"))
}

func parseInvField(re *regexp.Regexp, invID string) int {
	m := re.FindStringSubmatch(invID)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Call makes one tool call to the gm_ broker and returns the raw result.
func (c *Client) Call(tool string, params any) (json.RawMessage, error) {
	c.callCounter++
	callID := fmt.Sprintf("detagent-%d", c.callCounter)

	var paramsJSON json.RawMessage
	if params == nil {
		paramsJSON = json.RawMessage("{}")
	} else {
		var err error
		paramsJSON, err = json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
	}

	req := brokerRequest{
		RunID:        c.env.runID,
		InvocationID: c.env.invID,
		Tool:         tool,
		CallID:       callID,
		Params:       paramsJSON,
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	conn, err := net.Dial("unix", c.env.sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to broker %s: %w", c.env.sockPath, err)
	}
	defer conn.Close() //nolint:errcheck

	if _, err := conn.Write(reqJSON); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	var resp brokerResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return resp.Result, nil
}

// CheckOK returns an error if the broker result does not have ok:true.
func CheckOK(result json.RawMessage, tool string) error {
	var r struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return fmt.Errorf("%s: unmarshal result: %w", tool, err)
	}
	if !r.OK {
		return fmt.Errorf("%s failed: code=%s message=%s", tool, r.Code, r.Message)
	}
	return nil
}

// EmitTerminalLine writes the tool_execution_end JSON line to stdout so
// golemic's terminalDoneFromLine detects it and signals the terminal-done channel.
func EmitTerminalLine(toolName string, result json.RawMessage) {
	line, _ := json.Marshal(map[string]any{
		"type":     "tool_execution_end",
		"toolName": toolName,
		"result":   result,
	})
	fmt.Println(string(line))
}

// GitInCWD runs a git command in the current working directory.
func GitInCWD(args ...string) error {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

// FetchScenario calls gm_slice_get and extracts the E2E-SCENARIO name from
// the issue spec body.
func (c *Client) FetchScenario() (string, error) {
	result, err := c.Call("gm_slice_get", nil)
	if err != nil {
		return "", fmt.Errorf("gm_slice_get: %w", err)
	}
	var r struct {
		OK   bool   `json:"ok"`
		Spec string `json:"spec"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return "", fmt.Errorf("gm_slice_get result: %w", err)
	}
	if !r.OK {
		return "", fmt.Errorf("gm_slice_get returned ok=false")
	}
	m := scenarioRe.FindStringSubmatch(r.Spec)
	if len(m) < 2 {
		return "", fmt.Errorf("E2E-SCENARIO marker not found in spec")
	}
	return m[1], nil
}

// RunProjectCheck calls gm_project_check and returns (true, nil) when the
// check passes, (false, nil) when it fails cleanly, or (false, err) on error.
func (c *Client) RunProjectCheck() (bool, error) {
	result, err := c.Call("gm_project_check", nil)
	if err != nil {
		return false, fmt.Errorf("gm_project_check: %w", err)
	}
	var r struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return false, fmt.Errorf("gm_project_check: unmarshal result: %w", err)
	}
	return r.OK, nil
}

// DevDone calls gm_dev_done with the given fields and emits the terminal
// stdout line that golemic's terminalDoneFromLine detects.
func (c *Client) DevDone(summary, commitMsg, prTitle, prBody string) error {
	result, err := c.Call("gm_dev_done", map[string]string{
		"summary":   summary,
		"commitMsg": commitMsg,
		"prTitle":   prTitle,
		"prBody":    prBody,
	})
	if err != nil {
		return fmt.Errorf("gm_dev_done: %w", err)
	}
	if err := CheckOK(result, "gm_dev_done"); err != nil {
		return err
	}
	EmitTerminalLine("gm_dev_done", result)
	return nil
}

// WriteCommitAndDone writes filename with content, stages it, runs
// gm_project_check (must pass), then calls DevDone.
func (c *Client) WriteCommitAndDone(filename, content, commitMsg, prTitle, prBody string) error {
	dir := filepath.Dir(filename)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	if err := GitInCWD("add", filename); err != nil {
		return err
	}

	ok, err := c.RunProjectCheck()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("gm_project_check: check failed")
	}

	return c.DevDone(fmt.Sprintf("E2E detagent: %s", prTitle), commitMsg, prTitle, prBody)
}

// PRView calls gm_pr_view to confirm the PR exists before submitting a verdict.
func (c *Client) PRView() error {
	_, err := c.Call("gm_pr_view", nil)
	if err != nil {
		return fmt.Errorf("gm_pr_view: %w", err)
	}
	return nil
}

// Approve submits an approved review and emits the terminal stdout line.
func (c *Client) Approve(body string) error {
	return c.reviewSubmit("approved", body)
}

// RequestChanges submits a changes_requested review and emits the terminal stdout line.
func (c *Client) RequestChanges(body string) error {
	return c.reviewSubmit("changes_requested", body)
}

// SubmitComment submits an inline review comment. Non-fatal: callers may ignore errors.
func (c *Client) SubmitComment(path string, line int, body, severity string) error {
	_, err := c.Call("gm_review_submit_comment", map[string]any{
		"path":     path,
		"line":     line,
		"body":     body,
		"severity": severity,
	})
	return err
}

func (c *Client) reviewSubmit(verdict, body string) error {
	result, err := c.Call("gm_review_submit", map[string]string{
		"verdict":    verdict,
		"confidence": "high",
		"body":       body,
	})
	if err != nil {
		return fmt.Errorf("gm_review_submit: %w", err)
	}
	if err := CheckOK(result, "gm_review_submit"); err != nil {
		return err
	}
	EmitTerminalLine("gm_review_submit", result)
	return nil
}

type brokerRequest struct {
	RunID        string          `json:"runId"`
	InvocationID string          `json:"invocationId"`
	Tool         string          `json:"tool"`
	CallID       string          `json:"callId"`
	Params       json.RawMessage `json:"params"`
}

type brokerResponse struct {
	CallID string          `json:"callId"`
	Result json.RawMessage `json:"result"`
}
