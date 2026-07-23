// Package gmbroker runs a per-agent-invocation unix-socket JSON-RPC server that
// handles gm_ tool calls forwarded by the golemic pi extension.
//
// The runner owns the Broker lifecycle: Start before spawning pi, Shutdown after
// the invocation ends. The agent subprocess never holds GitHub credentials; all
// credential-bearing operations happen inside the broker.
package gmbroker

import (
	"bufio"
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
	"time"
	"unicode/utf8"

	"golemic/internal/worktreefingerprint"
)

// IssueFetcher fetches the Markdown body of a GitHub issue.
// Called at most once per Broker instance (lazy cache via sync.Once).
type IssueFetcher func(ctx context.Context) (string, error)

// cbmToolNameMap is the BR-1 fixed 1:1 map from gm_code_* tool name to upstream cbm sub.
// Two names do not match their trimmed form: gm_code_search→search_code, gm_code_get_snippet→get_code_snippet.
var cbmToolNameMap = map[string]string{
	"gm_code_search":           "search_code",
	"gm_code_search_graph":     "search_graph",
	"gm_code_query_graph":      "query_graph",
	"gm_code_trace_call_path":  "trace_call_path",
	"gm_code_get_architecture": "get_architecture",
	"gm_code_get_graph_schema": "get_graph_schema",
	"gm_code_get_snippet":      "get_code_snippet",
	"gm_code_detect_changes":   "detect_changes",
}

// CBMConfig configures the CBM proxy for gm_code_* tools.
type CBMConfig struct {
	SockPath string
	Project  string
}

// ProjectCheckConfig configures gm_project_check for a Dev invocation.
type ProjectCheckConfig struct {
	WorktreePath  string
	VerifyCommand string
	Env           map[string]string
}

// PrecheckState holds the reviewer precheck result injected before an agent invocation.
// It is used by the §12 approval gate in gm_review_submit.
type PrecheckState struct {
	OK                bool
	BeforeFingerprint string
	AfterFingerprint  string
}

