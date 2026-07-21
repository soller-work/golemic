package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSocketServer starts a unix-socket server that replays one canned response per
// connection.  It returns the socket path and a channel that receives every raw
// request line the server received.
func fakeSocketServer(t *testing.T, handler func(req []byte) []byte) string {
	t.Helper()
	// Use os.MkdirTemp with a short base to stay under the 104-byte Unix socket limit.
	dir, err := os.MkdirTemp("", "cbm")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReaderSize(c, 4<<20)
				line, err := reader.ReadBytes('\n')
				if err != nil {
					return
				}
				resp := handler(bytes.TrimSpace(line))
				c.Write(append(resp, '\n')) //nolint:errcheck
			}(conn)
		}
	}()
	return sockPath
}

// simpleMCPResponse builds a tools/call success response with the given text.
func simpleMCPResponse(id int64, text string) []byte {
	resp, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]interface{}{
			"content": []map[string]string{{"type": "text", "text": text}},
			"isError": false,
		},
	})
	return resp
}

// toolsListResponse builds a tools/list response with the given tools.
func toolsListResponse(id int64, tools []map[string]interface{}) []byte {
	resp, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]interface{}{"tools": tools},
	})
	return resp
}

// setupFakeToolsList sets up the process-level schema cache with the given tools.
func setupFakeToolsList(t *testing.T, tools []toolEntry) {
	t.Helper()
	orig := toolsListCache
	toolsListCache = tools
	t.Cleanup(func() { toolsListCache = orig })
}

// setupFakeHelpFetch injects a fake cbmFetchToolsListDirectFn and restores after test.
func setupFakeHelpFetch(t *testing.T, tools []toolEntry) {
	t.Helper()
	orig := cbmFetchToolsListDirectFn
	cbmFetchToolsListDirectFn = func() ([]toolEntry, error) { return tools, nil }
	t.Cleanup(func() { cbmFetchToolsListDirectFn = orig })
}

// ---- Guard tests ----

func TestCBM_NoArgs_PrintsUsage(t *testing.T) {
	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm"}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Error("expected non-zero exit for missing sub")
	}
	if !strings.Contains(stderr.String(), "help") {
		t.Errorf("expected usage hint in stderr; got: %s", stderr.String())
	}
}

func TestCBM_UnknownSub_Exit1(t *testing.T) {
	t.Setenv("CBM_SOCK", "")
	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "bogus_tool"}, &bytes.Buffer{}, &stderr)
	if code != 1 {
		t.Errorf("expected exit 1 for unknown sub; got %d", code)
	}
	if !strings.Contains(stderr.String(), "bogus_tool") {
		t.Errorf("stderr missing unknown sub name; got: %s", stderr.String())
	}
	for _, s := range cbmAllowedSubs {
		if !strings.Contains(stderr.String(), s) {
			t.Errorf("stderr missing allowed sub %q; got: %s", s, stderr.String())
		}
	}
}

func TestCBM_NoCBMSock_Exit2(t *testing.T) {
	t.Setenv("CBM_SOCK", "")
	t.Setenv("CBM_PROJECT", "test-proj")
	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "get_architecture"}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Errorf("expected exit 2 for missing CBM_SOCK; got %d", code)
	}
	if !strings.Contains(stderr.String(), "CBM_SOCK not set") {
		t.Errorf("stderr missing expected message; got: %s", stderr.String())
	}
}

func TestCBM_NoCBMProject_Exit2(t *testing.T) {
	t.Setenv("CBM_SOCK", "/tmp/fake.sock")
	t.Setenv("CBM_PROJECT", "")
	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "get_architecture"}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Errorf("expected exit 2 for missing CBM_PROJECT; got %d", code)
	}
	if !strings.Contains(stderr.String(), "CBM_PROJECT not set") {
		t.Errorf("stderr missing expected message; got: %s", stderr.String())
	}
}

func TestCBM_UserProjectFlag_Exit2(t *testing.T) {
	t.Setenv("CBM_SOCK", "/tmp/fake.sock")
	t.Setenv("CBM_PROJECT", "proj")

	cases := [][]string{
		{"golemic", "cbm", "search_graph", "--project=other"},
		{"golemic", "cbm", "search_graph", "--project", "other"},
	}
	for _, args := range cases {
		var stderr bytes.Buffer
		// Reset cache so we don't hit an unset socket on tools/list fetch.
		setupFakeToolsList(t, nil)
		code := runCBM(args, &bytes.Buffer{}, &stderr)
		if code != 2 {
			t.Errorf("args %v: expected exit 2; got %d", args, code)
		}
		if !strings.Contains(stderr.String(), "--project is managed by golemic") {
			t.Errorf("args %v: stderr missing managed message; got: %s", args, stderr.String())
		}
	}
}

