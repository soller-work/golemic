package cbmbroker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// fakeChild simulates an MCP stdio child process using in-memory pipes.
// It reads JSON-RPC requests, responds to initialize and tools/call, and
// skips notifications (which have no id).
type fakeChild struct {
	stdin  io.WriteCloser // broker writes here; child reads from the other end
	stdout io.WriteCloser // child writes here; broker reads from the other end

	reader    *bufio.Reader
	mu        sync.Mutex
	responses map[int64][]byte // pre-queued per-id responses; 0 = default
	done      chan struct{}
}

func newFakeChild(t *testing.T) (*fakeChild, io.WriteCloser, *bufio.Reader) {
	t.Helper()

	// childIn: broker → child
	childInR, childInW := io.Pipe()
	// childOut: child → broker
	childOutR, childOutW := io.Pipe()

	fc := &fakeChild{
		stdin:     childInW,
		stdout:    childOutW,
		reader:    bufio.NewReaderSize(childInR, readerBufSize),
		responses: make(map[int64][]byte),
		done:      make(chan struct{}),
	}

	go fc.run()

	t.Cleanup(func() {
		childInW.Close()
		childOutW.Close()
		select {
		case <-fc.done:
		case <-time.After(2 * time.Second):
		}
	})

	brokerStdout := bufio.NewReaderSize(childOutR, readerBufSize)
	return fc, childInW, brokerStdout
}

func (fc *fakeChild) enqueueResponse(id int64, resp map[string]interface{}) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	resp["id"] = id
	resp["jsonrpc"] = "2.0"
	data, _ := json.Marshal(resp)
	fc.responses[id] = data
}