// ReviewerConfig configures reviewer tools (gm_pr_view, gm_repo_tree, gm_review_submit_comment,
// gm_review_submit) for a reviewer invocation.
type ReviewerConfig struct {
	WorktreePath  string
	ReviewerToken string
	RepoRoot      string
	PRNumber      int
	Precheck      *PrecheckState // §12 approval gate; nil when no precheck ran
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

	cbmConfig CBMConfig

	// cbmSchema is the tools/list schema fetched from the CBM broker (lazy, cached).
	cbmSchemaOnce sync.Once
	cbmSchemaData map[string]map[string]struct{} // cbm-tool-name → allowed property names
	cbmSchemaErr  error
	// cbmFetchSchemaFn is injectable for tests.
	cbmFetchSchemaFn func(sockPath string) (map[string]map[string]struct{}, error)

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

	// §12/§13 reviewer terminal state (per invocation)
	reviewerMu                  sync.Mutex
	pendingReviewID             string              // Pending Review node ID for this invocation
	reviewSubmit                *ReviewSubmitParams // non-nil when gate passed
	reviewSubmitGateRejected    bool
	reviewSubmitGateMsg         string
	reviewSubmitTerminalCallID  string
	reviewSubmitTerminalRaw     json.RawMessage
	reviewSubmitTerminalResult  json.RawMessage
	reviewSubmitTerminalPending bool

	// Injectable functions — set to non-nil in tests for deterministic behavior.
	projectCheckFn             func(cfg ProjectCheckConfig, mode string) (*ProjectCheckResult, error)
	computeFingerprintFn       func(worktreePath string) (string, error)
	prViewFn                   func(cfg ReviewerConfig) (json.RawMessage, error)
	getOrCreatePendingReviewFn func(cfg ReviewerConfig) (string, error)
	addReviewCommentFn         func(cfg ReviewerConfig, reviewID, path, body string, line int) (commentID, threadID string, anchorInvalid bool, err error)
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

var defaultTools = []string{"gm_slice_get", "gm_dev_done", "gm_review_submit", "gm_review_submit_comment"}

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

// ConfigureCBM sets the CBM broker socket and project for gm_code_* tool proxying.
// Must be called before the agent subprocess is spawned (no concurrent access).
func (b *Broker) ConfigureCBM(cfg CBMConfig) {
	if b == nil {
		return
	}
	b.cbmConfig = cfg
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

// SetGetOrCreatePendingReviewFn replaces the pending-review discover-or-create function.
func (b *Broker) SetGetOrCreatePendingReviewFn(fn func(cfg ReviewerConfig) (string, error)) {
	if b == nil {
		return
	}
	b.getOrCreatePendingReviewFn = fn
}

// SetAddReviewCommentFn replaces the inline-comment add function.
func (b *Broker) SetAddReviewCommentFn(fn func(cfg ReviewerConfig, reviewID, path, body string, line int) (commentID, threadID string, anchorInvalid bool, err error)) {
	if b == nil {
		return
	}
	b.addReviewCommentFn = fn
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
		return b.handleReviewSubmit(req.CallID, req.Params)
	case "gm_review_submit_comment":
		return b.handleReviewSubmitComment(req.Params)
	case "gm_project_check":
		return b.handleProjectCheck(req.Params)
	case "gm_pr_view":
		return b.handlePRView()
	case "gm_repo_tree":
		return b.handleRepoTree(req.Params)
	default:
		if cbmSub, ok := cbmToolNameMap[req.Tool]; ok {
			return b.handleCodeTool(cbmSub, req.Params)
		}
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

// ReviewSubmitCommentParams is the expected payload for gm_review_submit_comment.
type ReviewSubmitCommentParams struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Body     string `json:"body"`
	Severity string `json:"severity"` // optional: "blocking" | "non_blocking"
}

// ReviewSubmitState captures the stored terminal response for gm_review_submit.
type ReviewSubmitState struct {
	Params       *ReviewSubmitParams
	GateRejected bool
	GateMsg      string
}

// handleReviewSubmit is the terminal gm_review_submit handler (§12/§14).
// It validates schema, checks the §12 approval gate for approved verdicts,
// stores the result, and returns {ok:true,accepted:true} on success.
func (b *Broker) handleReviewSubmit(callID string, raw json.RawMessage) json.RawMessage {
	rawCopy := cloneRawMessage(raw)
	if res := b.reserveReviewSubmitTerminal(callID, rawCopy); res != nil {
		return res
	}

	p, res := b.validateReviewSubmitParams(rawCopy)
	if res != nil {
		b.finalizeReviewSubmitTerminal(callID, rawCopy, res)
		return res
	}

	if p.Verdict == "approved" {
		if res := b.checkReviewerGate(); res != nil {
			b.finalizeReviewSubmitTerminal(callID, rawCopy, res)
			return res
		}
	}

	b.reviewerMu.Lock()
	b.reviewSubmit = p
	b.reviewerMu.Unlock()

	out, _ := json.Marshal(map[string]any{"ok": true, "accepted": true})
	result := json.RawMessage(out)
	b.finalizeReviewSubmitTerminal(callID, rawCopy, result)
	return result
}

func (b *Broker) reserveReviewSubmitTerminal(callID string, raw json.RawMessage) json.RawMessage {
	b.reviewerMu.Lock()
	defer b.reviewerMu.Unlock()

	if b.reviewSubmitTerminalResult != nil {
		if b.reviewSubmitTerminalCallID == callID && bytes.Equal(b.reviewSubmitTerminalRaw, raw) {
			return cloneRawMessage(b.reviewSubmitTerminalResult)
		}
		return errResult("PROTOCOL_ERROR", "gm_review_submit: terminal gm_review_submit already called in this invocation")
	}
	if b.reviewSubmitTerminalPending {
		return errResult("PROTOCOL_ERROR", "gm_review_submit: terminal gm_review_submit already in progress")
	}
	b.reviewSubmitTerminalPending = true
	b.reviewSubmitTerminalCallID = callID
	b.reviewSubmitTerminalRaw = cloneRawMessage(raw)
	return nil
}

func (b *Broker) finalizeReviewSubmitTerminal(callID string, raw, result json.RawMessage) {
	b.reviewerMu.Lock()
	b.reviewSubmitTerminalPending = false
	b.reviewSubmitTerminalCallID = callID
	b.reviewSubmitTerminalRaw = cloneRawMessage(raw)
	b.reviewSubmitTerminalResult = cloneRawMessage(result)
	b.reviewerMu.Unlock()
}

func (b *Broker) validateReviewSubmitParams(raw json.RawMessage) (*ReviewSubmitParams, json.RawMessage) {
	var p ReviewSubmitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errResult("SCHEMA_INVALID", "gm_review_submit: "+err.Error())
	}
	if p.Verdict != "approved" && p.Verdict != "changes_requested" {
		return nil, errResult("SCHEMA_INVALID", `gm_review_submit: verdict must be "approved" or "changes_requested"`)
	}
	if p.MergeConfidence != "high" && p.MergeConfidence != "medium" && p.MergeConfidence != "low" {
		return nil, errResult("SCHEMA_INVALID", `gm_review_submit: mergeConfidence must be "high", "medium", or "low"`)
	}
	if p.Body == "" {
		return nil, errResult("SCHEMA_INVALID", "gm_review_submit: body is required")
	}
	return &p, nil
}

// checkReviewerGate validates the §12 approval gate:
// precheck.ok && beforeFingerprint==afterFingerprint && currentFingerprint==afterFingerprint.
func (b *Broker) checkReviewerGate() json.RawMessage {
	b.reviewerMu.Lock()
	precheck := b.reviewer.Precheck
	worktreePath := b.reviewer.WorktreePath
	b.reviewerMu.Unlock()

	if precheck == nil {
		return b.rejectReviewerGate("no precheck result for this invocation; run verify first")
	}
	if !precheck.OK {
		return b.rejectReviewerGate("precheck was not ok (verify failed or tree was mutated)")
	}
	if precheck.BeforeFingerprint != precheck.AfterFingerprint {
		return b.rejectReviewerGate("precheck mutated the working tree")
	}
	if worktreePath == "" {
		return b.rejectReviewerGate("reviewer worktree path not configured")
	}
	currentFP, err := b.getComputeFingerprintFn()(worktreePath)
	if err != nil {
		return b.rejectReviewerGate("failed to compute current working-tree fingerprint: " + err.Error())
	}
	if currentFP != precheck.AfterFingerprint {
		return b.rejectReviewerGate("working tree changed since precheck")
	}
	return nil
}

func (b *Broker) rejectReviewerGate(msg string) json.RawMessage {
	b.reviewerMu.Lock()
	b.reviewSubmitGateRejected = true
	b.reviewSubmitGateMsg = msg
	b.reviewerMu.Unlock()
	return errResult("REVIEWER_GATE", "gm_review_submit: "+msg)
}

// ReviewSubmitResult returns the stored ReviewSubmitParams if gm_review_submit
// was called successfully (gate passed) during this broker's lifetime.
func (b *Broker) ReviewSubmitResult() (*ReviewSubmitParams, bool) {
	b.reviewerMu.Lock()
	p := b.reviewSubmit
	b.reviewerMu.Unlock()
	if p == nil {
		return nil, false
	}
	return p, true
}

// ReviewSubmitGateRejected reports whether gm_review_submit was called but
// the §12 approval gate rejected it during this broker's lifetime.
func (b *Broker) ReviewSubmitGateRejected() bool {
	b.reviewerMu.Lock()
	defer b.reviewerMu.Unlock()
	return b.reviewSubmitGateRejected
}

// ReviewSubmitGateReason returns the human-readable reason the §12 gate was rejected.
func (b *Broker) ReviewSubmitGateReason() string {
	b.reviewerMu.Lock()
	defer b.reviewerMu.Unlock()
	return b.reviewSubmitGateMsg
}

// PendingReviewID returns the GitHub Pending Review node ID created for this
// invocation via gm_review_submit_comment, or empty string if none was created.
func (b *Broker) PendingReviewID() string {
	b.reviewerMu.Lock()
	defer b.reviewerMu.Unlock()
	return b.pendingReviewID
}

// handleReviewSubmitComment handles gm_review_submit_comment (non-terminal, BR-1).
// It creates/reuses a Pending Review for this invocation and adds an inline comment.
func (b *Broker) handleReviewSubmitComment(raw json.RawMessage) json.RawMessage {
	if res := b.rejectReviewSubmitCommentAfterTerminal(); res != nil {
		return res
	}
	p, errRes := b.validateReviewSubmitCommentParams(raw)
	if errRes != nil {
		return errRes
	}
	reviewID, errRes := b.ensurePendingReview()
	if errRes != nil {
		return errRes
	}
	return b.addInlineComment(reviewID, p)
}

func (b *Broker) rejectReviewSubmitCommentAfterTerminal() json.RawMessage {
	b.reviewerMu.Lock()
	defer b.reviewerMu.Unlock()
	if b.reviewSubmit == nil {
		return nil
	}
	return errResult("PROTOCOL_ERROR", "gm_review_submit_comment: terminal gm_review_submit already accepted in this invocation")
}

func (b *Broker) validateReviewSubmitCommentParams(raw json.RawMessage) (*ReviewSubmitCommentParams, json.RawMessage) {
	var p ReviewSubmitCommentParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errResult("SCHEMA_INVALID", "gm_review_submit_comment: "+err.Error())
	}
	if p.Path == "" {
		return nil, errResult("SCHEMA_INVALID", "gm_review_submit_comment: path is required")
	}
	if p.Line == 0 {
		return nil, errResult("SCHEMA_INVALID", "gm_review_submit_comment: line is required")
	}
	if p.Body == "" {
		return nil, errResult("SCHEMA_INVALID", "gm_review_submit_comment: body is required")
	}
	return &p, nil
}