// ---- Tool call tests ----

func TestCBM_AllowedSubs_InjectProject(t *testing.T) { //nolint:funlen
	const project = "golemic-issue-42-dev"

	for _, sub := range cbmAllowedSubs {
		sub := sub
		t.Run(sub, func(t *testing.T) {
			// Reset schema cache per sub.
			origCache := toolsListCache
			toolsListCache = nil
			t.Cleanup(func() { toolsListCache = origCache })

			var captured []byte
			sockPath := fakeSocketServer(t, func(req []byte) []byte {
				var msg struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
					ID     int64           `json:"id"`
				}
				json.Unmarshal(req, &msg) //nolint:errcheck

				if msg.Method == "tools/list" {
					return toolsListResponse(msg.ID, []map[string]interface{}{
						{
							"name":        sub,
							"description": "test description",
							"inputSchema": map[string]interface{}{
								"properties": map[string]interface{}{
									"project": map[string]interface{}{"type": "string"},
								},
								"required": []string{"project"},
							},
						},
					})
				}
				captured = req
				return simpleMCPResponse(msg.ID, "ok")
			})

			t.Setenv("CBM_SOCK", sockPath)
			t.Setenv("CBM_PROJECT", project)

			var stdout, stderr bytes.Buffer
			code := runCBM([]string{"golemic", "cbm", sub}, &stdout, &stderr)
			if code != 0 {
				t.Errorf("sub %s: exit %d, stderr: %s", sub, code, stderr.String())
			}

			// Verify tools/call body.
			var callMsg struct {
				Method string `json:"method"`
				Params struct {
					Name      string                     `json:"name"`
					Arguments map[string]json.RawMessage `json:"arguments"`
				} `json:"params"`
			}
			if err := json.Unmarshal(captured, &callMsg); err != nil {
				t.Fatalf("sub %s: unmarshal tools/call: %v\ncaptured: %s", sub, err, captured)
			}
			if callMsg.Method != "tools/call" {
				t.Errorf("sub %s: method = %s, want tools/call", sub, callMsg.Method)
			}
			if callMsg.Params.Name != sub {
				t.Errorf("sub %s: params.name = %s, want %s", sub, callMsg.Params.Name, sub)
			}
			var projVal string
			if err := json.Unmarshal(callMsg.Params.Arguments["project"], &projVal); err != nil || projVal != project {
				t.Errorf("sub %s: arguments.project = %s, want %s", sub, callMsg.Params.Arguments["project"], project)
			}
		})
	}
}

func TestCBM_MissingSchemaForAllowedSub_Exit2(t *testing.T) {
	setupFakeToolsList(t, []toolEntry{{Name: "get_architecture", Description: "arch"}})
	t.Setenv("CBM_SOCK", "/tmp/cbm-missing-schema.sock")
	t.Setenv("CBM_PROJECT", "proj")

	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "search_graph", "--query=foo"}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for missing schema; got %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "missing inputSchema") {
		t.Fatalf("stderr missing missing-schema message; got: %s", stderr.String())
	}
}

func TestCBM_FlagTypeCoercion(t *testing.T) {
	const project = "golemic-issue-42-dev"
	var capturedArgs map[string]json.RawMessage

	sockPath := fakeSocketServer(t, func(req []byte) []byte {
		var msg struct {
			Method string `json:"method"`
			Params struct {
				Name      string                     `json:"name"`
				Arguments map[string]json.RawMessage `json:"arguments"`
			} `json:"params"`
			ID int64 `json:"id"`
		}
		json.Unmarshal(req, &msg) //nolint:errcheck
		if msg.Method == "tools/list" {
			return toolsListResponse(msg.ID, []map[string]interface{}{
				{
					"name":        "search_graph",
					"description": "search",
					"inputSchema": map[string]interface{}{
						"properties": map[string]interface{}{
							"project": map[string]interface{}{"type": "string"},
							"query":   map[string]interface{}{"type": "string"},
							"limit":   map[string]interface{}{"type": "integer"},
							"labels":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
						},
						"required": []string{"project"},
					},
				},
			})
		}
		capturedArgs = msg.Params.Arguments
		return simpleMCPResponse(msg.ID, "result")
	})

	setupFakeToolsList(t, nil) // clear cache
	t.Setenv("CBM_SOCK", sockPath)
	t.Setenv("CBM_PROJECT", project)
	toolsListCache = nil

	var stdout, stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "search_graph", "--query=foo", "--limit=5", "--labels=a,b,c"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit %d; stderr: %s", code, stderr.String())
	}

	// limit must be integer 5
	var limit int64
	if err := json.Unmarshal(capturedArgs["limit"], &limit); err != nil || limit != 5 {
		t.Errorf("limit: got %s, want 5 (integer)", capturedArgs["limit"])
	}
	// labels must be array
	var labels []string
	if err := json.Unmarshal(capturedArgs["labels"], &labels); err != nil || len(labels) != 3 {
		t.Errorf("labels: got %s, want [a b c]", capturedArgs["labels"])
	}
}

