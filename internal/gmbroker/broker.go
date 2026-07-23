// Package gmbroker runs a per-agent-invocation unix-socket JSON-RPC server that
// handles the three gm_ tool calls forwarded by the golemic pi extension:
// gm_slice_get, gm_dev_done, and gm_review_submit.
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
)

// IssueFetcher fetches the Markdown body of a GitHub issue.
// Called at most once per Broker instance (lazy cache via sync.Once).
type IssueFetcher func(ctx context.Context) (string, error)

// Broker listens on a unix socket and dispatches gm_ tool calls.
type Broker struct {
	sockPath string
	listener net.Listener
	fetcher  IssueFetcher

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
		sockPath: sockPath,
		listener: ln,
		fetcher:  fetcher,
	}
	go b.acceptLoop()
	return b, nil
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
	switch req.Tool {
	case "gm_slice_get":
		return b.handleSliceGet()
	case "gm_dev_done":
		return handleDevDone(req.Params)
	case "gm_review_submit":
		return handleReviewSubmit(req.Params)
	default:
		return errResult("UNKNOWN_TOOL", "unknown tool: "+req.Tool)
	}
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
