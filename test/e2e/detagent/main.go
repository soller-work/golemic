// Command detagent is the deterministic fake pi binary used by E2E tests.
// It is built as an executable named "pi" and placed first on PATH so golemic's
// newPiCmd resolves it instead of the real Claude Code CLI.
//
// Invocation (from golemic's buildPiArgs):
//
//	pi -p --mode json --session-id <id> --append-system-prompt @<file>
//	   --tools <csv> --model <model> <userPrompt>
//
// Required env vars (set by golemic's runner):
//
//	GOLEMIC_GM_SOCK       — unix socket for the gm_ broker
//	GOLEMIC_RUN_ID        — current run ID
//	GOLEMIC_INVOCATION_ID — current invocation ID (contains role and round)
//	GOLEMIC_ROLE          — "dev" or "reviewer"
//
// The scenario is read from the issue spec via gm_slice_get by looking for the
// line "E2E-SCENARIO: <name>" in the spec body.
package main

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
	"time"

	"golemic/test/e2e/scenarios"
)

// scenarioRe matches "E2E-SCENARIO: <name>" in the issue body.
var scenarioRe = regexp.MustCompile(`(?m)^E2E-SCENARIO:\s*(\S+)`)

// roundRe matches "round-N" in the invocation ID.
var roundRe = regexp.MustCompile(`round-(\d+)`)

func main() {
	role := os.Getenv("GOLEMIC_ROLE")
	var err error
	switch role {
	case "dev":
		err = runDev()
	case "reviewer":
		err = runReviewer()
	default:
		fmt.Fprintf(os.Stderr, "detagent: unknown role %q\n", role)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "detagent %s: %v\n", role, err)
		os.Exit(1)
	}
}