func (b *Broker) ensurePendingReview() (string, json.RawMessage) {
	b.reviewerMu.Lock()
	reviewID := b.pendingReviewID
	cfg := b.reviewer
	b.reviewerMu.Unlock()
	if reviewID != "" {
		return reviewID, nil
	}
	getOrCreate := b.getOrCreatePendingReviewFn
	if getOrCreate == nil {
		getOrCreate = getOrCreatePendingReview
	}
	id, err := getOrCreate(cfg)
	if err != nil {
		return "", errResult("GITHUB_ERROR", "gm_review_submit_comment: "+err.Error())
	}
	b.reviewerMu.Lock()
	b.pendingReviewID = id
	b.reviewerMu.Unlock()
	return id, nil
}

func (b *Broker) addInlineComment(reviewID string, p *ReviewSubmitCommentParams) json.RawMessage {
	b.reviewerMu.Lock()
	cfg := b.reviewer
	b.reviewerMu.Unlock()
	addComment := b.addReviewCommentFn
	if addComment == nil {
		addComment = addReviewComment
	}
	commentID, threadID, anchorInvalid, err := addComment(cfg, reviewID, p.Path, p.Body, p.Line)
	if err != nil {
		return errResult("GITHUB_ERROR", "gm_review_submit_comment: "+err.Error())
	}
	if anchorInvalid {
		out, _ := json.Marshal(map[string]any{
			"ok": false, "code": "ANCHOR_INVALID",
			"message": "line is not commentable in the current PR diff",
			"path":    p.Path, "line": p.Line,
		})
		return json.RawMessage(out)
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "commentId": commentID, "threadId": threadID})
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

	mode, errRes := projectCheckMode(p.Output)
	if errRes != nil {
		return errRes
	}

	checkFn, errRes := b.projectCheckFnOrError()
	if errRes != nil {
		return errRes
	}

	res, err := checkFn(b.projectCheck, mode)
	if err != nil {
		return errResult("PROJECT_CHECK_FAILED", err.Error())
	}

	b.lastCheckMu.Lock()
	b.lastCheck = res
	b.lastCheckMu.Unlock()

	out, _ := json.Marshal(res)
	return json.RawMessage(out)
}

