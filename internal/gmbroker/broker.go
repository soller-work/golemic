// Package gmbroker runs a per-agent-invocation unix-socket JSON-RPC server that
// handles gm_ tool calls forwarded by the golemic pi extension.
//
// The runner owns the Broker lifecycle: Start before spawning pi, Shutdown after
// the invocation ends. The agent subprocess never holds GitHub credentials; all
// credential-bearing operations happen inside the broker.
package gmbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golemic/internal/worktreefingerprint"
)

// IssueFetcher fetches the Markdown body of a GitHub issue.
// Called at most once per Broker instance (lazy cache via sync.Once).
type IssueFetcher func(ctx context.Context) (string, error)

// ProjectCheckConfig configures gm_project_check for a Dev invocation.
type ProjectCheckConfig struct {
	WorktreePath  string
	VerifyCommand string
	Env           map[string]string
}

// Broker listens on a unix socket and dispatches gm_ tool calls.
type Broker struct {
	sockPath string
	listener net.Listener
	fetcher  IssueFetcher

	projectCheck ProjectCheckConfig
	allowedTools map[string]struct{}

	once       sync.Once
	cachedBody string
	fetchErr   error
}

// gmRequest is the payload the pi extension sends for each tool call.
type gmRequest struct {
	Tool   string          `json:"tool"`
	CallID string          `json:"callId"`
	Params json.RawMessage `json:"params"`
}

// gmResponse wraps the tool result for the pi extension.
type gmResponse struct {
	CallID string          `json:"callId"`
	Result json.RawMessage `json:"result"`
}

var defaultTools = []string{"gm_slice_get", "gm_dev_done", "gm_review_submit"}

// Start creates and returns a Broker that fetches issue body via gh CLI using
// devToken. The socket dir is created with 0700 permissions.
func Start(sockPath string, issueNum int, devToken string) (*Broker, error) {
	fetcher := func(ctx context.Context) (string, error) {
		return fetchIssueMD(ctx, issueNum, devToken)
	}
	return StartWithFetcher(sockPath, fetcher)
}

// StartWithFetcher creates a Broker with an injectable fetcher for tests.
func StartWithFetcher(sockPath string, fetcher IssueFetcher) (*Broker, error) {
	return StartWithFetcherAndProjectCheck(sockPath, fetcher, ProjectCheckConfig{}, defaultTools)
}

// StartWithFetcherAndProjectCheck creates a Broker with injectable fetcher,
// project-check configuration, and tool allowlist.
func StartWithFetcherAndProjectCheck(sockPath string, fetcher IssueFetcher, projectCheck ProjectCheckConfig, allowedTools []string) (*Broker, error) {
	if fetcher == nil {
		fetcher = func(context.Context) (string, error) { return "", fmt.Errorf("fetcher not configured") }
	}
	sockDir := filepath.Dir(sockPath)
	if err := os.MkdirAll(sockDir, 0700); err != nil {
		return nil, fmt.Errorf("gmbroker: mkdir socket dir: %w", err)
	}
	if err := os.Chmod(sockDir, 0700); err != nil {
		return nil, fmt.Errorf("gmbroker: chmod socket dir: %w", err)
	}
	os.Remove(sockPath) //nolint:errcheck
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("gmbroker: listen %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0600); err != nil {
		ln.Close()          //nolint:errcheck
		os.Remove(sockPath) //nolint:errcheck
		return nil, fmt.Errorf("gmbroker: chmod socket: %w", err)
	}
	b := &Broker{
		sockPath:     sockPath,
		listener:     ln,
		fetcher:      fetcher,
		projectCheck: projectCheck,
		allowedTools: toolSet(allowedTools),
	}
	go b.acceptLoop()
	return b, nil
}

// ConfigureProjectCheck updates the dev-only gm_project_check configuration.
func (b *Broker) ConfigureProjectCheck(cfg ProjectCheckConfig) {
	if b == nil {
		return
	}
	b.projectCheck = cfg
}

// SetAllowedTools replaces the tool allowlist for this broker instance.
func (b *Broker) SetAllowedTools(tools []string) {
	if b == nil {
		return
	}
	b.allowedTools = toolSet(tools)
}

// Shutdown stops the broker listener and removes the socket file.
func (b *Broker) Shutdown() {
	if b.listener != nil {
		b.listener.Close() //nolint:errcheck
	}
	os.Remove(b.sockPath) //nolint:errcheck
}