func TestCBM_UnknownFlag_Exit2(t *testing.T) {
	sockPath := fakeSocketServer(t, func(req []byte) []byte {
		var msg struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		json.Unmarshal(req, &msg) //nolint:errcheck
		if msg.Method == "tools/list" {
			return toolsListResponse(msg.ID, []map[string]interface{}{
				{
					"name":        "get_architecture",
					"description": "arch",
					"inputSchema": map[string]interface{}{
						"properties": map[string]interface{}{
							"project": map[string]interface{}{"type": "string"},
						},
						"required": []string{"project"},
					},
				},
			})
		}
		return simpleMCPResponse(msg.ID, "ok")
	})

	toolsListCache = nil
	t.Setenv("CBM_SOCK", sockPath)
	t.Setenv("CBM_PROJECT", "proj")
	t.Cleanup(func() { toolsListCache = nil })

	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "get_architecture", "--nonexistent=val"}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Errorf("expected exit 2 for unknown flag; got %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "nonexistent") {
		t.Errorf("stderr missing flag name; got: %s", stderr.String())
	}
}

func TestCBM_IsErrorResponse_Exit1(t *testing.T) {
	sockPath := fakeSocketServer(t, func(req []byte) []byte {
		var msg struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		json.Unmarshal(req, &msg) //nolint:errcheck
		if msg.Method == "tools/list" {
			return toolsListResponse(msg.ID, []map[string]interface{}{
				{
					"name":        "get_architecture",
					"description": "arch",
					"inputSchema": map[string]interface{}{
						"properties": map[string]interface{}{
							"project": map[string]interface{}{"type": "string"},
						},
						"required": []string{"project"},
					},
				},
			})
		}
		resp, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"result": map[string]interface{}{
				"content": []map[string]string{{"type": "text", "text": "error details"}},
				"isError": true,
			},
		})
		return resp
	})

	toolsListCache = nil
	t.Setenv("CBM_SOCK", sockPath)
	t.Setenv("CBM_PROJECT", "proj")
	t.Cleanup(func() { toolsListCache = nil })

	var stdout, stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "get_architecture"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit 1 for isError response; got %d", code)
	}
	if !strings.Contains(stdout.String(), "error details") {
		t.Errorf("stdout missing error content; got: %s", stdout.String())
	}
}

func TestCBM_BrokerJSONRPCError_Exit1(t *testing.T) {
	sockPath := fakeSocketServer(t, func(req []byte) []byte {
		var msg struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		json.Unmarshal(req, &msg) //nolint:errcheck
		if msg.Method == "tools/list" {
			return toolsListResponse(msg.ID, []map[string]interface{}{
				{
					"name":        "get_architecture",
					"description": "arch",
					"inputSchema": map[string]interface{}{
						"properties": map[string]interface{}{
							"project": map[string]interface{}{"type": "string"},
						},
						"required": []string{"project"},
					},
				},
			})
		}
		resp, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"error": map[string]interface{}{
				"code":    -32000,
				"message": "child-dead",
			},
		})
		return resp
	})

	toolsListCache = nil
	t.Setenv("CBM_SOCK", sockPath)
	t.Setenv("CBM_PROJECT", "proj")
	t.Cleanup(func() { toolsListCache = nil })

	var stdout, stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "get_architecture"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for broker JSON-RPC error; got %d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout for broker JSON-RPC error; got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "cbm: broker child-dead") {
		t.Fatalf("stderr missing broker error; got: %s", stderr.String())
	}
}

// ---- Help tests ----

