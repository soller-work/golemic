// Package gmbroker runs a per-agent-invocation unix-socket JSON-RPC server that
// handles gm_ tool calls forwarded by the golemic pi extension.
//
// The runner owns the Broker lifecycle: Start before spawning pi, Shutdown after
// the invocation ends. The agent subprocess never holds GitHub credentials; all
// credential-bearing operations happen inside the broker.
package gmbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

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

// ReviewerConfig configures gm_pr_view and gm_repo_tree for a reviewer invocation.
type ReviewerConfig struct {
	WorktreePath  string
	ReviewerToken string
	RepoRoot      string
	PRNumber      int
}

// ProjectCheckResult is the result of a gm_project_check call.
// Exported so tests can inject a fake ProjectCheckFn.
type ProjectCheckResult struct {
	OK                     bool   `json:"ok"`
	Command                string `json:"command"`
	ExitCode               int    `json:"exitCode"`
	Stdout                 string `json:"stdout"`
	Stderr                 string `json:"stderr"`
	Summary                string `json:"summary"`
	WorkingTreeFingerprint string `json:"workingTreeFingerprint"`
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

	// §10 gate state (per invocation)
	lastCheckMu            sync.Mutex
	lastCheck              *ProjectCheckResult // nil until first gm_project_check in this invocation
	devDoneMu              sync.Mutex
	devDone                *DevDoneParams
	devDoneGateRejected    bool
	devDoneGateMsg         string
	devDoneFingerprint     string
	devDoneTerminalCallID  string
	devDoneTerminalRaw     json.RawMessage
	devDoneTerminalResult  json.RawMessage
	devDoneTerminalStatus  string
	devDoneTerminalMessage string
	devDoneTerminalPending bool

	reviewer ReviewerConfig

	// Injectable functions — set to non-nil in tests for deterministic behavior.
	projectCheckFn       func(cfg ProjectCheckConfig, mode string) (*ProjectCheckResult, error)
	computeFingerprintFn func(worktreePath string) (string, error)
	prViewFn             func(cfg ReviewerConfig) (json.RawMessage, error)
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

// SetReviewerConfig configures gm_pr_view and gm_repo_tree for the reviewer role.
func (b *Broker) SetReviewerConfig(cfg ReviewerConfig) {
	if b == nil {
		return
	}
	b.reviewer = cfg
}

// SetAllowedTools replaces the tool allowlist for this broker instance.
func (b *Broker) SetAllowedTools(tools []string) {
	if b == nil {
		return
	}
	b.allowedTools = toolSet(tools)
}

// SetProjectCheckFn replaces the project-check function. Used in tests to return
// a deterministic result without running real commands or accessing a git repo.
func (b *Broker) SetProjectCheckFn(fn func(cfg ProjectCheckConfig, mode string) (*ProjectCheckResult, error)) {
	if b == nil {
		return
	}
	b.projectCheckFn = fn
}

// SetComputeFingerprintFn replaces the fingerprint computation function. Used in
// tests to return a fixed fingerprint matching the fake project-check result.
func (b *Broker) SetComputeFingerprintFn(fn func(worktreePath string) (string, error)) {
	if b == nil {
		return
	}
	b.computeFingerprintFn = fn
}

// SetPRViewFn replaces the gm_pr_view fetch function. Used in tests.
func (b *Broker) SetPRViewFn(fn func(cfg ReviewerConfig) (json.RawMessage, error)) {
	if b == nil {
		return
	}
	b.prViewFn = fn
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
		return b.handleDevDone(req.CallID, req.Params)
	case "gm_review_submit":
		return handleReviewSubmit(req.Params)
	case "gm_project_check":
		return b.handleProjectCheck(req.Params)
	case "gm_pr_view":
		return b.handlePRView()
	case "gm_repo_tree":
		return b.handleRepoTree(req.Params)
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
	PrTitle   string `json:"prTitle"`
	PrBody    string `json:"prBody"`
}

// DevDoneTerminalState captures the stored terminal response for gm_dev_done.
type DevDoneTerminalState struct {
	Result  json.RawMessage
	Status  string
	Message string
}

func (b *Broker) handleDevDone(callID string, raw json.RawMessage) json.RawMessage {
	rawCopy := cloneRawMessage(raw)
	if res := b.reserveDevDoneTerminal(callID, rawCopy); res != nil {
		return res
	}

	p, res := b.validateDevDoneParams(rawCopy)
	if res != nil {
		b.finalizeDevDoneTerminal(callID, rawCopy, res)
		return res
	}
	if res := b.checkDevDoneGate(); res != nil {
		b.finalizeDevDoneTerminal(callID, rawCopy, res)
		return res
	}
	b.devDoneMu.Lock()
	b.devDone = p
	b.devDoneMu.Unlock()
	out, _ := json.Marshal(map[string]any{"ok": true, "accepted": true})
	result := json.RawMessage(out)
	b.finalizeDevDoneTerminal(callID, rawCopy, result)
	return result
}

func (b *Broker) reserveDevDoneTerminal(callID string, raw json.RawMessage) json.RawMessage {
	b.devDoneMu.Lock()
	defer b.devDoneMu.Unlock()

	if b.devDoneTerminalResult != nil {
		if b.devDoneTerminalCallID == callID && bytes.Equal(b.devDoneTerminalRaw, raw) {
			return cloneRawMessage(b.devDoneTerminalResult)
		}
		return errResult("PROTOCOL_ERROR", "gm_dev_done: terminal gm_dev_done already called in this invocation")
	}
	if b.devDoneTerminalPending {
		return errResult("PROTOCOL_ERROR", "gm_dev_done: terminal gm_dev_done already in progress")
	}

	b.devDoneTerminalPending = true
	b.devDoneTerminalCallID = callID
	b.devDoneTerminalRaw = cloneRawMessage(raw)
	return nil
}

func (b *Broker) finalizeDevDoneTerminal(callID string, raw, result json.RawMessage) {
	status, message := parseDevDoneTerminalResult(result)
	b.devDoneMu.Lock()
	b.devDoneTerminalPending = false
	b.devDoneTerminalCallID = callID
	b.devDoneTerminalRaw = cloneRawMessage(raw)
	b.devDoneTerminalResult = cloneRawMessage(result)
	b.devDoneTerminalStatus = status
	b.devDoneTerminalMessage = message
	b.devDoneMu.Unlock()
}

func (b *Broker) validateDevDoneParams(raw json.RawMessage) (*DevDoneParams, json.RawMessage) {
	var p DevDoneParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errResult("SCHEMA_INVALID", "gm_dev_done: "+err.Error())
	}
	if p.Summary == "" {
		return nil, errResult("SCHEMA_INVALID", "gm_dev_done: summary is required")
	}
	if p.CommitMsg == "" {
		return nil, errResult("SCHEMA_INVALID", "gm_dev_done: commitMsg is required")
	}
	if p.PrTitle == "" {
		return nil, errResult("SCHEMA_INVALID", "gm_dev_done: prTitle is required")
	}
	if p.PrBody == "" {
		return nil, errResult("SCHEMA_INVALID", "gm_dev_done: prBody is required")
	}
	return &p, nil
}

