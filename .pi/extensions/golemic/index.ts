// golemic gm_ tool extension — forwards tool calls over the runner-owned unix
// socket (GOLEMIC_GM_SOCK) as JSON-RPC to the golemic broker. The broker holds
// GitHub credentials and performs all side effects; the agent process carries
// no token for these operations.
import * as net from "net";

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

export default function (pi: { registerTool: (def: object) => void }) {
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
      return callBroker("gm_slice_get", callId, params);
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
      "Required fields: summary (string), commitMsg (string). " +
      "Returns { ok: true, echo: { summary, commitMsg } } or a schema error.",
    parameters: {
      type: "object",
      properties: {
        summary: { type: "string", description: "Plain-language summary of changes made." },
        commitMsg: {
          type: "string",
          description: "Conventional commit message: type(scope): summary (NNN).",
        },
      },
      required: ["summary", "commitMsg"],
      additionalProperties: false,
    },
    async execute(callId: string, params: unknown): Promise<unknown> {
      return callBroker("gm_dev_done", callId, params);
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
      return callBroker("gm_review_submit", callId, params);
    },
  });
}
