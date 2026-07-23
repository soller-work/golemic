package gmbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// fakeCBMToolSchemas is the schema the fake server returns for tools/list.
// It mirrors the real cbmAllowedSubs property sets for validation testing.
var fakeCBMToolSchemas = map[string][]string{
	"search_code":      {"pattern", "file_pattern", "path_filter", "mode", "context", "regex", "limit", "project"},
	"search_graph":     {"query", "label", "name_pattern", "qn_pattern", "file_pattern", "relationship", "min_degree", "max_degree", "exclude_entry_points", "include_connected", "semantic_query", "limit", "offset", "project"},
	"query_graph":      {"query", "max_rows", "project"},
	"trace_call_path":  {"function_name", "direction", "depth", "mode", "parameter_name", "edge_types", "risk_labels", "include_tests", "project"},
	"get_architecture": {"path", "aspects", "project"},
	"get_graph_schema": {"project"},
	"get_code_snippet": {"qualified_name", "include_neighbors", "project"},
	"detect_changes":   {"since", "path", "project"},
}

// buildFakeToolsListResponse constructs a tools/list JSON-RPC response using fakeCBMToolSchemas.
func buildFakeToolsListResponse(id int64) []byte {
	tools := make([]map[string]interface{}, 0, len(fakeCBMToolSchemas))
	for name, props := range fakeCBMToolSchemas {
		properties := make(map[string]interface{}, len(props))
		for _, p := range props {
			properties[p] = map[string]interface{}{"type": "string"}
		}
		tools = append(tools, map[string]interface{}{
			"name": name,
			"inputSchema": map[string]interface{}{
				"properties": properties,
			},
		})
	}
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]interface{}{"tools": tools},
	}
	out, _ := json.Marshal(resp)
	return append(out, '\n')
}

// startFakeCBMServer starts a minimal unix-socket server that handles both MCP
// tools/list and tools/call requests. It records the last received call name and
// arguments for assertions. tools/list returns fakeCBMToolSchemas.
func startFakeCBMServer(t *testing.T, responseText string) (sockPath string, lastTool *string, lastArgs *map[string]interface{}) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cbmsrv*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck

	sockPath = filepath.Join(dir, "cbm.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	lastTool = new(string)
	lastArgs = new(map[string]interface{})
	go serveFakeCBMConns(ln, responseText, lastTool, lastArgs)
	return sockPath, lastTool, lastArgs
}

func serveFakeCBMConns(ln net.Listener, responseText string, lastTool *string, lastArgs *map[string]interface{}) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleFakeCBMConn(conn, responseText, lastTool, lastArgs)
	}
}

func handleFakeCBMConn(c net.Conn, responseText string, lastTool *string, lastArgs *map[string]interface{}) {
	defer c.Close() //nolint:errcheck
	reader := bufio.NewReaderSize(c, 4<<20)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var req struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &req) != nil {
		return
	}
	if req.Method == "tools/list" {
		c.Write(buildFakeToolsListResponse(req.ID)) //nolint:errcheck
		return
	}
	// tools/call
	*lastTool = req.Params.Name
	*lastArgs = req.Params.Arguments
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result": map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": responseText}},
			"isError": false,
		},
	}
	out, _ := json.Marshal(resp)
	c.Write(append(out, '\n')) //nolint:errcheck
}

