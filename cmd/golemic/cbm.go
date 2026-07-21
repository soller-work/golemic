package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// cbmAllowedSubs is the security whitelist. Adding a name here is the only
// Golemic-code change needed to expose a new upstream CBM tool; descriptions
// and argument schemas come live from tools/list (BR-C5).
var cbmAllowedSubs = []string{
	"search_graph", "trace_call_path", "query_graph",
	"get_architecture", "get_graph_schema", "get_code_snippet",
	"search_code", "detect_changes",
}

// toolEntry is one entry from the MCP tools/list response.
type toolEntry struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Properties map[string]propSchema `json:"properties"`
	Required   []string              `json:"required"`
}

type propSchema struct {
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Items       *propSchema `json:"items,omitempty"` // for array type
}

// toolsListCache holds the fetched schema for the process lifetime (BR-C4).
var toolsListCache []toolEntry

// runCBM dispatches `golemic cbm <sub> [flags…]` via JSON-RPC to the broker.
// stdout/stderr are injectable for tests.
func runCBM(args []string, stdout, stderr io.Writer) int {
	// args: ["golemic", "cbm", <sub>, ...]
	if len(args) < 3 {
		fmt.Fprintf(stderr, "Usage: golemic cbm <sub> [--k=v …]\nRun 'golemic cbm help' for available subcommands.\n")
		return 1
	}

	sub := args[2]
	flagArgs := args[3:]

	// help and help <sub> are special: they work even without a broker.
	if sub == "help" {
		if len(flagArgs) > 0 {
			return runCBMHelpSub(flagArgs[0], stdout, stderr)
		}
		return runCBMHelp(stdout, stderr)
	}

	// Whitelist check (BR-C1).
	if !isAllowed(sub) {
		fmt.Fprintf(stderr, "unknown cbm subcommand %q; allowed: %s\n", sub, strings.Join(cbmAllowedSubs, ", "))
		return 1
	}

	// Reject user-supplied --project (BR-C2).
	if err := rejectProjectFlag(flagArgs); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	// Require CBM_SOCK and CBM_PROJECT for non-help subs (BR-C3).
	sockPath := os.Getenv("CBM_SOCK")
	if sockPath == "" {
		fmt.Fprintln(stderr, "CBM_SOCK not set; golemic cbm runs only inside a golemic-managed run")
		return 2
	}
	project := os.Getenv("CBM_PROJECT")
	if project == "" {
		fmt.Fprintln(stderr, "CBM_PROJECT not set; golemic cbm runs only inside a golemic-managed run")
		return 2
	}

	// Fetch and cache schema (BR-C4).
	if toolsListCache == nil {
		tools, err := fetchToolsListFromSocket(sockPath)
		if err != nil {
			fmt.Fprintf(stderr, "cbm: broker unreachable at %s: %v\n", sockPath, err)
			return 1
		}
		toolsListCache = tools
	}

	// Resolve schema for this sub.
	schema := findSchema(toolsListCache, sub)

	// Parse and validate flags (BR-C4).
	arguments, exitCode := buildArguments(sub, flagArgs, schema, project, stderr)
	if exitCode != 0 {
		return exitCode
	}

	// Send tools/call.
	result, isError, err := callTool(sockPath, sub, arguments)
	if err != nil {
		fmt.Fprintf(stderr, "cbm: broker unreachable at %s: %v\n", sockPath, err)
		return 1
	}

	// Stream content to stdout.
	for _, c := range result {
		fmt.Fprint(stdout, c.Text)
	}

	if isError {
		return 1
	}
	return 0
}

// runCBMHelp renders the tool overview from tools/list, working on-demand
// even without an active broker (BR-C3, BR-C8).
func runCBMHelp(stdout, stderr io.Writer) int {
	tools, err := fetchToolsListForHelp(stderr)
	if err != nil {
		return 1
	}

	project := os.Getenv("CBM_PROJECT")
	if project == "" {
		project = "(not inside a golemic run)"
	}

	fmt.Fprintf(stdout, "CBM Project: %s\n", project)
	fmt.Fprintf(stdout, "Usage: golemic cbm <sub> [--key=value …]\n\n")

	for _, sub := range cbmAllowedSubs {
		entry := findEntry(tools, sub)
		desc := "(description not available)"
		if entry != nil {
			desc = firstLine(entry.Description)
		}
		// Build example call with required args.
		var exampleArgs []string
		if entry != nil {
			for _, req := range entry.InputSchema.Required {
				if req == "project" {
					continue // injected automatically
				}
				exampleArgs = append(exampleArgs, fmt.Sprintf("--%s=<value>", req))
			}
		}
		example := "golemic cbm " + sub
		if len(exampleArgs) > 0 {
			example += " " + strings.Join(exampleArgs, " ")
		}
		fmt.Fprintf(stdout, "%s\n  %s\n  Example: %s\n\n", sub, desc, example)
	}
	return 0
}