func projectCheckMode(output *string) (string, json.RawMessage) {
	mode := "capped"
	if output != nil && *output != "" {
		mode = *output
	}
	if mode != "capped" && mode != "full" {
		return "", errResult("SCHEMA_INVALID", `gm_project_check: output must be "capped" or "full"`)
	}
	return mode, nil
}

func (b *Broker) projectCheckFnOrError() (func(ProjectCheckConfig, string) (*ProjectCheckResult, error), json.RawMessage) {
	if b.projectCheckFn == nil && (b.projectCheck.WorktreePath == "" || b.projectCheck.VerifyCommand == "") {
		return nil, errResult("PROJECT_CHECK_NOT_AVAILABLE", "gm_project_check is not configured for this broker")
	}
	checkFn := b.projectCheckFn
	if checkFn == nil {
		checkFn = runProjectCheck
	}
	return checkFn, nil
}

func (b *Broker) getComputeFingerprintFn() func(string) (string, error) {
	if b.computeFingerprintFn != nil {
		return b.computeFingerprintFn
	}
	return fingerprintAfterVerify
}

// SetCBMFetchSchemaFn replaces the schema-fetch function (injectable for tests).
func (b *Broker) SetCBMFetchSchemaFn(fn func(sockPath string) (map[string]map[string]struct{}, error)) {
	if b == nil {
		return
	}
	b.cbmFetchSchemaFn = fn
}

