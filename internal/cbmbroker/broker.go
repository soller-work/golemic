// Package cbmbroker manages a long-lived codebase-memory-mcp stdio process and
// exposes it as a unix-socket JSON-RPC multiplexer for golemic cbm clients.
package cbmbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	mcpVersion           = "2024-11-05"
	initHandshakeTimeout = 30 * time.Second
	roundtripTimeout     = 60 * time.Second
	graceShutdown        = 2 * time.Second
	readerBufSize        = 4 << 20 // 4 MiB — MCP responses can be large
)

// Broker holds a long-lived MCP child process and serializes JSON-RPC calls to
// it from multiple concurrent unix-socket clients.
type Broker struct {
	sockPath  string
	listener  net.Listener
	stdin     io.WriteCloser
	sigSend   func(os.Signal) error
	hardKill  func() error
	childDone <-chan struct{} // closed when child process exits

	mu      sync.Mutex // protects pending map only
	stdinMu sync.Mutex // serializes writes to child stdin
	pending map[int64]chan []byte
	nextID  int64 // starts at 1; initialize uses 1, clients start from 2

	dead chan struct{} // closed when readResponses goroutine exits
}

// Start spawns npx codebase-memory-mcp@0.9.0 in stdio MCP server mode,
// completes the initialize handshake, and begins accepting JSON-RPC clients
// on sockPath. env is merged into the child's environment (existing OS env is
// preserved; keys in env override duplicates).
func Start(sockPath string, env map[string]string) (*Broker, error) {
	cmd := exec.Command("npx", "-y", "codebase-memory-mcp@0.9.0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("cbmbroker: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cbmbroker: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cbmbroker: start npx: %w", err)
	}

	childDone := make(chan struct{})
	go func() {
		cmd.Wait() //nolint:errcheck
		close(childDone)
	}()

	stdout := bufio.NewReaderSize(stdoutPipe, readerBufSize)

	signalGroup := func(sig os.Signal) error {
		s, ok := sig.(syscall.Signal)
		if !ok {
			return fmt.Errorf("unsupported signal type %T", sig)
		}
		return syscall.Kill(-cmd.Process.Pid, s)
	}

	b, err := StartWithIO(sockPath, stdin, stdout,
		signalGroup,
		func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) },
		childDone,
	)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// StartWithIO is the internal constructor used by tests: it accepts pre-built IO
// instead of spawning a real npx process.
func StartWithIO(
	sockPath string,
	stdin io.WriteCloser,
	stdout *bufio.Reader,
	sigSend func(os.Signal) error,
	hardKill func() error,
	childDone <-chan struct{},
) (*Broker, error) {
	b := &Broker{
		sockPath:  sockPath,
		stdin:     stdin,
		sigSend:   sigSend,
		hardKill:  hardKill,
		childDone: childDone,
		pending:   make(map[int64]chan []byte),
		nextID:    1, // 1 is reserved for initialize; clients start from 2
		dead:      make(chan struct{}),
	}

	// Response router must start before the handshake so it can route the
	// initialize response.
	go b.readResponses(stdout)

	if err := b.initialize(); err != nil {
		b.stdin.Close() //nolint:errcheck
		b.terminateChild()
		return nil, fmt.Errorf("cbmbroker: initialize: %w", err)
	}

	sockDir := filepath.Dir(sockPath)
	if err := os.MkdirAll(sockDir, 0700); err != nil {
		b.stdin.Close() //nolint:errcheck
		b.terminateChild()
		return nil, fmt.Errorf("cbmbroker: mkdir socket dir: %w", err)
	}
	if err := os.Chmod(sockDir, 0700); err != nil {
		b.stdin.Close() //nolint:errcheck
		b.terminateChild()
		return nil, fmt.Errorf("cbmbroker: chmod socket dir: %w", err)
	}
	os.Remove(sockPath) //nolint:errcheck
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		b.stdin.Close() //nolint:errcheck
		b.terminateChild()
		return nil, fmt.Errorf("cbmbroker: listen %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0600); err != nil {
		ln.Close()          //nolint:errcheck
		os.Remove(sockPath) //nolint:errcheck
		b.stdin.Close()     //nolint:errcheck
		b.terminateChild()
		return nil, fmt.Errorf("cbmbroker: chmod socket: %w", err)
	}
	b.listener = ln

	go b.acceptLoop()
	return b, nil
}

