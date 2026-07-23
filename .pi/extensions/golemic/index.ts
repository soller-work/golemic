// golemic gm_ tool extension — forwards tool calls over the runner-owned unix
// socket (GOLEMIC_GM_SOCK) as JSON-RPC to the golemic broker. The broker holds
// GitHub credentials and performs all side effects; the agent process carries
// no token for these operations.
import * as net from "net";
import {
  isBrokerErrorDetails,
  normalizeBrokerErrorResult,
  normalizeBrokerSuccessResult,
} from "./broker-result.ts";

const SOCK_ENV = "GOLEMIC_GM_SOCK";

// callBroker sends { tool, callId, params } over the unix socket and returns
// the broker's result object. Rejects if the socket env var is unset or the
// connection fails.
function callBroker(tool: string, callId: string, params: unknown): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const sockPath = process.env[SOCK_ENV];
    if (!sockPath) {
      reject({ ok: false, code: "TRANSPORT_ERROR", message: `${SOCK_ENV} is not set` });
      return;
    }

    const conn = net.createConnection(sockPath);
    let buf = "";

    conn.on("data", (chunk: Buffer) => {
      buf += chunk.toString();
    });

    conn.on("end", () => {
      try {
        const resp = JSON.parse(buf.trim()) as { callId: string; result: unknown };
        resolve(resp.result);
      } catch (e) {
        reject({ ok: false, code: "TRANSPORT_ERROR", message: `failed to parse broker response: ${e}` });
      }
    });

    conn.on("error", (err: Error) => {
      reject({ ok: false, code: "TRANSPORT_ERROR", message: `socket error: ${err.message}` });
    });

    const payload = JSON.stringify({ tool, callId, params }) + "\n";
    conn.write(payload);
  });
}

async function executeBrokerTool(tool: string, callId: string, params: unknown): Promise<unknown> {
  try {
    return normalizeBrokerSuccessResult(await callBroker(tool, callId, params));
  } catch (error) {
    return normalizeBrokerErrorResult(error);
  }
}