// getCBMSchema returns the cached tools/list schema for the CBM broker.
// Fetch failures degrade gracefully (BR-7): returns nil, err and callers skip validation.
func (b *Broker) getCBMSchema(sockPath string) (map[string]map[string]struct{}, error) {
	b.cbmSchemaOnce.Do(func() {
		fn := b.cbmFetchSchemaFn
		if fn == nil {
			fn = fetchCBMSchemaFromSocket
		}
		b.cbmSchemaData, b.cbmSchemaErr = fn(sockPath)
	})
	return b.cbmSchemaData, b.cbmSchemaErr
}

// fetchCBMSchemaFromSocket sends a tools/list MCP request and returns a map of
// tool-name → set of allowed property names.
func fetchCBMSchemaFromSocket(sockPath string) (map[string]map[string]struct{}, error) {
	reqData, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tools/list: %w", err)
	}
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial CBM socket for schema: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	if _, err := conn.Write(append(reqData, '\n')); err != nil {
		return nil, fmt.Errorf("write tools/list: %w", err)
	}
	reader := bufio.NewReaderSize(conn, 4<<20)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read tools/list response: %w", err)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("parse tools/list: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}
	result := make(map[string]map[string]struct{}, len(resp.Result.Tools))
	for _, t := range resp.Result.Tools {
		props := make(map[string]struct{}, len(t.InputSchema.Properties))
		for k := range t.InputSchema.Properties {
			props[k] = struct{}{}
		}
		result[t.Name] = props
	}
	return result, nil
}

// handleCodeTool proxies a resolved cbm sub to the configured CBM broker socket.
// cbmSub is already resolved from the BR-1 fixed map (never a raw trimmed name).
func (b *Broker) handleCodeTool(cbmSub string, params json.RawMessage) json.RawMessage {
	cfg := b.cbmConfig
	if cfg.SockPath == "" {
		return errResult("CBM_NOT_AVAILABLE", "gm_code: codebase memory not configured for this invocation")
	}
	rawArgs, errMsg := b.parseAndValidateCodeArgs(cbmSub, cfg.SockPath, params)
	if errMsg != nil {
		return errMsg
	}
	projectJSON, _ := json.Marshal(cfg.Project)
	rawArgs["project"] = projectJSON
	args := decodeRawArgs(rawArgs)
	texts, isError, err := cbmProxyCall(cfg.SockPath, cbmSub, args)
	if err != nil {
		return errResult("CBM_CALL_FAILED", "gm_code: "+err.Error())
	}
	content := strings.Join(texts, "")
	if isError {
		return errResult("CBM_TOOL_ERROR", "gm_code: "+content)
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "content": content})
	return json.RawMessage(out)
}

// parseAndValidateCodeArgs parses params, enforces BR-3 (no caller project) and BR-2
// (args against live schema). Returns parsed args or a non-nil error result.
func (b *Broker) parseAndValidateCodeArgs(cbmSub, sockPath string, params json.RawMessage) (map[string]json.RawMessage, json.RawMessage) {
	var rawArgs map[string]json.RawMessage
	if len(params) > 0 && !bytes.Equal(params, json.RawMessage("null")) {
		if err := json.Unmarshal(params, &rawArgs); err != nil {
			return nil, errResult("SCHEMA_INVALID", "gm_code: invalid params: "+err.Error())
		}
	}
	if rawArgs == nil {
		rawArgs = make(map[string]json.RawMessage)
	}
	if _, hasProject := rawArgs["project"]; hasProject {
		return nil, errResult("SCHEMA_INVALID", "gm_code: 'project' must not be supplied by caller (injected automatically)")
	}
	if errMsg := b.validateArgsAgainstSchema(cbmSub, sockPath, rawArgs); errMsg != nil {
		return nil, errMsg
	}
	return rawArgs, nil
}