func (fc *fakeChild) run() {
	defer close(fc.done)
	for {
		line, err := fc.reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg struct {
			JSONRPC string `json:"jsonrpc"`
			ID      *int64 `json:"id"`
			Method  string `json:"method"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			return
		}
		if msg.ID == nil {
			// Notification — no response
			continue
		}
		id := *msg.ID

		fc.mu.Lock()
		resp, ok := fc.responses[id]
		if !ok {
			// Default: echo back a simple result
			resp, _ = json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]interface{}{"echo": msg.Method},
			})
		}
		delete(fc.responses, id)
		fc.mu.Unlock()

		fc.stdout.Write(append(resp, '\n')) //nolint:errcheck
	}
}

// shortSockPath returns a unix-socket path short enough for macOS (104-byte limit).
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cbm")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir + "/s.sock"
}

// startTestBroker starts a broker backed by a fake child and returns the broker
// and the socket path.
func startTestBroker(t *testing.T) (*Broker, string) {
	t.Helper()
	sockPath := shortSockPath(t)

	fc, childStdinW, childStdout := newFakeChild(t)

	// Seed initialize response.
	fc.enqueueResponse(1, map[string]interface{}{
		"result": map[string]interface{}{
			"protocolVersion": mcpVersion,
			"capabilities":    map[string]interface{}{},
			"serverInfo":      map[string]interface{}{"name": "fake-mcp", "version": "0.0.1"},
		},
	})

	childDone := make(chan struct{})
	go func() {
		<-fc.done
		close(childDone)
	}()

	b, err := StartWithIO(sockPath, childStdinW, childStdout,
		func(os.Signal) error { childStdinW.Close(); return nil },
		func() error { return nil },
		childDone,
	)
	if err != nil {
		t.Fatalf("StartWithIO: %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b, sockPath
}

// dialAndCall opens a unix connection, sends one JSON-RPC request, and returns
// the raw response line.
func dialAndCall(t *testing.T, sockPath string, req interface{}) []byte {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial %s: %v", sockPath, err)
	}
	defer conn.Close()

	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reader := bufio.NewReaderSize(conn, readerBufSize)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return line
}

func TestBroker_InitializeHandshake(t *testing.T) {
	_, sockPath := startTestBroker(t)
	// If socket exists, handshake succeeded.
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket not created after handshake: %v", err)
	}
}

func TestBroker_InitializeTimeout_TerminatesChild(t *testing.T) { //nolint:funlen
	oldInit := initHandshakeTimeout
	oldGrace := graceShutdown
	initHandshakeTimeout = 25 * time.Millisecond
	graceShutdown = 10 * time.Millisecond
	t.Cleanup(func() {
		initHandshakeTimeout = oldInit
		graceShutdown = oldGrace
	})

	sockPath := shortSockPath(t)
	childInR, childInW := io.Pipe()
	childOutR, childOutW := io.Pipe()
	t.Cleanup(func() {
		childInW.Close()
		childOutW.Close()
	})

	go func() {
		reader := bufio.NewReaderSize(childInR, readerBufSize)
		for {
			if _, err := reader.ReadBytes('\n'); err != nil {
				return
			}
		}
	}()

	childDone := make(chan struct{})
	var mu sync.Mutex
	sigCount := 0
	killCount := 0

	start := time.Now()
	_, err := StartWithIO(sockPath, childInW, bufio.NewReaderSize(childOutR, readerBufSize),
		func(os.Signal) error {
			mu.Lock()
			sigCount++
			mu.Unlock()
			return nil
		},
		func() error {
			mu.Lock()
			killCount++
			mu.Unlock()
			return nil
		},
		childDone,
	)
	if err == nil {
		t.Fatal("expected initialize timeout error")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("StartWithIO took %v, want bounded timeout", elapsed)
	}

	mu.Lock()
	defer mu.Unlock()
	if sigCount == 0 {
		t.Error("expected SIGTERM to be sent on initialize timeout")
	}
	if killCount == 0 {
		t.Error("expected hard kill fallback on initialize timeout")
	}
}

func TestBroker_SingleRequest(t *testing.T) {
	b, sockPath := startTestBroker(t)
	_ = b

	// Re-inject a response for the next request (id=2, since 1 was initialize).
	// We rely on the default echo response from fakeChild.
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "tools/list",
	}
	raw := dialAndCall(t, sockPath, req)

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	// The response id must be rewritten to 99 (client's id).
	var gotID int64
	if err := json.Unmarshal(resp["id"], &gotID); err != nil {
		t.Fatalf("unmarshal id: %v", err)
	}
	if gotID != 99 {
		t.Errorf("response id = %d, want 99", gotID)
	}
}

func TestBroker_ParallelRequests(t *testing.T) {
	_, sockPath := startTestBroker(t)

	const n = 10
	results := make([][]byte, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      i,
				"method":  fmt.Sprintf("tools/call/%d", i),
			}
			results[i] = dialAndCall(t, sockPath, req)
		}()
	}
	wg.Wait()

	for i, raw := range results {
		var resp map[string]json.RawMessage
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Errorf("request %d: unmarshal error: %v", i, err)
			continue
		}
		// Each response must carry back the original client id.
		var gotID int
		if err := json.Unmarshal(resp["id"], &gotID); err != nil {
			t.Errorf("request %d: id not integer: %v", i, err)
			continue
		}
		if gotID != i {
			t.Errorf("request %d: response id = %d, want %d", i, gotID, i)
		}
	}
}

func TestBroker_RoundtripPerformance(t *testing.T) {
	_, sockPath := startTestBroker(t)

	const n = 10
	budget := time.Duration(n) * 50 * time.Millisecond

	start := time.Now()
	for i := 0; i < n; i++ {
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      100 + i,
			"method":  "tools/call",
		}
		dialAndCall(t, sockPath, req)
	}
	elapsed := time.Since(start)
	if elapsed > budget {
		t.Errorf("%d sequential roundtrips took %v, want < %v", n, elapsed, budget)
	}
}

func TestBroker_Shutdown_RemovesSocket(t *testing.T) {
	b, sockPath := startTestBroker(t)
	// Override cleanup so Shutdown isn't called twice.
	t.Cleanup(func() {})

	b.Shutdown()

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file should be removed after Shutdown; stat err = %v", err)
	}
}

func TestBroker_Shutdown_ChildSIGTERM(t *testing.T) { //nolint:funlen
	sockPath := shortSockPath(t)

	childInR, childInW := io.Pipe()
	childOutR, childOutW := io.Pipe()

	// fake child: handle initialize and then signal on sigterm
	terminated := make(chan struct{})
	go func() {
		reader := bufio.NewReaderSize(childInR, 4096)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var msg struct {
				ID     *int64 `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(line, &msg) != nil || msg.ID == nil {
				continue
			}
			resp, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"result":  map[string]interface{}{},
			})
			childOutW.Write(append(resp, '\n')) //nolint:errcheck
		}
	}()

	childDone := make(chan struct{})
	sigTermReceived := false
	var sigMu sync.Mutex

	b, err := StartWithIO(sockPath, childInW, bufio.NewReaderSize(childOutR, readerBufSize),
		func(sig os.Signal) error {
			sigMu.Lock()
			sigTermReceived = true
			sigMu.Unlock()
			childInW.Close()
			childOutW.Close()
			close(childDone)
			close(terminated)
			return nil
		},
		func() error { return nil },
		childDone,
	)
	if err != nil {
		t.Fatalf("StartWithIO: %v", err)
	}

	b.Shutdown()

	select {
	case <-terminated:
	case <-time.After(3 * time.Second):
		t.Error("SIGTERM not sent within timeout")
	}

	sigMu.Lock()
	got := sigTermReceived
	sigMu.Unlock()
	if !got {
		t.Error("SIGTERM not received by child")
	}
}