func (b *Broker) checkDevDoneGate() json.RawMessage {
	b.lastCheckMu.Lock()
	lastCheck := b.lastCheck
	b.lastCheckMu.Unlock()
	if lastCheck == nil {
		return b.rejectDevDoneGate("no prior gm_project_check in this invocation")
	}
	if !lastCheck.OK {
		return b.rejectDevDoneGate("last gm_project_check was not green")
	}
	currentFP, err := b.getComputeFingerprintFn()(b.projectCheck.WorktreePath)
	if err != nil {
		return b.rejectDevDoneGate("failed to compute working-tree fingerprint: " + err.Error())
	}
	if currentFP != lastCheck.WorkingTreeFingerprint {
		return b.rejectDevDoneGate("working tree changed since last gm_project_check")
	}
	b.devDoneMu.Lock()
	b.devDoneFingerprint = currentFP
	b.devDoneMu.Unlock()
	return nil
}

func (b *Broker) rejectDevDoneGate(msg string) json.RawMessage {
	b.devDoneMu.Lock()
	b.devDoneGateRejected = true
	b.devDoneGateMsg = msg
	b.devDoneMu.Unlock()
	return errResult("DEV_GATE", "gm_dev_done: "+msg)
}

// DevDoneResult returns the stored DevDoneParams if gm_dev_done was called
// successfully (gate passed) during this broker's lifetime, and false otherwise.
func (b *Broker) DevDoneResult() (*DevDoneParams, bool) {
	b.devDoneMu.Lock()
	p := b.devDone
	b.devDoneMu.Unlock()
	if p == nil {
		return nil, false
	}
	return p, true
}

// DevDoneTerminalResult returns the stored terminal gm_dev_done response,
// including schema/protocol failures, if any terminal call occurred.
func (b *Broker) DevDoneTerminalResult() (DevDoneTerminalState, bool) {
	b.devDoneMu.Lock()
	defer b.devDoneMu.Unlock()
	if b.devDoneTerminalResult == nil {
		return DevDoneTerminalState{}, false
	}
	return DevDoneTerminalState{
		Result:  cloneRawMessage(b.devDoneTerminalResult),
		Status:  b.devDoneTerminalStatus,
		Message: b.devDoneTerminalMessage,
	}, true
}