// roundFromEnv parses the round number from GOLEMIC_INVOCATION_ID.
// Returns 0 if not found.
func roundFromEnv() int {
	inv := os.Getenv("GOLEMIC_INVOCATION_ID")
	m := roundRe.FindStringSubmatch(inv)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// brokerEnv bundles the env vars needed for every broker call.
type brokerEnv struct {
	sockPath string
	runID    string
	invID    string
}

func envFromOS() brokerEnv {
	return brokerEnv{
		sockPath: os.Getenv("GOLEMIC_GM_SOCK"),
		runID:    os.Getenv("GOLEMIC_RUN_ID"),
		invID:    os.Getenv("GOLEMIC_INVOCATION_ID"),
	}
}

// brokerRequest is the JSON object sent to the gm_ broker per the broker protocol.
type brokerRequest struct {
	RunID        string          `json:"runId"`
	InvocationID string          `json:"invocationId"`
	Tool         string          `json:"tool"`
	CallID       string          `json:"callId"`
	Params       json.RawMessage `json:"params"`
}

// brokerResponse is the JSON response from the broker.
type brokerResponse struct {
	CallID string          `json:"callId"`
	Result json.RawMessage `json:"result"`
}

var callCounter int

// brokerCall makes one tool call to the gm_ broker and returns the raw result.
func brokerCall(benv brokerEnv, tool string, params any) (json.RawMessage, error) {
	callCounter++
	callID := fmt.Sprintf("detagent-%d", callCounter)

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
		RunID:        benv.runID,
		InvocationID: benv.invID,
		Tool:         tool,
		CallID:       callID,
		Params:       paramsJSON,
	}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	conn, err := net.Dial("unix", benv.sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to broker %s: %w", benv.sockPath, err)
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

// checkOK returns an error if the broker result does not have ok:true.
func checkOK(result json.RawMessage, tool string) error {
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

// emitTerminalLine writes the tool_execution_end JSON line to stdout so golemic's
// terminalDoneFromLine detects it and signals the terminal-done channel.
func emitTerminalLine(toolName string, result json.RawMessage) {
	line, _ := json.Marshal(map[string]any{
		"type":     "tool_execution_end",
		"toolName": toolName,
		"result":   result,
	})
	fmt.Println(string(line))
}

// fetchScenario calls gm_slice_get and extracts the E2E-SCENARIO name.
func fetchScenario(benv brokerEnv) (string, error) {
	result, err := brokerCall(benv, "gm_slice_get", nil)
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

// gitInCWD runs a git command in the current working directory.
func gitInCWD(args ...string) error {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

// runDev implements the dev role script.
func runDev() error {
	benv := envFromOS()
	if benv.sockPath == "" {
		return fmt.Errorf("GOLEMIC_GM_SOCK not set")
	}

	scenario, err := fetchScenario(benv)
	if err != nil {
		return err
	}

	round := roundFromEnv()
	fmt.Fprintf(os.Stderr, "detagent dev: scenario=%s round=%d\n", scenario, round)

	switch scenario {
	case scenarios.HappyPath:
		return devHappyPath(benv)
	case scenarios.ReviewerRejectsOnce:
		return devReviewerRejectsOnce(benv, round)
	case scenarios.DevVerifyFails:
		return devVerifyFails(benv)
	case scenarios.Collision:
		// Should not be invoked (golemic exits before dev agent runs).
		return devHappyPath(benv)
	case scenarios.Timeout:
		// Sleep until golemic kills us — this triggers the timeout/stall path.
		fmt.Fprintln(os.Stderr, "detagent dev: sleeping to trigger timeout")
		time.Sleep(24 * time.Hour)
		return nil
	default:
		return fmt.Errorf("unknown scenario %q", scenario)
	}
}

func devHappyPath(benv brokerEnv) error {
	return devWriteCommitAndDone(benv, "e2e_happy_path.txt", "E2E happy path marker\n",
		"feat: add e2e happy path marker (detagent)",
		"E2E happy path test",
		"Automated E2E happy path test driven by detagent.")
}

func devReviewerRejectsOnce(benv brokerEnv, round int) error {
	content := fmt.Sprintf("E2E reviewer_rejects_once marker — round %d\n", round)
	commitMsg := fmt.Sprintf("feat: e2e reviewer_rejects_once round %d (detagent)", round)
	return devWriteCommitAndDone(benv, "e2e_review_reject.txt", content,
		commitMsg,
		"E2E reviewer_rejects_once test",
		"Automated E2E reviewer_rejects_once test driven by detagent.")
}

func devVerifyFails(benv brokerEnv) error {
	// Write a sentinel file that the sandbox verify.sh checks for.
	// If the sandbox has `bash ./verify.sh` and verify.sh fails when VERIFY_BREAK
	// exists, gm_project_check will return ok=false and we exit 1 (dev_failed).
	// If the sandbox verify doesn't check VERIFY_BREAK, we still exit 1 explicitly
	// so the test's dev_failed assertion holds.
	if err := os.WriteFile("VERIFY_BREAK", []byte("detagent: verify break sentinel\n"), 0644); err != nil {
		return fmt.Errorf("write VERIFY_BREAK: %w", err)
	}
	if err := gitInCWD("add", "VERIFY_BREAK"); err != nil {
		return fmt.Errorf("git add VERIFY_BREAK: %w", err)
	}

	benv2 := envFromOS()
	result, err := brokerCall(benv2, "gm_project_check", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "detagent dev_verify_fails: gm_project_check error: %v\n", err)
		return err
	}
	var pcResult struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(result, &pcResult); err != nil {
		fmt.Fprintf(os.Stderr, "detagent dev_verify_fails: parse project_check result: %v\n", err)
		return err
	}
	if pcResult.OK {
		fmt.Fprintln(os.Stderr, "detagent dev_verify_fails: verify passed despite VERIFY_BREAK; sandbox needs verify.sh that checks for VERIFY_BREAK")
	} else {
		fmt.Fprintln(os.Stderr, "detagent dev_verify_fails: verify correctly failed due to VERIFY_BREAK")
	}
	// Always exit 1 to signal dev failure to the runner.
	return fmt.Errorf("dev_verify_fails: exiting with error to trigger dev_failed")
}

// devWriteCommitAndDone writes a file, stages it, calls gm_project_check and gm_dev_done,
// then emits the terminal stdout line.
func devWriteCommitAndDone(benv brokerEnv, filename, content, commitMsg, prTitle, prBody string) error {
	dir := filepath.Dir(filename)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	if err := gitInCWD("add", filename); err != nil {
		return err
	}

	// gm_project_check — must pass before gm_dev_done.
	pcResult, err := brokerCall(benv, "gm_project_check", nil)
	if err != nil {
		return fmt.Errorf("gm_project_check: %w", err)
	}
	if err := checkOK(pcResult, "gm_project_check"); err != nil {
		return err
	}

	// gm_dev_done — runner will commit the staged file with commitMsg, push, and open PR.
	devDoneParams := map[string]string{
		"summary":   fmt.Sprintf("E2E detagent: %s", prTitle),
		"commitMsg": commitMsg,
		"prTitle":   prTitle,
		"prBody":    prBody,
	}
	ddResult, err := brokerCall(benv, "gm_dev_done", devDoneParams)
	if err != nil {
		return fmt.Errorf("gm_dev_done: %w", err)
	}
	if err := checkOK(ddResult, "gm_dev_done"); err != nil {
		return err
	}

	// Emit the terminal stdout line so golemic's terminalDoneFromLine fires.
	emitTerminalLine("gm_dev_done", ddResult)
	return nil
}

// runReviewer implements the reviewer role script.
func runReviewer() error {
	benv := envFromOS()
	if benv.sockPath == "" {
		return fmt.Errorf("GOLEMIC_GM_SOCK not set")
	}

	scenario, err := fetchScenario(benv)
	if err != nil {
		return err
	}

	round := roundFromEnv()
	fmt.Fprintf(os.Stderr, "detagent reviewer: scenario=%s round=%d\n", scenario, round)

	// gm_pr_view — confirm the PR exists before deciding verdict.
	if _, err := brokerCall(benv, "gm_pr_view", nil); err != nil {
		return fmt.Errorf("gm_pr_view: %w", err)
	}

	switch scenario {
	case scenarios.HappyPath, scenarios.DevVerifyFails, scenarios.Collision, scenarios.Timeout:
		return reviewerApprove(benv)
	case scenarios.ReviewerRejectsOnce:
		if round == 1 {
			return reviewerReject(benv)
		}
		return reviewerApprove(benv)
	default:
		return fmt.Errorf("unknown scenario %q", scenario)
	}
}

func reviewerApprove(benv brokerEnv) error {
	return reviewerSubmit(benv, "approved", "E2E detagent: LGTM — changes look good.")
}

func reviewerReject(benv brokerEnv) error {
	// Add a blocking comment so the test can verify comment round-trip if needed.
	if _, err := brokerCall(benv, "gm_review_submit_comment", map[string]any{
		"path":     "e2e_review_reject.txt",
		"line":     1,
		"body":     "E2E detagent blocking comment: please fix before approval.",
		"severity": "blocking",
	}); err != nil {
		// Non-fatal: proceed to review_submit even if comment fails.
		fmt.Fprintf(os.Stderr, "detagent reviewer: gm_review_submit_comment: %v (continuing)\n", err)
	}
	return reviewerSubmit(benv, "changes_requested", "E2E detagent: changes requested — see blocking comment.")
}

func reviewerSubmit(benv brokerEnv, verdict, body string) error {
	result, err := brokerCall(benv, "gm_review_submit", map[string]string{
		"verdict":    verdict,
		"confidence": "high",
		"body":       body,
	})
	if err != nil {
		return fmt.Errorf("gm_review_submit: %w", err)
	}
	if err := checkOK(result, "gm_review_submit"); err != nil {
		return err
	}
	// Emit terminal line so golemic's terminalDoneFromLine fires.
	emitTerminalLine("gm_review_submit", result)
	return nil
}