func TestCBM_Help_RendersAllSubsWithProjectID(t *testing.T) {
	const project = "golemic-issue-99-dev"
	t.Setenv("CBM_PROJECT", project)
	t.Setenv("CBM_SOCK", "")

	fakeTool := func(name string) map[string]interface{} {
		return map[string]interface{}{
			"name":        name,
			"description": "desc for " + name,
			"inputSchema": map[string]interface{}{
				"properties": map[string]interface{}{
					"project": map[string]interface{}{"type": "string"},
					"query":   map[string]interface{}{"type": "string"},
				},
				"required": []string{"project", "query"},
			},
		}
	}

	var tools []toolEntry
	for _, sub := range cbmAllowedSubs {
		raw, _ := json.Marshal(fakeTool(sub))
		var te toolEntry
		json.Unmarshal(raw, &te) //nolint:errcheck
		tools = append(tools, te)
	}
	setupFakeHelpFetch(t, tools)
	toolsListCache = nil
	t.Cleanup(func() { toolsListCache = nil })

	var stdout, stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit %d; stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, project) {
		t.Errorf("help output missing project ID %q; got:\n%s", project, out)
	}
	for _, sub := range cbmAllowedSubs {
		if !strings.Contains(out, sub) {
			t.Errorf("help output missing sub %q", sub)
		}
		if !strings.Contains(out, "desc for "+sub) {
			t.Errorf("help output missing description for %q", sub)
		}
		if !strings.Contains(out, "Example:") {
			t.Errorf("help output missing Example line for %q", sub)
		}
	}
}

func TestCBM_HelpSub_RendersParameterTable(t *testing.T) {
	t.Setenv("CBM_SOCK", "")
	t.Setenv("CBM_PROJECT", "")

	tools := []toolEntry{
		{
			Name:        "search_graph",
			Description: "Search the graph.\n\nFull description.",
			InputSchema: inputSchema{
				Properties: map[string]propSchema{
					"project": {Type: "string", Description: "project name"},
					"query":   {Type: "string", Description: "search query"},
					"limit":   {Type: "integer", Description: "max results"},
					"labels":  {Type: "array", Description: "filter labels", Items: &propSchema{Type: "string"}},
				},
				Required: []string{"project", "query"},
			},
		},
	}
	setupFakeHelpFetch(t, tools)
	toolsListCache = nil
	t.Cleanup(func() { toolsListCache = nil })

	var stdout, stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "help", "search_graph"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit %d; stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{"project", "query", "limit", "labels", "(required)", "integer", "array<string>"} {
		if !strings.Contains(out, want) {
			t.Errorf("help search_graph: missing %q in output:\n%s", want, out)
		}
	}
}

func TestCBM_Help_WithoutSock_UsesOnDemandFetch(t *testing.T) {
	t.Setenv("CBM_SOCK", "")
	t.Setenv("CBM_PROJECT", "")

	called := false
	orig := cbmFetchToolsListDirectFn
	cbmFetchToolsListDirectFn = func() ([]toolEntry, error) {
		called = true
		return []toolEntry{{Name: "get_architecture", Description: "arch"}}, nil
	}
	t.Cleanup(func() { cbmFetchToolsListDirectFn = orig })
	toolsListCache = nil
	t.Cleanup(func() { toolsListCache = nil })

	var stdout, stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit %d; stderr: %s", code, stderr.String())
	}
	if !called {
		t.Error("on-demand fetch not called when CBM_SOCK is unset")
	}
}

func TestCBM_NewWhitelistEntry_AppearsInHelp(t *testing.T) {
	// Simulate adding "foo_tool" to the whitelist.
	origList := cbmAllowedSubs
	cbmAllowedSubs = append(cbmAllowedSubs, "foo_tool")
	t.Cleanup(func() { cbmAllowedSubs = origList })

	tools := []toolEntry{
		{Name: "foo_tool", Description: "Foo does foo things.", InputSchema: inputSchema{
			Properties: map[string]propSchema{
				"project": {Type: "string"},
				"x":       {Type: "integer"},
			},
			Required: []string{"project", "x"},
		}},
	}
	setupFakeHelpFetch(t, tools)
	toolsListCache = nil
	t.Cleanup(func() { toolsListCache = nil })

	t.Setenv("CBM_SOCK", "")
	t.Setenv("CBM_PROJECT", "")

	var stdout, stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "help"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit %d; stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "foo_tool") {
		t.Errorf("help missing foo_tool; got:\n%s", out)
	}
	if !strings.Contains(out, "Foo does foo things.") {
		t.Errorf("help missing foo_tool description; got:\n%s", out)
	}
	if !strings.Contains(out, "--x=<value>") {
		t.Errorf("help missing foo_tool required arg example; got:\n%s", out)
	}
}