// runCBMHelpSub renders the full parameter table for a single sub.
func runCBMHelpSub(sub string, stdout, stderr io.Writer) int {
	if !isAllowed(sub) {
		fmt.Fprintf(stderr, "unknown cbm subcommand %q; allowed: %s\n", sub, strings.Join(cbmAllowedSubs, ", "))
		return 1
	}

	tools, err := fetchToolsListForHelp(stderr)
	if err != nil {
		return 1
	}

	entry := findEntry(tools, sub)
	if entry == nil {
		fmt.Fprintf(stderr, "cbm: tool %q not found in upstream tools/list\n", sub)
		return 1
	}

	fmt.Fprintf(stdout, "Tool: %s\n\n%s\n\nParameters:\n", entry.Name, entry.Description)

	isRequired := make(map[string]bool, len(entry.InputSchema.Required))
	for _, r := range entry.InputSchema.Required {
		isRequired[r] = true
	}
	for name, prop := range entry.InputSchema.Properties {
		typStr := formatType(prop)
		req := ""
		if isRequired[name] {
			req = " (required)"
		}
		desc := prop.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(stdout, "  --%s\t%s%s\t%s\n", name, typStr, req, desc)
	}
	return 0
}

// fetchToolsListForHelp fetches tools/list either from the broker socket (if
// CBM_SOCK is set) or by spawning a temporary npx MCP process (BR-C3).
func fetchToolsListForHelp(stderr io.Writer) ([]toolEntry, error) {
	if toolsListCache != nil {
		return toolsListCache, nil
	}
	sockPath := os.Getenv("CBM_SOCK")
	if sockPath != "" {
		tools, err := fetchToolsListFromSocket(sockPath)
		if err != nil {
			fmt.Fprintf(stderr, "cbm: broker unreachable at %s: %v\n", sockPath, err)
			return nil, err
		}
		toolsListCache = tools
		return tools, nil
	}
	// On-demand: start a short-lived npx MCP process just for tools/list.
	tools, err := cbmFetchToolsListDirect()
	if err != nil {
		fmt.Fprintf(stderr, "cbm: failed to fetch tool list: %v\n", err)
		return nil, err
	}
	return tools, nil
}

// cbmFetchToolsListDirect spawns a temporary npx MCP process, performs the
// MCP initialize handshake, fetches tools/list, and kills the process.
// Override in tests via cbmFetchToolsListDirectFn.
var cbmFetchToolsListDirectFn = fetchToolsListDirectImpl

func cbmFetchToolsListDirect() ([]toolEntry, error) {
	return cbmFetchToolsListDirectFn()
}

func fetchToolsListDirectImpl() ([]toolEntry, error) {
	cmd := exec.Command("npx", "-y", "codebase-memory-mcp@0.9.0")
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start npx: %w", err)
	}
	defer func() {
		stdin.Close()      //nolint:errcheck
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
	}()

	reader := bufio.NewReaderSize(stdoutPipe, 4<<20)

	// initialize handshake
	initReq, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo":      map[string]interface{}{"name": "golemic", "version": "1.0"},
		},
	})
	if _, err := fmt.Fprintf(stdin, "%s\n", initReq); err != nil {
		return nil, fmt.Errorf("send initialize: %w", err)
	}
	if err := readUntilID(reader, 1); err != nil {
		return nil, fmt.Errorf("initialize response: %w", err)
	}

	notification, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	fmt.Fprintf(stdin, "%s\n", notification) //nolint:errcheck

	// tools/list
	listReq, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	fmt.Fprintf(stdin, "%s\n", listReq) //nolint:errcheck

	line, err := readLineWithID(reader, 2)
	if err != nil {
		return nil, fmt.Errorf("tools/list response: %w", err)
	}
	return parseToolsList(line)
}

// readUntilID reads lines until it finds one with the given integer id.
func readUntilID(r *bufio.Reader, id int64) error {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return err
		}
		var msg struct {
			ID *int64 `json:"id"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.ID != nil && *msg.ID == id {
			return nil
		}
	}
}

// readLineWithID reads lines until it finds one with the given integer id and
// returns the raw line.
func readLineWithID(r *bufio.Reader, id int64) ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg struct {
			ID *int64 `json:"id"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.ID != nil && *msg.ID == id {
			return line, nil
		}
	}
}

// fetchToolsListFromSocket sends a tools/list request to the broker socket.
func fetchToolsListFromSocket(sockPath string) ([]toolEntry, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	raw, err := doSocketRPC(sockPath, req)
	if err != nil {
		return nil, err
	}
	return parseToolsList(raw)
}

