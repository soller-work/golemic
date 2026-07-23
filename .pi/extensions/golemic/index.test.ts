import { beforeEach, describe, expect, it, vi } from "vitest";
import { isBrokerErrorDetails } from "./broker-result.ts";

const createConnection = vi.hoisted(() => vi.fn());

vi.mock("net", () => ({
  createConnection,
}));

function makePi() {
  const tools = new Map<string, { execute: (callId: string, params: unknown) => Promise<unknown> }>();
  const handlers = new Map<string, Array<(event: { toolName: string; details: unknown }) => unknown>>();

  return {
    tools,
    handlers,
    api: {
      registerTool(def: { name: string; execute: (callId: string, params: unknown) => Promise<unknown> }) {
        tools.set(def.name, def);
      },
      on(event: string, handler: (event: { toolName: string; details: unknown }) => unknown) {
        const list = handlers.get(event) ?? [];
        list.push(handler);
        handlers.set(event, list);
      },
    },
  };
}

function makeBrokerConnection(result: unknown) {
  const handlers = new Map<string, Array<(chunk?: Buffer) => void>>();
  return {
    on(event: string, handler: (chunk?: Buffer) => void) {
      const list = handlers.get(event) ?? [];
      list.push(handler);
      handlers.set(event, list);
      return this;
    },
    write() {
      setImmediate(() => {
        const payload = JSON.stringify({ callId: "call-1", result }) + "\n";
        for (const handler of handlers.get("data") ?? []) {
          handler(Buffer.from(payload));
        }
        for (const handler of handlers.get("end") ?? []) {
          handler();
        }
      });
    },
  };
}

async function loadExtension() {
  const { default: extension } = await import("./index.ts");
  return extension;
}

describe("golemic extension", () => {
  beforeEach(() => {
    createConnection.mockReset();
    delete process.env.GOLEMIC_GM_SOCK;
  });

  it("normalizes scalar broker payloads before Pi sees them", async () => {
    process.env.GOLEMIC_GM_SOCK = "/tmp/gm.sock";
    createConnection.mockReturnValue(makeBrokerConnection({ ok: true, content: "text payload" }));

    const { api, tools, handlers } = makePi();
    const extension = await loadExtension();
    extension(api as never);

    const result = await tools.get("gm_slice_get")!.execute("call-1", {});

    expect(result).toEqual({
      content: [{ type: "text", text: "text payload" }],
      details: { ok: true, content: "text payload" },
    });
    expect(await handlers.get("tool_result")![0]({ toolName: "gm_slice_get", details: (result as { details: unknown }).details })).toBeUndefined();
  });

  it("marks broker validation payloads as errors on the execute path", async () => {
    process.env.GOLEMIC_GM_SOCK = "/tmp/gm.sock";
    createConnection.mockReturnValue(
      makeBrokerConnection({ ok: false, code: "BAD_ARGUMENTS", message: "invalid query" }),
    );

    const { api, tools, handlers } = makePi();
    const extension = await loadExtension();
    extension(api as never);

    const result = await tools.get("gm_code_search_graph")!.execute("call-2", { query: "x" });
    const patch = await handlers.get("tool_result")![0]({
      toolName: "gm_code_search_graph",
      details: (result as { details: unknown }).details,
    });

    expect(result).toEqual({
      content: [{ type: "text", text: "invalid query" }],
      details: { ok: false, code: "BAD_ARGUMENTS", message: "invalid query" },
    });
    expect(isBrokerErrorDetails((result as { details: unknown }).details)).toBe(true);
    expect(patch).toEqual({ isError: true });
  });

  it("marks broker transport failures as errors", async () => {
    process.env.GOLEMIC_GM_SOCK = "/tmp/gm.sock";
    createConnection.mockReturnValue(makeBrokerConnection({ ok: false, code: "TRANSPORT_ERROR", message: "socket error" }));

    const { api, tools, handlers } = makePi();
    const extension = await loadExtension();
    extension(api as never);

    const result = await tools.get("gm_code_search_graph")!.execute("call-3", { query: "x" });
    const patch = await handlers.get("tool_result")![0]({ toolName: "gm_code_search_graph", details: (result as { details: unknown }).details });

    expect(result).toEqual({
      content: [{ type: "text", text: "socket error" }],
      details: { ok: false, code: "TRANSPORT_ERROR", message: "socket error" },
    });
    expect(isBrokerErrorDetails((result as { details: unknown }).details)).toBe(true);
    expect(patch).toEqual({ isError: true });
  });

  it("returns an error-shaped result when the transport is unavailable", async () => {
    const { api, tools, handlers } = makePi();
    const extension = await loadExtension();
    extension(api as never);

    const result = await tools.get("gm_code_search_graph")!.execute("call-3", { query: "x" });
    const patch = await handlers.get("tool_result")![0]({ toolName: "gm_code_search_graph", details: (result as { details: unknown }).details });

    expect(result).toEqual({
      content: [{ type: "text", text: "GOLEMIC_GM_SOCK is not set" }],
      details: { ok: false, code: "TRANSPORT_ERROR", message: "GOLEMIC_GM_SOCK is not set" },
    });
    expect(isBrokerErrorDetails((result as { details: unknown }).details)).toBe(true);
    expect(patch).toEqual({ isError: true });
  });
});