// validateArgsAgainstSchema checks caller args against the live CBM tools/list schema (BR-2).
// Returns a non-nil error result on validation failure; nil on success or schema unavailability.
func (b *Broker) validateArgsAgainstSchema(cbmSub, sockPath string, rawArgs map[string]json.RawMessage) json.RawMessage {
	schema, err := b.getCBMSchema(sockPath)
	if err != nil || schema == nil {
		return nil // graceful degradation (BR-7)
	}
	allowed, ok := schema[cbmSub]
	if !ok {
		return nil // tool not in schema: skip validation
	}
	for k := range rawArgs {
		if _, ok := allowed[k]; !ok {
			return errResult("SCHEMA_INVALID", fmt.Sprintf("gm_code: unknown argument %q for %s", k, cbmSub))
		}
	}
	return nil
}

// decodeRawArgs converts a JSON raw-message map to interface{} values for MCP forwarding.
func decodeRawArgs(rawArgs map[string]json.RawMessage) map[string]interface{} {
	arguments := make(map[string]interface{}, len(rawArgs))
	for k, v := range rawArgs {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			arguments[k] = string(v)
		} else {
			arguments[k] = val
		}
	}
	return arguments
}

type cbmContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cbmToolsCallResult struct {
	Content []cbmContent `json:"content"`
	IsError bool         `json:"isError"`
}

type cbmResponse struct {
	Result *cbmToolsCallResult `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// cbmProxyCall makes one MCP tools/call request to the CBM broker unix socket and
// returns the text content items, the isError flag, and any transport error.
func cbmProxyCall(sockPath, toolName string, arguments map[string]interface{}) ([]string, bool, error) {
	data, err := cbmMarshalRequest(toolName, arguments)
	if err != nil {
		return nil, false, err
	}
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, false, fmt.Errorf("dial CBM socket: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	if err := conn.SetDeadline(time.Now().Add(90 * time.Second)); err != nil {
		return nil, false, fmt.Errorf("set deadline: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return nil, false, fmt.Errorf("write request: %w", err)
	}
	return cbmReadResponse(conn)
}

func cbmMarshalRequest(toolName string, arguments map[string]interface{}) ([]byte, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]interface{}{"name": toolName, "arguments": arguments},
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	return append(data, '\n'), nil
}

func cbmReadResponse(conn net.Conn) ([]string, bool, error) {
	reader := bufio.NewReaderSize(conn, 4<<20)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, false, fmt.Errorf("read response: %w", err)
	}
	var resp cbmResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, false, fmt.Errorf("parse response: %w", err)
	}
	if resp.Error != nil {
		return nil, false, fmt.Errorf("CBM broker error: %s", resp.Error.Message)
	}
	if resp.Result == nil {
		return nil, false, fmt.Errorf("empty CBM response")
	}
	texts := make([]string, 0, len(resp.Result.Content))
	for _, c := range resp.Result.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	return texts, resp.Result.IsError, nil
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

// ---------------------------------------------------------------------------
// Reviewer GitHub API helpers
// ---------------------------------------------------------------------------

// graphqlDiscoverOrCreateReview queries viewer login, PR node ID, and pending reviews.
const graphqlDiscoverOrCreateReview = `query($owner:String!,$name:String!,$prNumber:Int!){viewer{login}repository(owner:$owner,name:$name){pullRequest(number:$prNumber){id reviews(first:10,states:[PENDING]){nodes{id author{login}}}}}}` //nolint:lll

// graphqlCreatePendingReview creates an empty pending review on a PR.
const graphqlCreatePendingReview = `mutation($prId:ID!){addPullRequestReview(input:{pullRequestId:$prId}){pullRequestReview{id}}}` //nolint:lll

// graphqlAddReviewThread adds an inline thread to a pending review (RIGHT side).
const graphqlAddReviewThreadBroker = `mutation($reviewId:ID!,$path:String!,$line:Int!,$side:DiffSide!,$body:String!){addPullRequestReviewThread(input:{pullRequestReviewId:$reviewId,path:$path,line:$line,side:$side,body:$body}){thread{id}}}` //nolint:lll

// getOrCreatePendingReview discovers the viewer's existing PENDING review or creates one.
func getOrCreatePendingReview(cfg ReviewerConfig) (string, error) {
	env := append(os.Environ(), "GH_TOKEN="+cfg.ReviewerToken)

	out, err := runGHWithEnv(env, cfg.RepoRoot, "api", "graphql",
		"-f", fmt.Sprintf("query=%s", graphqlDiscoverOrCreateReview),
		"-f", "owner="+ownerFromNWO(cfg.RepoRoot, cfg.ReviewerToken),
		"-f", "name="+nameFromNWO(cfg.RepoRoot, cfg.ReviewerToken),
		"-F", fmt.Sprintf("prNumber=%d", cfg.PRNumber),
	)
	if err != nil {
		return "", fmt.Errorf("discover pending review: %w", err)
	}

	var discoverResp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Repository struct {
				PullRequest struct {
					ID      string `json:"id"`
					Reviews struct {
						Nodes []struct {
							ID     string `json:"id"`
							Author struct {
								Login string `json:"login"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &discoverResp); err != nil {
		return "", fmt.Errorf("parse discover response: %w", err)
	}
	viewerLogin := discoverResp.Data.Viewer.Login
	prID := discoverResp.Data.Repository.PullRequest.ID
	if prID == "" {
		return "", fmt.Errorf("PR #%d not found", cfg.PRNumber)
	}
	for _, node := range discoverResp.Data.Repository.PullRequest.Reviews.Nodes {
		if node.Author.Login == viewerLogin {
			return node.ID, nil
		}
	}
	return createPendingReview(env, cfg.RepoRoot, prID)
}