func (b *Broker) acceptLoop() {
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		go b.handleConn(conn)
	}
}

func (b *Broker) handleConn(conn net.Conn) {
	defer conn.Close() //nolint:errcheck

	var req gmRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeResult(conn, "", errResult("TRANSPORT_ERROR", "invalid JSON: "+err.Error()))
		return
	}

	result := b.dispatch(req)
	resp := gmResponse{CallID: req.CallID, Result: result}
	out, _ := json.Marshal(resp)
	out = append(out, '\n')
	conn.Write(out) //nolint:errcheck
}

func (b *Broker) dispatch(req gmRequest) json.RawMessage {
	if !b.toolAllowed(req.Tool) {
		return errResult("UNKNOWN_TOOL", "unknown tool: "+req.Tool)
	}

	switch req.Tool {
	case "gm_slice_get":
		return b.handleSliceGet()
	case "gm_dev_done":
		return handleDevDone(req.Params)
	case "gm_review_submit":
		return handleReviewSubmit(req.Params)
	case "gm_project_check":
		return b.handleProjectCheck(req.Params)
	default:
		return errResult("UNKNOWN_TOOL", "unknown tool: "+req.Tool)
	}
}

func (b *Broker) toolAllowed(tool string) bool {
	if len(b.allowedTools) == 0 {
		return true
	}
	_, ok := b.allowedTools[tool]
	return ok
}

// handleSliceGet returns the cached issue body, fetching it on the first call.
func (b *Broker) handleSliceGet() json.RawMessage {
	b.once.Do(func() {
		b.cachedBody, b.fetchErr = b.fetcher(context.Background())
	})
	if b.fetchErr != nil {
		return errResult("FETCH_FAILED", b.fetchErr.Error())
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "spec": b.cachedBody})
	return json.RawMessage(out)
}

// DevDoneParams is the expected payload for gm_dev_done.
type DevDoneParams struct {
	Summary   string `json:"summary"`
	CommitMsg string `json:"commitMsg"`
}

func handleDevDone(raw json.RawMessage) json.RawMessage {
	var p DevDoneParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errResult("SCHEMA_INVALID", "gm_dev_done: "+err.Error())
	}
	if p.Summary == "" {
		return errResult("SCHEMA_INVALID", "gm_dev_done: summary is required")
	}
	if p.CommitMsg == "" {
		return errResult("SCHEMA_INVALID", "gm_dev_done: commitMsg is required")
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "echo": p})
	return json.RawMessage(out)
}

// ReviewSubmitParams is the expected payload for gm_review_submit.
type ReviewSubmitParams struct {
	Verdict         string `json:"verdict"`
	MergeConfidence string `json:"mergeConfidence"`
	Body            string `json:"body"`
}

func handleReviewSubmit(raw json.RawMessage) json.RawMessage {
	var p ReviewSubmitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errResult("SCHEMA_INVALID", "gm_review_submit: "+err.Error())
	}
	if p.Verdict != "approved" && p.Verdict != "changes_requested" {
		return errResult("SCHEMA_INVALID", `gm_review_submit: verdict must be "approved" or "changes_requested"`)
	}
	if p.MergeConfidence == "" {
		return errResult("SCHEMA_INVALID", "gm_review_submit: mergeConfidence is required")
	}
	if p.Body == "" {
		return errResult("SCHEMA_INVALID", "gm_review_submit: body is required")
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "echo": p})
	return json.RawMessage(out)
}

// ProjectCheckParams is the optional payload for gm_project_check.
type ProjectCheckParams struct {
	Output *string `json:"output"`
}

type projectCheckResult struct {
	OK                     bool   `json:"ok"`
	Command                string `json:"command"`
	ExitCode               int    `json:"exitCode"`
	Stdout                 string `json:"stdout"`
	Stderr                 string `json:"stderr"`
	Summary                string `json:"summary"`
	WorkingTreeFingerprint string `json:"workingTreeFingerprint"`
}

func (b *Broker) handleProjectCheck(raw json.RawMessage) json.RawMessage {
	var p ProjectCheckParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errResult("SCHEMA_INVALID", "gm_project_check: "+err.Error())
	}

	mode := "capped"
	if p.Output != nil && *p.Output != "" {
		mode = *p.Output
	}
	if mode != "capped" && mode != "full" {
		return errResult("SCHEMA_INVALID", `gm_project_check: output must be "capped" or "full"`)
	}
	if b.projectCheck.WorktreePath == "" || b.projectCheck.VerifyCommand == "" {
		return errResult("PROJECT_CHECK_NOT_AVAILABLE", "gm_project_check is not configured for this broker")
	}

	res, err := runProjectCheck(b.projectCheck, mode)
	if err != nil {
		return errResult("PROJECT_CHECK_FAILED", err.Error())
	}
	out, _ := json.Marshal(res)
	return json.RawMessage(out)
}