// DevDoneFingerprint returns the working-tree fingerprint captured when
// gm_dev_done passed the §10 gate.
func (b *Broker) DevDoneFingerprint() (string, bool) {
	b.devDoneMu.Lock()
	defer b.devDoneMu.Unlock()
	if b.devDone == nil || b.devDoneFingerprint == "" {
		return "", false
	}
	return b.devDoneFingerprint, true
}

// CurrentFingerprint recomputes the broker's configured worktree fingerprint.
// Returns false when the broker is not configured for fingerprinting.
func (b *Broker) CurrentFingerprint() (string, bool) {
	if b.projectCheck.WorktreePath == "" {
		return "", false
	}
	fp, err := b.getComputeFingerprintFn()(b.projectCheck.WorktreePath)
	if err != nil {
		return "", false
	}
	return fp, true
}

// DevDoneGateRejected reports whether gm_dev_done was called but the §10 gate
// rejected it during this broker's lifetime.
func (b *Broker) DevDoneGateRejected() bool {
	b.devDoneMu.Lock()
	defer b.devDoneMu.Unlock()
	return b.devDoneGateRejected
}

// DevDoneTerminalStatus returns the stored terminal gm_dev_done status code.
// Returns false if no terminal call has completed.
func (b *Broker) DevDoneTerminalStatus() (string, bool) {
	b.devDoneMu.Lock()
	defer b.devDoneMu.Unlock()
	if b.devDoneTerminalResult == nil {
		return "", false
	}
	return b.devDoneTerminalStatus, true
}

// DevDoneTerminalMessage returns the stored terminal gm_dev_done message.
// Returns false if no terminal call has completed.
func (b *Broker) DevDoneTerminalMessage() (string, bool) {
	b.devDoneMu.Lock()
	defer b.devDoneMu.Unlock()
	if b.devDoneTerminalResult == nil {
		return "", false
	}
	return b.devDoneTerminalMessage, true
}

// DevDoneGateReason returns the human-readable reason the §10 gate was rejected.
// Returns empty string if the gate was not rejected.
func (b *Broker) DevDoneGateReason() string {
	b.devDoneMu.Lock()
	defer b.devDoneMu.Unlock()
	return b.devDoneGateMsg
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
	if b.projectCheckFn == nil && (b.projectCheck.WorktreePath == "" || b.projectCheck.VerifyCommand == "") {
		return errResult("PROJECT_CHECK_NOT_AVAILABLE", "gm_project_check is not configured for this broker")
	}

	checkFn := b.projectCheckFn
	if checkFn == nil {
		checkFn = runProjectCheck
	}

	res, err := checkFn(b.projectCheck, mode)
	if err != nil {
		return errResult("PROJECT_CHECK_FAILED", err.Error())
	}

	// Record for §10 gate check.
	b.lastCheckMu.Lock()
	b.lastCheck = res
	b.lastCheckMu.Unlock()

	out, _ := json.Marshal(res)
	return json.RawMessage(out)
}

func (b *Broker) getComputeFingerprintFn() func(string) (string, error) {
	if b.computeFingerprintFn != nil {
		return b.computeFingerprintFn
	}
	return fingerprintAfterVerify
}