// Shutdown stops the broker: closes the listener, signals the child (SIGTERM →
// grace period → SIGKILL), and removes the socket file. Safe to call from defer.
func (b *Broker) Shutdown() {
	if b.listener != nil {
		b.listener.Close() //nolint:errcheck
	}
	b.terminateChild()
	os.Remove(b.sockPath) //nolint:errcheck
}

// readResponses reads JSON-RPC responses from the child's stdout and routes
// each one to the pending channel registered for that ID. Notifications (no
// id field) are silently dropped. When the child pipe closes this goroutine
// drains all pending waiters with an error response and closes b.dead.
func (b *Broker) readResponses(stdout *bufio.Reader) {
	defer b.failPending()

	for {
		line, err := stdout.ReadBytes('\n')
		if err != nil {
			return
		}
		id, ok := responseID(line)
		if !ok {
			continue
		}
		if ch := b.takePending(id); ch != nil {
			select {
			case ch <- line:
			default:
			}
		}
	}
}

func (b *Broker) failPending() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.pending {
		errResp, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"error":   map[string]interface{}{"code": -32603, "message": "cbm: MCP child exited"},
		})
		select {
		case ch <- errResp:
		default:
		}
		delete(b.pending, id)
	}
	close(b.dead)
}

func (b *Broker) terminateChild() {
	if b == nil {
		return
	}
	if b.stdin != nil {
		b.stdin.Close() //nolint:errcheck
	}
	if b.sigSend != nil {
		b.sigSend(syscall.SIGTERM) //nolint:errcheck
	}
	if waitForDone(b.childDone, graceShutdown) {
		return
	}
	if b.hardKill != nil {
		b.hardKill() //nolint:errcheck
	}
	waitForDone(b.childDone, graceShutdown)
}

func waitForDone(done <-chan struct{}, timeout time.Duration) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func responseID(line []byte) (int64, bool) {
	var msg struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.ID == nil {
		return 0, false
	}
	return *msg.ID, true
}

func (b *Broker) takePending(id int64) chan []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	return ch
}

// initialize sends the MCP initialize request and notifications/initialized.
func (b *Broker) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), initHandshakeTimeout)
	defer cancel()

	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": mcpVersion,
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "golemic", "version": "1.0"},
		},
	}

	_, err := b.sendAndWait(ctx, 1, initReq)
	if err != nil {
		return err
	}

	notification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	data, _ := json.Marshal(notification)
	data = append(data, '\n')
	b.stdinMu.Lock()
	_, writeErr := b.stdin.Write(data)
	b.stdinMu.Unlock()
	return writeErr
}

// sendAndWait writes a JSON-RPC request to the child stdin and waits for the
// response with the matching id. The caller must supply both the int id and the
// request body (which must already contain that id).
func (b *Broker) sendAndWait(ctx context.Context, id int64, req interface{}) ([]byte, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	replyCh := make(chan []byte, 1)
	// Register pending channel before writing to stdin so the response can be
	// routed even if it arrives before we start waiting.
	b.mu.Lock()
	b.pending[id] = replyCh
	b.mu.Unlock()

	// Write to child stdin outside the pending mutex to avoid deadlock: if we
	// held mu while writing, readResponses could not acquire mu to route the
	// response, blocking the child write, blocking us.
	b.stdinMu.Lock()
	_, writeErr := b.stdin.Write(data)
	b.stdinMu.Unlock()
	if writeErr != nil {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("write to child: %w", writeErr)
	}

	select {
	case resp := <-replyCh:
		return resp, nil
	case <-b.dead:
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("MCP child exited")
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, ctx.Err()
	}
}