func runProjectCheck(cfg ProjectCheckConfig, mode string) (*projectCheckResult, error) {
	cmd := exec.Command("sh", "-c", cfg.VerifyCommand)
	cmd.Dir = cfg.WorktreePath
	cmd.Env = mergeEnv(os.Environ(), cfg.Env)

	var stdoutBuf strings.Builder
	var stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	fullStdout := stdoutBuf.String()
	fullStderr := stderrBuf.String()

	fingerprint, fpErr := fingerprintAfterVerify(cfg.WorktreePath)
	if fpErr != nil {
		return nil, fpErr
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("run verify command: %w", err)
		}
	}

	stdoutr := fullStdout
	stderrr := fullStderr
	if mode == "capped" {
		stdoutr = capStream(fullStdout)
		stderrr = capStream(fullStderr)
	}

	logProjectCheck(cfg.VerifyCommand, exitCode, fullStdout, fullStderr, fingerprint)

	return &projectCheckResult{
		OK:                     exitCode == 0,
		Command:                cfg.VerifyCommand,
		ExitCode:               exitCode,
		Stdout:                 stdoutr,
		Stderr:                 stderrr,
		Summary:                summaryForExit(exitCode),
		WorkingTreeFingerprint: fingerprint,
	}, nil
}

func fingerprintAfterVerify(worktreePath string) (string, error) {
	return worktreefingerprint.Compute(worktreePath, gitExecutor{})
}

type gitExecutor struct{}

func (gitExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

func capStream(out string) string {
	const maxLines = 200
	if out == "" {
		return out
	}
	trimmed := strings.TrimSuffix(out, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= maxLines {
		return out
	}

	head := maxLines / 2
	tail := maxLines - head
	omitted := len(lines) - head - tail
	var b strings.Builder
	b.Grow(len(out))
	b.WriteString(strings.Join(lines[:head], "\n"))
	if head > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(fmt.Sprintf("... <%d lines truncated> ...", omitted))
	if tail > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Join(lines[len(lines)-tail:], "\n"))
	}
	return b.String()
}

func summaryForExit(exitCode int) string {
	if exitCode == 0 {
		return "verify passed"
	}
	return fmt.Sprintf("verify failed (exit %d)", exitCode)
}

func logProjectCheck(command string, exitCode int, stdout, stderr, fingerprint string) {
	fmt.Fprintf(os.Stderr, "gm_project_check: command=%q exit=%d fingerprint=%s\n", command, exitCode, fingerprint)
	if stdout != "" {
		fmt.Fprintf(os.Stderr, "gm_project_check stdout:\n%s\n", stdout)
	}
	if stderr != "" {
		fmt.Fprintf(os.Stderr, "gm_project_check stderr:\n%s\n", stderr)
	}
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return append([]string(nil), base...)
	}
	vars := make(map[string]string, len(base)+len(extra))
	order := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, seen := vars[k]; !seen {
			order = append(order, k)
		}
		vars[k] = v
	}
	for k, v := range extra {
		if _, seen := vars[k]; !seen {
			order = append(order, k)
		}
		vars[k] = v
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out
}

func errResult(code, message string) json.RawMessage {
	out, _ := json.Marshal(map[string]any{"ok": false, "code": code, "message": message})
	return json.RawMessage(out)
}

func writeResult(conn net.Conn, callID string, result json.RawMessage) {
	resp := gmResponse{CallID: callID, Result: result}
	out, _ := json.Marshal(resp)
	out = append(out, '\n')
	conn.Write(out) //nolint:errcheck
}

func fetchIssueMD(ctx context.Context, issueNum int, devToken string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "issue", "view",
		fmt.Sprintf("%d", issueNum), "--json", "body", "--jq", ".body")
	cmd.Env = append(os.Environ(), "GH_TOKEN="+devToken)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh issue view %d: %w", issueNum, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func toolSet(tools []string) map[string]struct{} {
	if len(tools) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		set[tool] = struct{}{}
	}
	return set
}