// parseToolsList parses the tools/list response line into toolEntry slice.
func parseToolsList(line []byte) ([]toolEntry, error) {
	var resp struct {
		Result struct {
			Tools []toolEntry `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("parse tools/list response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}
	return resp.Result.Tools, nil
}

// callTool sends a tools/call JSON-RPC request and returns the content items,
// the isError flag, and any transport error.
func callTool(sockPath, name string, arguments map[string]interface{}) ([]contentItem, bool, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      name,
			"arguments": arguments,
		},
	}
	raw, err := doSocketRPC(sockPath, req)
	if err != nil {
		return nil, false, err
	}

	var resp struct {
		Result struct {
			Content []contentItem `json:"content"`
			IsError bool          `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, true, fmt.Errorf("parse tools/call response: %w", err)
	}
	if resp.Error != nil {
		return []contentItem{{Text: resp.Error.Message}}, true, nil
	}
	return resp.Result.Content, resp.Result.IsError, nil
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// doSocketRPC connects to the unix socket, sends req as a JSON line, reads one
// JSON line response, and returns the raw bytes.
func doSocketRPC(sockPath string, req interface{}) ([]byte, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", sockPath, err)
	}
	defer conn.Close()

	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	reader := bufio.NewReaderSize(conn, 4<<20)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return bytes.TrimSpace(line), nil
}

// buildArguments parses flagArgs, validates against schema, injects project,
// and returns the arguments map. Returns exit code 0 on success, 2 on guard
// violation.
func buildArguments(sub string, flagArgs []string, schema *inputSchema, project string, stderr io.Writer) (map[string]interface{}, int) {
	if schema == nil {
		fmt.Fprintf(stderr, "cbm: tool %q missing inputSchema in upstream tools/list\n", sub)
		return nil, 2
	}

	raw, err := parseFlags(flagArgs)
	if err != nil {
		fmt.Fprintf(stderr, "cbm: %v\n", err)
		return nil, 2
	}

	if err := validateFlagArgs(sub, raw, schema, stderr); err != nil {
		return nil, 2
	}

	coerced, err := coerceArguments(raw, schema)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 2
	}

	arguments := make(map[string]interface{}, len(coerced)+1)
	arguments["project"] = project
	for k, v := range coerced {
		arguments[k] = v
	}
	return arguments, 0
}

func validateFlagArgs(sub string, raw map[string]string, schema *inputSchema, stderr io.Writer) error {
	if schema == nil {
		return nil
	}
	allowed := allowedSchemaKeys(schema)
	for k := range raw {
		if _, ok := schema.Properties[k]; !ok {
			fmt.Fprintf(stderr, "unknown argument --%s for tool %s (allowed: %s)\n", k, sub, strings.Join(allowed, ", "))
			return fmt.Errorf("unknown argument --%s", k)
		}
	}
	return nil
}

func allowedSchemaKeys(schema *inputSchema) []string {
	allowed := make([]string, 0, len(schema.Properties))
	for k := range schema.Properties {
		if k != "project" {
			allowed = append(allowed, k)
		}
	}
	return allowed
}

func coerceArguments(raw map[string]string, schema *inputSchema) (map[string]interface{}, error) {
	arguments := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		prop, ok := schema.Properties[k]
		if !ok {
			arguments[k] = v
			continue
		}
		coerced, err := coerceValue(k, v, prop.Type)
		if err != nil {
			return nil, err
		}
		arguments[k] = coerced
	}
	return arguments, nil
}

// parseFlags parses GNU-style --key=value or --key value flag pairs.
// Boolean flags without a value (--flag as last arg or followed by another flag)
// are set to "true".
func parseFlags(args []string) (map[string]string, error) {
	result := make(map[string]string)
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return nil, fmt.Errorf("unexpected positional argument: %q", arg)
		}
		key := strings.TrimPrefix(arg, "--")
		if idx := strings.IndexByte(key, '='); idx >= 0 {
			result[key[:idx]] = key[idx+1:]
			i++
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			result[key] = args[i+1]
			i += 2
		} else {
			result[key] = "true"
			i++
		}
	}
	return result, nil
}

// coerceValue converts a string flag value to the JSON type dictated by the schema.
func coerceValue(key, raw, typ string) (interface{}, error) {
	switch typ {
	case "integer":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("flag --%s: expected integer, got %q", key, raw)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("flag --%s: expected number, got %q", key, raw)
		}
		return f, nil
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("flag --%s: expected boolean, got %q", key, raw)
		}
		return b, nil
	case "array":
		if raw == "" {
			return []string{}, nil
		}
		return strings.Split(raw, ","), nil
	default:
		return raw, nil
	}
}

// rejectProjectFlag returns an error if any flag named "project" appears in args.
func rejectProjectFlag(args []string) error {
	for _, a := range args {
		if a == "--project" || strings.HasPrefix(a, "--project=") {
			return fmt.Errorf("--project is managed by golemic; do not pass it manually")
		}
	}
	return nil
}

func isAllowed(sub string) bool {
	for _, s := range cbmAllowedSubs {
		if s == sub {
			return true
		}
	}
	return false
}

func findSchema(tools []toolEntry, name string) *inputSchema {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i].InputSchema
		}
	}
	return nil
}

func findEntry(tools []toolEntry, name string) *toolEntry {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func formatType(p propSchema) string {
	if p.Type == "array" {
		itemType := "string"
		if p.Items != nil && p.Items.Type != "" {
			itemType = p.Items.Type
		}
		return "array<" + itemType + ">"
	}
	return p.Type
}