func runProjectCheck(cfg ProjectCheckConfig, mode string) (*ProjectCheckResult, error) {
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

	return &ProjectCheckResult{
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
	const maxBytes = 32 * 1024
	if out == "" {
		return out
	}
	trimmed := strings.TrimSuffix(out, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= maxLines {
		return capStreamBytes(out, maxBytes)
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
	return capStreamBytes(b.String(), maxBytes)
}

func capStreamBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	half := maxBytes / 2
	headEnd := half
	for headEnd > 0 && !utf8.RuneStart(s[headEnd]) {
		headEnd--
	}
	tailStart := len(s) - half
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	omitted := tailStart - headEnd
	return s[:headEnd] + fmt.Sprintf("\n... <%d bytes truncated> ...\n", omitted) + s[tailStart:]
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

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func errResult(code, message string) json.RawMessage {
	out, _ := json.Marshal(map[string]any{"ok": false, "code": code, "message": message})
	return json.RawMessage(out)
}

func parseDevDoneTerminalResult(result json.RawMessage) (string, string) {
	var decoded struct {
		OK      bool   `json:"ok"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return "", ""
	}
	if decoded.OK {
		return "OK", ""
	}
	return decoded.Code, decoded.Message
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

// PRViewResult is the output of gm_pr_view.
type PRViewResult struct {
	OK           bool            `json:"ok"`
	PR           json.RawMessage `json:"pr"`
	Diff         string          `json:"diff"`
	ChangedFiles json.RawMessage `json:"changedFiles"`
}

func (b *Broker) handlePRView() json.RawMessage {
	if b.reviewer.ReviewerToken == "" || b.reviewer.PRNumber == 0 {
		return errResult("NOT_CONFIGURED", "gm_pr_view is not configured for this broker")
	}

	fetchFn := b.prViewFn
	if fetchFn == nil {
		fetchFn = fetchPRView
	}

	result, err := fetchFn(b.reviewer)
	if err != nil {
		return errResult("FETCH_FAILED", err.Error())
	}
	return result
}

func fetchPRView(cfg ReviewerConfig) (json.RawMessage, error) {
	prNumStr := fmt.Sprintf("%d", cfg.PRNumber)
	env := append(os.Environ(), "GH_TOKEN="+cfg.ReviewerToken)

	// Fetch PR metadata
	prMeta, err := runGHWithEnv(env, cfg.RepoRoot, "pr", "view", prNumStr,
		"--json", "number,title,state,body,author,createdAt,updatedAt,headRefName,baseRefName")
	if err != nil {
		return nil, fmt.Errorf("gh pr view: %w", err)
	}

	// Fetch unified diff
	diff, err := runGHWithEnv(env, cfg.RepoRoot, "pr", "diff", prNumStr)
	if err != nil {
		return nil, fmt.Errorf("gh pr diff: %w", err)
	}

	// Fetch changed files
	filesJSON, err := runGHWithEnv(env, cfg.RepoRoot, "pr", "view", prNumStr,
		"--json", "files", "--jq", ".files")
	if err != nil {
		return nil, fmt.Errorf("gh pr view files: %w", err)
	}

	var changedFiles json.RawMessage
	if err := json.Unmarshal([]byte(filesJSON), &changedFiles); err != nil {
		changedFiles = json.RawMessage("[]")
	}

	out, _ := json.Marshal(PRViewResult{
		OK:           true,
		PR:           json.RawMessage(prMeta),
		Diff:         diff,
		ChangedFiles: changedFiles,
	})
	return json.RawMessage(out), nil
}

func runGHWithEnv(env []string, dir string, args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// RepoTreeParams is the optional input for gm_repo_tree.
type RepoTreeParams struct {
	Path *string `json:"path"`
}

// RepoTreeEntry is one entry in a directory listing.
type RepoTreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "file" or "dir"
}

func (b *Broker) handleRepoTree(raw json.RawMessage) json.RawMessage {
	root := b.reviewer.WorktreePath
	if root == "" {
		return errResult("NOT_CONFIGURED", "gm_repo_tree is not configured for this broker")
	}

	var p RepoTreeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return errResult("SCHEMA_INVALID", "gm_repo_tree: "+err.Error())
	}

	relPath := "."
	if p.Path != nil && *p.Path != "" {
		relPath = *p.Path
	}

	absClean, ok := resolveWorktreePath(root, relPath)
	if !ok {
		return errResult("PATH_OUTSIDE_WORKTREE", "path escapes the worktree root")
	}

	entries, err := os.ReadDir(absClean)
	if err != nil {
		if os.IsNotExist(err) {
			return errResult("NOT_FOUND", "path not found: "+relPath)
		}
		return errResult("READ_FAILED", err.Error())
	}

	displayPath := relPath
	if displayPath == "." {
		displayPath = "/"
	}

	out, _ := json.Marshal(map[string]any{
		"ok":      true,
		"path":    displayPath,
		"entries": buildTreeEntries(entries),
	})
	return json.RawMessage(out)
}

// resolveWorktreePath resolves relPath against root and checks it stays inside root.
// Returns the cleaned absolute path and true on success, or "", false if it escapes.
func resolveWorktreePath(root, relPath string) (string, bool) {
	absClean := filepath.Clean(filepath.Join(root, relPath))
	rootClean := filepath.Clean(root)
	if absClean != rootClean && !strings.HasPrefix(absClean+string(filepath.Separator), rootClean+string(filepath.Separator)) {
		return "", false
	}
	return absClean, true
}

// buildTreeEntries converts os.ReadDir entries into RepoTreeEntry slice.
func buildTreeEntries(entries []os.DirEntry) []RepoTreeEntry {
	result := make([]RepoTreeEntry, 0, len(entries))
	for _, e := range entries {
		t := "file"
		if e.IsDir() {
			t = "dir"
		}
		result = append(result, RepoTreeEntry{Name: e.Name(), Type: t})
	}
	return result
}