export default function (pi: { registerTool: (def: object) => void; on: (event: "tool_result", handler: (event: { toolName: string; details: unknown }) => unknown) => void }) {
  pi.on("tool_result", async (event) => {
    if (!event.toolName.startsWith("gm_")) {
      return;
    }
    if (!isBrokerErrorDetails(event.details)) {
      return;
    }
    return { isError: true };
  });

  // gm_slice_get — fetches the issue Markdown body from the broker.
  // The broker caches the result for the duration of this invocation;
  // each new invocation re-fetches so edits between runs are seen.
  // (Future: structured multi-field document in a later slice.)
  pi.registerTool({
    name: "gm_slice_get",
    label: "Get task spec",
    description:
      "Fetch the authoritative task specification (issue Markdown body) " +
      "via the runner broker. Returns { ok: true, spec: string }. " +
      "Prefer this over shelling to `golemic slice` when available.",
    parameters: {
      type: "object",
      properties: {},
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_slice_get", callId, params);
    },
  });

  // gm_project_check — runs the canonical verify command in the dev worktree.
  // Dev-only: the broker allowlist exposes this tool only to the Dev role.
  pi.registerTool({
    name: "gm_project_check",
    label: "Run project verify check",
    description:
      "Run the project's configured verify command in the dev worktree and return " +
      "{ ok, command, exitCode, stdout, stderr, summary, workingTreeFingerprint }. " +
      "Optional parameter: output ('capped' | 'full'), default capped. Dev-only.",
    parameters: {
      type: "object",
      properties: {
        output: {
          type: "string",
          enum: ["capped", "full"],
          description: "Returned stdout/stderr volume: capped head+tail or full.",
        },
      },
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_project_check", callId, params);
    },
  });

  // gm_dev_done — signals that the dev agent has finished its work.
  // Stub in this slice: validates schema and echoes the payload.
  // No git/GitHub/event-log side effects until Slice 3.
  pi.registerTool({
    name: "gm_dev_done",
    label: "Signal dev work complete",
    description:
      "Signal that dev work is complete. " +
      "Required fields: summary (string), commitMsg (string), prTitle (string), prBody (string). " +
      "Returns { ok: true, accepted: true } or a schema/gate error.",
    parameters: {
      type: "object",
      properties: {
        summary: { type: "string", description: "Plain-language summary of changes made." },
        commitMsg: {
          type: "string",
          description: "Conventional commit message: type(scope): summary (NNN).",
        },
        prTitle: { type: "string", description: "Concise pull-request title." },
        prBody: {
          type: "string",
          description: "Pull-request description; must include `Closes #<issue>`.",
        },
      },
      required: ["summary", "commitMsg", "prTitle", "prBody"],
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_dev_done", callId, params);
    },
  });

  // gm_pr_view — returns PR metadata, unified diff, and changed-files list.
  // Reviewer-only: the broker allowlist exposes this tool only to the reviewer role.
  pi.registerTool({
    name: "gm_pr_view",
    label: "View pull request",
    description:
      "Fetch PR metadata, the unified diff, and the changed-files list for the current review PR. " +
      "Fetched runner-side with the reviewer token; no GitHub credential is needed in the agent. " +
      "Returns { ok: true, pr: { number, title, state, ... }, diff: string, changedFiles: [...] }. Reviewer-only.",
    parameters: {
      type: "object",
      properties: {},
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_pr_view", callId, params);
    },
  });

  // gm_repo_tree — returns a read-only directory listing of the reviewer worktree.
  // Reviewer-only: confined to the worktree root; path escapes return a structured error.
  pi.registerTool({
    name: "gm_repo_tree",
    label: "List repo directory",
    description:
      "List a directory inside the reviewer worktree. " +
      "Input: { path?: string } relative to the worktree root (default: root). " +
      "Returns { ok: true, path, entries: [ { name, type: 'file'|'dir' } ] }. " +
      "Returns { ok: false, code: 'PATH_OUTSIDE_WORKTREE' } for paths that escape the root. Reviewer-only.",
    parameters: {
      type: "object",
      properties: {
        path: {
          type: "string",
          description: "Relative path inside the reviewer worktree (default: root).",
        },
      },
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_repo_tree", callId, params);
    },
  });

  // gm_review_submit — submits the reviewer's verdict.
  // Stub in this slice: validates schema and echoes the payload.
  // No GitHub review submission until Slice 5.
  pi.registerTool({
    name: "gm_review_submit",
    label: "Submit review verdict",
    description:
      "Submit the reviewer's verdict. " +
      'Required fields: verdict ("approved" | "changes_requested"), ' +
      "mergeConfidence (string), body (string). " +
      "Returns { ok: true, echo: { verdict, mergeConfidence, body } } or a schema error.",
    parameters: {
      type: "object",
      properties: {
        verdict: {
          type: "string",
          enum: ["approved", "changes_requested"],
          description: "Review outcome.",
        },
        mergeConfidence: {
          type: "string",
          description: "Confidence level: high | medium | low.",
        },
        body: { type: "string", description: "Review body text." },
      },
      required: ["verdict", "mergeConfidence", "body"],
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_review_submit", callId, params);
    },
  });

  // gm_code_* — read-only code-intelligence tools proxied to the CBM broker.
  // The broker injects the project parameter; callers must not pass it.
  // Dev-only: only available when codebase-memory indexing succeeded.

  pi.registerTool({
    name: "gm_code_search_graph",
    label: "Search code graph",
    description:
      "Search the code knowledge graph for functions, classes, routes, and variables. " +
      "Use INSTEAD OF grep/glob when finding code definitions, implementations, or relationships. " +
      "Three search modes: (1) query for BM25 full-text search; (2) name_pattern for regex; " +
      "(3) semantic_query (array of strings) for vector search. " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {
        query: { type: "string", description: "Natural-language or keyword full-text search (BM25)." },
        label: { type: "string", description: "Node label filter (e.g. Function, Method, Route)." },
        name_pattern: { type: "string", description: "Regex pattern matched against node names." },
        qn_pattern: { type: "string", description: "Regex pattern matched against qualified names." },
        file_pattern: { type: "string", description: "Glob pattern to restrict by file path." },
        relationship: { type: "string", description: "Edge type filter (e.g. CALLS, IMPORTS)." },
        min_degree: { type: "integer", description: "Minimum node degree (in+out edges)." },
        max_degree: { type: "integer", description: "Maximum node degree." },
        exclude_entry_points: { type: "boolean" },
        include_connected: { type: "boolean", description: "Include directly connected nodes in results." },
        semantic_query: {
          type: "array",
          items: { type: "string" },
          description: "Array of keyword strings for vector cosine search (requires moderate/full index).",
        },
        limit: { type: "integer", description: "Max results per call (default 200)." },
        offset: { type: "integer", description: "Pagination offset." },
      },
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_search_graph", callId, params);
    },
  });

  pi.registerTool({
    name: "gm_code_query_graph",
    label: "Query code graph (Cypher)",
    description:
      "Execute a Cypher query against the knowledge graph for complex multi-hop patterns, " +
      "aggregations, and cross-service analysis. Hard 100k row ceiling. " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {
        query: { type: "string", description: "Cypher query string." },
        max_rows: { type: "integer", description: "Optional row limit (default: unlimited up to 100k)." },
      },
      required: ["query"],
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_query_graph", callId, params);
    },
  });

  pi.registerTool({
    name: "gm_code_trace_call_path",
    label: "Trace call/data path",
    description:
      "Trace paths through the code graph. Modes: calls (callers/callees), " +
      "data_flow (value propagation), cross_service (through HTTP/async Route nodes). " +
      "Use INSTEAD OF grep for callers, dependencies, or impact analysis. " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {
        function_name: { type: "string", description: "Function to trace from." },
        direction: {
          type: "string",
          enum: ["inbound", "outbound", "both"],
          description: "Traversal direction (default: both).",
        },
        depth: { type: "integer", description: "Max traversal depth (default: 3)." },
        mode: {
          type: "string",
          enum: ["calls", "data_flow", "cross_service"],
          description: "Traversal mode (default: calls).",
        },
        parameter_name: { type: "string", description: "For data_flow: scope to a specific parameter name." },
        edge_types: { type: "array", items: { type: "string" } },
        risk_labels: { type: "boolean", description: "Add risk classification per hop." },
        include_tests: { type: "boolean", description: "Include test files in results." },
      },
      required: ["function_name"],
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_trace_call_path", callId, params);
    },
  });

  pi.registerTool({
    name: "gm_code_get_snippet",
    label: "Get code snippet",
    description:
      "Read source code for a function, class, or symbol. " +
      "First call gm_code_search_graph to find the exact qualified_name, then pass it here. " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {
        qualified_name: {
          type: "string",
          description: "Full qualified_name from gm_code_search_graph, or short function name.",
        },
        include_neighbors: { type: "boolean", description: "Include neighboring nodes in result." },
      },
      required: ["qualified_name"],
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_get_snippet", callId, params);
    },
  });

  pi.registerTool({
    name: "gm_code_get_graph_schema",
    label: "Get graph schema",
    description:
      "Get the schema of the knowledge graph (node labels, edge types). " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {},
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_get_graph_schema", callId, params);
    },
  });

  pi.registerTool({
    name: "gm_code_get_architecture",
    label: "Get architecture overview",
    description:
      "Get high-level architecture overview — packages, services, dependencies, clusters, " +
      "and project structure at a glance. Optional path scopes analysis to a subdirectory. " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {
        path: { type: "string", description: "Optional directory prefix to scope analysis." },
        aspects: {
          type: "array",
          items: {
            type: "string",
            enum: ["all", "overview", "structure", "dependencies", "routes", "languages",
                   "packages", "entry_points", "hotspots", "boundaries", "layers",
                   "file_tree", "clusters"],
          },
          description: "Aspects to include. Default: all.",
        },
      },
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_get_architecture", callId, params);
    },
  });

  pi.registerTool({
    name: "gm_code_search",
    label: "Search code (text + graph)",
    description:
      "Graph-augmented code search. Finds text patterns via grep then enriches results " +
      "with the knowledge graph: deduplicates matches into containing functions, " +
      "ranks by structural importance. " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {
        pattern: { type: "string", description: "Search pattern (text or regex with regex:true)." },
        file_pattern: { type: "string", description: "Glob for grep --include (e.g. *.go)." },
        path_filter: { type: "string", description: "Regex filter on result file paths." },
        mode: {
          type: "string",
          enum: ["compact", "full", "files"],
          description: "Result verbosity: compact (default), full (with source), files (paths only).",
        },
        context: { type: "integer", description: "Lines of context around each match." },
        regex: { type: "boolean", description: "Treat pattern as a regex." },
        limit: { type: "integer", description: "Max enriched results per call (default 10)." },
      },
      required: ["pattern"],
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_search", callId, params);
    },
  });

  pi.registerTool({
    name: "gm_code_detect_changes",
    label: "Detect code changes",
    description:
      "Detect files and symbols changed since a git ref (commit SHA, branch, or tag). " +
      "Use to scope impact analysis after a merge or to understand what changed since main. " +
      "Returns { ok: true, content: string }.",
    parameters: {
      type: "object",
      properties: {
        since: { type: "string", description: "Git ref to compare against (e.g. main, HEAD~1, a3f8c2d)." },
        path: { type: "string", description: "Optional path prefix to scope the analysis." },
      },
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return executeBrokerTool("gm_code_detect_changes", callId, params);
    },
  });
}