// acceptLoop accepts client connections and dispatches each to handleClient.
func (b *Broker) acceptLoop() {
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go b.handleClient(conn)
	}
}

// handleClient reads one JSON-RPC request from conn, proxies it to the MCP
// child with an internal ID, waits for the response, rewrites the ID back to
// the client's original ID, and closes the connection.
func (b *Broker) handleClient(conn net.Conn) {
	defer conn.Close()

	line, err := readJSONLine(conn)
	if err != nil {
		return
	}

	clientID, err := clientRequestID(line)
	if err != nil {
		writeErrorToConn(conn, nil, -32700, "parse error")
		return
	}

	internalID := atomic.AddInt64(&b.nextID, 1)
	rewritten, err := rewriteRequestID(line, internalID)
	if err != nil {
		writeErrorToConn(conn, clientID, -32603, "internal error")
		return
	}

	replyCh, err := b.registerPending(internalID)
	if err != nil {
		writeErrorToConn(conn, clientID, -32603, err.Error())
		return
	}

	if err := b.writeChildRequest(rewritten); err != nil {
		b.removePending(internalID)
		writeErrorToConn(conn, clientID, -32603, "cbm: write to MCP child failed")
		return
	}

	respLine, err := b.waitForResponse(replyCh)
	if err != nil {
		b.removePending(internalID)
		writeErrorToConn(conn, clientID, -32603, err.Error())
		return
	}

	if out, ok := rewriteResponseID(respLine, clientID); ok {
		conn.Write(out) //nolint:errcheck
		return
	}
	conn.Write(respLine) //nolint:errcheck
}

func readJSONLine(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReaderSize(conn, readerBufSize)
	return reader.ReadBytes('\n')
}

func clientRequestID(line []byte) (json.RawMessage, error) {
	var clientMsg struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &clientMsg); err != nil {
		return nil, err
	}
	return clientMsg.ID, nil
}

func rewriteRequestID(line []byte, id int64) ([]byte, error) {
	var reqMap map[string]json.RawMessage
	if err := json.Unmarshal(line, &reqMap); err != nil {
		return nil, err
	}
	internalIDBytes, _ := json.Marshal(id)
	reqMap["id"] = internalIDBytes
	rewritten, err := json.Marshal(reqMap)
	if err != nil {
		return nil, err
	}
	return append(rewritten, '\n'), nil
}

func (b *Broker) registerPending(id int64) (chan []byte, error) {
	replyCh := make(chan []byte, 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	select {
	case <-b.dead:
		return nil, fmt.Errorf("cbm: MCP broker not available")
	default:
		b.pending[id] = replyCh
		return replyCh, nil
	}
}

func (b *Broker) removePending(id int64) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

func (b *Broker) writeChildRequest(req []byte) error {
	b.stdinMu.Lock()
	_, err := b.stdin.Write(req)
	b.stdinMu.Unlock()
	return err
}

func (b *Broker) waitForResponse(replyCh chan []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), roundtripTimeout)
	defer cancel()

	select {
	case respLine := <-replyCh:
		return respLine, nil
	case <-b.dead:
		return nil, fmt.Errorf("cbm: MCP broker died")
	case <-ctx.Done():
		return nil, fmt.Errorf("cbm: request timed out")
	}
}

func rewriteResponseID(respLine []byte, clientID json.RawMessage) ([]byte, bool) {
	var respMap map[string]json.RawMessage
	if err := json.Unmarshal(respLine, &respMap); err != nil {
		return nil, false
	}
	respMap["id"] = clientID
	out, err := json.Marshal(respMap)
	if err != nil {
		return nil, false
	}
	return append(out, '\n'), true
}

func writeErrorToConn(conn net.Conn, id json.RawMessage, code int, msg string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      nil,
		"error":   map[string]interface{}{"code": code, "message": msg},
	}
	if id != nil {
		resp["id"] = id
	}
	out, _ := json.Marshal(resp)
	out = append(out, '\n')
	conn.Write(out) //nolint:errcheck
}