func createPendingReview(env []string, repoRoot, prID string) (string, error) {
	out, err := runGHWithEnv(env, repoRoot, "api", "graphql",
		"-f", fmt.Sprintf("query=%s", graphqlCreatePendingReview),
		"-f", "prId="+prID,
	)
	if err != nil {
		return "", fmt.Errorf("create pending review: %w", err)
	}
	var resp struct {
		Data struct {
			AddPullRequestReview struct {
				PullRequestReview struct {
					ID string `json:"id"`
				} `json:"pullRequestReview"`
			} `json:"addPullRequestReview"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", fmt.Errorf("parse create review response: %w", err)
	}
	id := resp.Data.AddPullRequestReview.PullRequestReview.ID
	if id == "" {
		return "", fmt.Errorf("create pending review returned empty id")
	}
	return id, nil
}

// addReviewComment adds an inline thread to the pending review.
// Returns anchorInvalid=true (and no error) when the line is not in the PR diff.
func addReviewComment(cfg ReviewerConfig, reviewID, path, body string, line int) (commentID, threadID string, anchorInvalid bool, err error) {
	env := append(os.Environ(), "GH_TOKEN="+cfg.ReviewerToken)
	out, runErr := runGHWithEnv(env, cfg.RepoRoot, "api", "graphql",
		"-f", fmt.Sprintf("query=%s", graphqlAddReviewThreadBroker),
		"-f", "reviewId="+reviewID,
		"-f", "path="+path,
		"-F", fmt.Sprintf("line=%d", line),
		"-f", "side=RIGHT",
		"-f", "body="+body,
	)
	if runErr != nil {
		if isReviewAnchorError(runErr.Error() + out) {
			return "", "", true, nil
		}
		return "", "", false, fmt.Errorf("add review thread: %w", runErr)
	}

	var resp struct {
		Data struct {
			AddPullRequestReviewThread struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			} `json:"addPullRequestReviewThread"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return "", "", false, fmt.Errorf("parse add thread response: %w", err)
	}
	if len(resp.Errors) > 0 {
		msg := resp.Errors[0].Message
		if isReviewAnchorError(msg) {
			return "", "", true, nil
		}
		return "", "", false, fmt.Errorf("add review thread: %s", msg)
	}
	tid := resp.Data.AddPullRequestReviewThread.Thread.ID
	return tid, tid, false, nil
}

// isReviewAnchorError returns true when the error indicates the line is not in the diff.
func isReviewAnchorError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "part of the diff") ||
		strings.Contains(lower, "pull request review thread") ||
		strings.Contains(lower, "not part of")
}

// ownerFromNWO extracts the owner from "owner/name" via gh repo view.
// Falls back to empty string on error (caller will get a useful error from GitHub API).
func ownerFromNWO(repoRoot, token string) string {
	owner, _ := repoOwnerName(repoRoot, token)
	return owner
}

func nameFromNWO(repoRoot, token string) string {
	_, name := repoOwnerName(repoRoot, token)
	return name
}

func repoOwnerName(repoRoot, token string) (owner, name string) {
	env := append(os.Environ(), "GH_TOKEN="+token)
	out, err := runGHWithEnv(env, repoRoot, "repo", "view", "--json", "owner,name")
	if err != nil {
		return "", ""
	}
	var v struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", ""
	}
	return v.Owner.Login, v.Name
}