// startBrokerWithTools starts a broker with a custom allowed-tools list.
func startBrokerWithTools(t *testing.T, allowedTools []string) (*Broker, string) {
	t.Helper()
	sockPath := filepath.Join(shortTempDir(t), "gm.sock")
	b, err := StartWithFetcherAndProjectCheck(sockPath,
		func(_ context.Context) (string, error) { return "spec", nil },
		ProjectCheckConfig{}, allowedTools)
	if err != nil {
		t.Fatalf("StartWithFetcherAndProjectCheck: %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b, sockPath
}

// TestCodeTool_CBMNotConfigured verifies that gm_code_* tools return CBM_NOT_AVAILABLE
// when no CBM config has been set on the broker.
func TestCodeTool_CBMNotConfigured(t *testing.T) {
	b, sockPath := startBrokerWithTools(t, []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_code_search_graph"})
	_ = b

	result := call(t, sockPath, "gm_code_search_graph", "c1", map[string]any{"query": "handleClient"})
	if result["ok"] != false {
		t.Fatalf("expected ok=false, got: %v", result)
	}
	if result["code"] != "CBM_NOT_AVAILABLE" {
		t.Fatalf("expected code=CBM_NOT_AVAILABLE, got: %v", result["code"])
	}
}

// TestCodeTool_Forward verifies that a valid gm_code_search call is forwarded to the CBM
// socket as search_code (BR-1 canonical name), the response is returned, and project is injected.
// AC: forward.
func TestCodeTool_Forward(t *testing.T) {
	cbmSock, lastTool, lastArgs := startFakeCBMServer(t, "search results here")

	b, sockPath := startBrokerWithTools(t, []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_code_search"})
	b.ConfigureCBM(CBMConfig{SockPath: cbmSock, Project: "my-project"})

	result := call(t, sockPath, "gm_code_search", "c1", map[string]any{"pattern": "handleClient"})

	if result["ok"] != true {
		t.Fatalf("expected ok=true, got: %v", result)
	}
	if result["content"] != "search results here" {
		t.Fatalf("expected content='search results here', got: %v", result["content"])
	}
	if *lastTool != "search_code" {
		t.Fatalf("expected CBM tool name=search_code (not 'search'), got: %q", *lastTool)
	}
	if got := fmt.Sprintf("%v", (*lastArgs)["project"]); got != "my-project" {
		t.Fatalf("expected project=my-project injected, got: %q", got)
	}
}

// TestCodeTool_ProxiesToCBM verifies that a gm_code_search_graph call is forwarded
// to the CBM socket with the project injected and the response text returned.
func TestCodeTool_ProxiesToCBM(t *testing.T) {
	cbmSock, lastTool, lastArgs := startFakeCBMServer(t, "graph results here")

	b, sockPath := startBrokerWithTools(t, []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_code_search_graph"})
	b.ConfigureCBM(CBMConfig{SockPath: cbmSock, Project: "my-project"})

	result := call(t, sockPath, "gm_code_search_graph", "c1", map[string]any{"query": "handleClient"})

	if result["ok"] != true {
		t.Fatalf("expected ok=true, got: %v", result)
	}
	if result["content"] != "graph results here" {
		t.Fatalf("expected content='graph results here', got: %v", result["content"])
	}
	if *lastTool != "search_graph" {
		t.Fatalf("expected CBM tool name=search_graph, got: %q", *lastTool)
	}
	if got := fmt.Sprintf("%v", (*lastArgs)["project"]); got != "my-project" {
		t.Fatalf("expected project=my-project injected in arguments, got: %q", got)
	}
	if got := fmt.Sprintf("%v", (*lastArgs)["query"]); got != "handleClient" {
		t.Fatalf("expected query=handleClient forwarded, got: %q", got)
	}
}

// TestCodeTool_BlockedByAllowlist verifies that gm_code_* tools are rejected when
// not present in the broker's allowed tool set.
func TestCodeTool_BlockedByAllowlist(t *testing.T) {
	_, sockPath := startBrokerWithTools(t, []string{"gm_slice_get"})

	result := call(t, sockPath, "gm_code_search_graph", "c1", map[string]any{})
	if result["ok"] != false {
		t.Fatalf("expected ok=false, got: %v", result)
	}
	if result["code"] != "UNKNOWN_TOOL" {
		t.Fatalf("expected code=UNKNOWN_TOOL, got: %v", result["code"])
	}
}

// TestCodeTool_NameMap verifies that each of the eight gm_code_* names maps 1:1 to its
// correct cbmAllowedSubs entry via the broker. AC: name-map.
func TestCodeTool_NameMap(t *testing.T) {
	// The expected BR-1 fixed map.
	wantMap := map[string]string{
		"gm_code_search":           "search_code",
		"gm_code_search_graph":     "search_graph",
		"gm_code_query_graph":      "query_graph",
		"gm_code_trace_call_path":  "trace_call_path",
		"gm_code_get_architecture": "get_architecture",
		"gm_code_get_graph_schema": "get_graph_schema",
		"gm_code_get_snippet":      "get_code_snippet",
		"gm_code_detect_changes":   "detect_changes",
	}

	cbmSock, lastTool, _ := startFakeCBMServer(t, "ok")
	allowed := append([]string{"gm_slice_get", "gm_project_check", "gm_dev_done"}, keysOf(wantMap)...)
	b, sockPath := startBrokerWithTools(t, allowed)
	b.ConfigureCBM(CBMConfig{SockPath: cbmSock, Project: "proj"})

	for gmTool, wantCBMTool := range wantMap {
		result := call(t, sockPath, gmTool, "c-"+gmTool, map[string]any{})
		if result["ok"] != true {
			t.Errorf("%s: expected ok=true, got: %v", gmTool, result)
			continue
		}
		if *lastTool != wantCBMTool {
			t.Errorf("%s: expected CBM sub=%q, got: %q (TrimPrefix would give %q)", gmTool, wantCBMTool, *lastTool, trimPrefixGMCode(gmTool))
		}
	}
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func trimPrefixGMCode(s string) string {
	const prefix = "gm_code_"
	if len(s) > len(prefix) {
		return s[len(prefix):]
	}
	return s
}

// TestCodeTool_ProjectGuard verifies that a caller-supplied 'project' argument is
// rejected before forwarding. AC: project-guard, BR-3.
func TestCodeTool_ProjectGuard(t *testing.T) {
	cbmSock, lastTool, _ := startFakeCBMServer(t, "should not reach")

	b, sockPath := startBrokerWithTools(t, []string{"gm_code_search"})
	b.ConfigureCBM(CBMConfig{SockPath: cbmSock, Project: "injected-project"})

	result := call(t, sockPath, "gm_code_search", "c1", map[string]any{"pattern": "foo", "project": "evil"})

	if result["ok"] != false {
		t.Fatalf("expected ok=false when project supplied, got: %v", result)
	}
	if result["code"] != "SCHEMA_INVALID" {
		t.Fatalf("expected code=SCHEMA_INVALID, got: %v", result["code"])
	}
	msg, _ := result["message"].(string)
	if msg == "" || !containsStr(msg, "project") {
		t.Fatalf("expected message to mention 'project', got: %q", msg)
	}
	// CBM must not have been called.
	if *lastTool != "" {
		t.Fatalf("CBM was called despite project guard rejection; lastTool=%q", *lastTool)
	}
}

// TestCodeTool_BadArg verifies that an unknown argument name is rejected before
// forwarding via live schema validation. AC: bad-arg, BR-2.
func TestCodeTool_BadArg(t *testing.T) {
	cbmSock, lastTool, _ := startFakeCBMServer(t, "should not reach")

	b, sockPath := startBrokerWithTools(t, []string{"gm_code_search"})
	b.ConfigureCBM(CBMConfig{SockPath: cbmSock, Project: "proj"})

	result := call(t, sockPath, "gm_code_search", "c1", map[string]any{"bogus_field": "foo"})

	if result["ok"] != false {
		t.Fatalf("expected ok=false for unknown arg, got: %v", result)
	}
	if result["code"] != "SCHEMA_INVALID" {
		t.Fatalf("expected code=SCHEMA_INVALID, got: %v", result["code"])
	}
	msg, _ := result["message"].(string)
	if !containsStr(msg, "bogus_field") {
		t.Fatalf("expected message to mention unknown field name, got: %q", msg)
	}
	// CBM tools/call must not have been invoked.
	if *lastTool != "" {
		t.Fatalf("CBM tools/call was invoked despite bad-arg rejection; lastTool=%q", *lastTool)
	}
}

// TestCodeTool_ReadOnly verifies that a gm_code_* call writes no eventlog entry.
// AC: read-only.
func TestCodeTool_ReadOnly(t *testing.T) {
	cbmSock, _, _ := startFakeCBMServer(t, "result")

	b, sockPath := startBrokerWithTools(t, []string{"gm_code_search_graph"})
	b.ConfigureCBM(CBMConfig{SockPath: cbmSock, Project: "proj"})

	result := call(t, sockPath, "gm_code_search_graph", "c1", map[string]any{"query": "foo"})
	if result["ok"] != true {
		t.Fatalf("expected ok=true, got: %v", result)
	}

	// The broker has no eventlog writer — DevDoneResult should be empty.
	if _, ok := b.DevDoneResult(); ok {
		t.Error("read-only tool must not set DevDoneResult")
	}
	if b.DevDoneGateRejected() {
		t.Error("read-only tool must not affect dev gate state")
	}
}

// TestCodeTool_CBMBrokerError verifies that a CBM broker JSON-RPC error is surfaced
// as CBM_CALL_FAILED.
func TestCodeTool_CBMBrokerError(t *testing.T) {
	dir, err := os.MkdirTemp("", "cbmerr*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck

	cbmSockPath := filepath.Join(dir, "cbm.sock")
	ln, err := net.Listen("unix", cbmSockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close() //nolint:errcheck
				reader := bufio.NewReaderSize(c, 4<<20)
				var req struct {
					ID     int64  `json:"id"`
					Method string `json:"method"`
				}
				line, _ := reader.ReadBytes('\n')
				json.Unmarshal(line, &req) //nolint:errcheck
				if req.Method == "tools/list" {
					c.Write(buildFakeToolsListResponse(req.ID)) //nolint:errcheck
					return
				}
				resp := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]interface{}{"code": -32603, "message": "unknown tool"},
				}
				out, _ := json.Marshal(resp)
				c.Write(append(out, '\n')) //nolint:errcheck
			}(conn)
		}
	}()

	b, gmSock := startBrokerWithTools(t, []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_code_search_graph"})
	b.ConfigureCBM(CBMConfig{SockPath: cbmSockPath, Project: "proj"})

	result := call(t, gmSock, "gm_code_search_graph", "c1", map[string]any{})
	if result["ok"] != false {
		t.Fatalf("expected ok=false, got: %v", result)
	}
	if result["code"] != "CBM_CALL_FAILED" {
		t.Fatalf("expected code=CBM_CALL_FAILED, got: %v", result["code"])
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
